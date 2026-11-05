package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// JobSpec describes a single fuzzing job to be executed by a worker.
type JobSpec struct {
	WorkerID        string
	Project         string
	RepoDir         string // Module directory where `go test` should run
	PkgImport       string
	PkgRelDir       string
	TargetName      string
	FuzzTime        time.Duration
	CorpusDir       string
	CleanCrashInGit bool // If true, remove newly generated testdata/fuzz crash file after copying to corpus tree
}

// CrashArtifact holds details of a crash discovered during a fuzz run.
type CrashArtifact struct {
	InputHash        string
	StackFingerprint string
	MinimizedInput   string
	Stack            string
}

// JobResult contains parsed metrics, corpus size, coverage, and crash details from a completed fuzz run.
type JobResult struct {
	Started          time.Time
	DurationSec      float64
	Execs            int64
	ExecsPerSec      float64
	NewInteresting   int
	CorpusCount      int
	Crashes          int
	CoveragePct      float64
	CoverageMeasured bool
	Status           string
	Output           string
	CrashRecords     []CrashArtifact
}

// WorkerRunner abstracts fuzz job execution so remote workers can be added alongside LocalRunner.
type WorkerRunner interface {
	RunTarget(ctx context.Context, job JobSpec) (JobResult, error)
}

// LocalRunner executes `go test -fuzz=...` in local subprocesses capped at GOMAXPROCS=1 per worker.
type LocalRunner struct{}

// NewLocalRunner creates a new LocalRunner.
func NewLocalRunner() *LocalRunner {
	return &LocalRunner{}
}

var (
	// Matches: fuzz: elapsed: 3s, execs: 145203 (48392/sec), new interesting: 4 (total: 16)
	reFuzzProgress = regexp.MustCompile(`execs:\s*(\d+)\s*\((\d+(?:\.\d+)?)/sec\)(?:,\s*new interesting:\s*(\d+))?(?:\s*\(total:\s*(\d+)\))?`)
	// Matches: Failing input written to testdata/fuzz/FuzzFoo/1234abcd...
	reFailingInput = regexp.MustCompile(`Failing input written to\s+(\S+)`)
	// Matches: coverage: 76.5% of statements
	reCoverage = regexp.MustCompile(`coverage:\s*(\d+(?:\.\d+)?)%\s*of statements`)

	reFileLine     = regexp.MustCompile(`(?m)([A-Za-z0-9_./\\-]+\.go):\d+(\s*\+0x[0-9a-fA-F]+)?`)
	reHexAddr      = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	reGoroutine    = regexp.MustCompile(`goroutine \d+`)
	reFailDuration = regexp.MustCompile(`\([0-9.]+s\)`)
)

// RunTarget executes `go test -fuzz` with GOMAXPROCS=1 and -parallel=1, copies any crash inputs into
// the persistent corpus tree (hashing the input bytes), and measures corpus coverage in an isolated
// temporary copy of the module.
func (r *LocalRunner) RunTarget(ctx context.Context, job JobSpec) (JobResult, error) {
	if err := ctx.Err(); err != nil {
		return JobResult{}, err
	}

	started := time.Now().UTC()
	fuzzTime := job.FuzzTime
	if fuzzTime <= 0 {
		fuzzTime = 10 * time.Second
	}

	absCorpusRoot, err := filepath.Abs(job.CorpusDir)
	if err != nil {
		absCorpusRoot = job.CorpusDir
	}
	pkgSeg := sanitizePathSegment(job.PkgImport)
	targetCacheRoot := filepath.Join(absCorpusRoot, "cache", pkgSeg)
	if err := os.MkdirAll(targetCacheRoot, 0o755); err != nil {
		return JobResult{Started: started, Status: "error"}, fmt.Errorf("mkdir corpus cache %q: %w", targetCacheRoot, err)
	}

	// One-time automatic migration of any legacy corpus layout (<corpus>/<pkg>/<Target>/<Target>/*)
	_ = MigrateLegacyCorpus(absCorpusRoot, pkgSeg, job.TargetName, targetCacheRoot)

	pkgArg := job.PkgRelDir
	if pkgArg == "" {
		pkgArg = "."
	} else if pkgArg != "." && !strings.HasPrefix(pkgArg, "./") {
		pkgArg = "./" + pkgArg
	}

	exactPattern := fmt.Sprintf("^%s$", regexp.QuoteMeta(job.TargetName))
	args := []string{
		"test",
		"-parallel=1",
		"-run=" + exactPattern,
		"-fuzz=" + exactPattern,
		"-fuzztime=" + fuzzTime.String(),
		"-test.fuzzcachedir=" + targetCacheRoot,
		pkgArg,
	}

	runCtx, cancel := context.WithTimeout(ctx, fuzzTime+45*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "go", args...)
	cmd.Dir = job.RepoDir
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 5 * time.Second

	outBytes, runErr := cmd.CombinedOutput()
	if err := ctx.Err(); err != nil {
		return JobResult{Started: started, Output: string(outBytes)}, err
	}

	duration := time.Since(started).Seconds()
	output := string(outBytes)

	res := ParseFuzzOutput(output, started, duration, runErr)

	// Persist crash artifacts into the corpus tree and hash their actual bytes
	pkgDir := job.RepoDir
	if pkgArg != "." {
		pkgDir = filepath.Join(job.RepoDir, strings.TrimPrefix(pkgArg, "./"))
	}
	crashStoreDir := filepath.Join(absCorpusRoot, "crashes", pkgSeg, job.TargetName)
	for i := range res.CrashRecords {
		rec := &res.CrashRecords[i]
		if rec.MinimizedInput != "" && rec.MinimizedInput != "inline-failure" {
			srcPath := rec.MinimizedInput
			if !filepath.IsAbs(srcPath) {
				srcPath = filepath.Join(pkgDir, srcPath)
			}
			if persistedPath, contentHash, copyErr := persistCrashInput(srcPath, crashStoreDir, job.CleanCrashInGit); copyErr == nil {
				rec.MinimizedInput = persistedPath
				rec.InputHash = contentHash
			}
		}
	}

	// Count cached corpus entries in <targetCacheRoot>/<TargetName>
	cacheTargetDir := filepath.Join(targetCacheRoot, job.TargetName)
	if cachedFiles := listRegularFiles(cacheTargetDir); len(cachedFiles) > res.CorpusCount {
		res.CorpusCount = len(cachedFiles)
	}

	// Measure statement coverage across f.Add seeds + testdata/fuzz + cached corpus entries in an isolated temp copy
	if !res.CoverageMeasured && ctx.Err() == nil {
		if cov, measured, covErr := measureCorpusCoverage(ctx, job.RepoDir, pkgArg, exactPattern, job.TargetName, cacheTargetDir); covErr == nil && measured {
			res.CoveragePct = cov
			res.CoverageMeasured = true
		}
	}

	return res, nil
}

// MigrateLegacyCorpus moves corpus entries from the older <corpusRoot>/<pkgSeg>/<targetName>[/<targetName>]
// layout into the new <targetCacheRoot>/<targetName> directory if present.
func MigrateLegacyCorpus(absCorpusRoot, pkgSeg, targetName, targetCacheRoot string) error {
	legacyOuter := filepath.Join(absCorpusRoot, pkgSeg, targetName)
	if !dirExists(legacyOuter) {
		return nil
	}
	dstDir := filepath.Join(targetCacheRoot, targetName)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	legacyInner := filepath.Join(legacyOuter, targetName)
	for _, srcDir := range []string{legacyInner, legacyOuter} {
		for _, f := range listRegularFiles(srcDir) {
			dst := filepath.Join(dstDir, filepath.Base(f))
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				_ = copyFile(f, dst)
			}
			_ = os.Remove(f)
		}
	}
	_ = os.Remove(legacyInner)
	_ = os.Remove(legacyOuter)
	_ = os.Remove(filepath.Join(absCorpusRoot, pkgSeg))
	return nil
}

// ParseFuzzOutput extracts execution metrics, coverage, status, stack fingerprints, and crash artifacts.
func ParseFuzzOutput(output string, started time.Time, durationSec float64, runErr error) JobResult {
	if math.IsNaN(durationSec) || math.IsInf(durationSec, 0) || durationSec <= 0 {
		durationSec = 0.001
	}
	res := JobResult{
		Started:     started,
		DurationSec: durationSec,
		Status:      "ok",
		Output:      output,
	}

	var lastRate float64
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if m := reFuzzProgress.FindStringSubmatch(line); m != nil {
			if execs, err := strconv.ParseInt(m[1], 10, 64); err == nil && execs >= res.Execs {
				res.Execs = execs
			}
			if rate, err := strconv.ParseFloat(m[2], 64); err == nil && !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate > 0 {
				lastRate = rate
			}
			if len(m) > 3 && m[3] != "" {
				if ni, err := strconv.Atoi(m[3]); err == nil && ni >= res.NewInteresting {
					res.NewInteresting = ni
				}
			}
			if len(m) > 4 && m[4] != "" {
				if total, err := strconv.Atoi(m[4]); err == nil && total >= res.CorpusCount {
					res.CorpusCount = total
				}
			}
		}
		if m := reCoverage.FindStringSubmatch(line); m != nil {
			if cov, err := strconv.ParseFloat(m[1], 64); err == nil && !math.IsNaN(cov) && !math.IsInf(cov, 0) {
				res.CoveragePct = cov
				res.CoverageMeasured = true
			}
		}
		if m := reFailingInput.FindStringSubmatch(line); m != nil {
			artifactPath := strings.TrimSpace(m[1])
			hash := filepath.Base(artifactPath)
			stack := extractCrashStack(output)
			res.CrashRecords = append(res.CrashRecords, CrashArtifact{
				InputHash:        hash,
				StackFingerprint: FingerprintStack(stack),
				MinimizedInput:   artifactPath,
				Stack:            stack,
			})
		}
	}

	if res.Execs > 0 {
		if rate := float64(res.Execs) / durationSec; !math.IsNaN(rate) && !math.IsInf(rate, 0) {
			res.ExecsPerSec = rate
		}
	}
	if lastRate > 0 && res.ExecsPerSec == 0 {
		res.ExecsPerSec = lastRate
	}

	if len(res.CrashRecords) > 0 {
		res.Crashes = len(res.CrashRecords)
		res.Status = "crash"
		return res
	}

	if runErr != nil {
		if strings.Contains(output, "--- FAIL:") || strings.Contains(output, "panic:") || strings.Contains(output, "fuzzing process hung or terminated unexpectedly") {
			stack := extractCrashStack(output)
			fp := FingerprintStack(stack)
			sum := sha256.Sum256([]byte(stack))
			hash := hex.EncodeToString(sum[:16])
			res.CrashRecords = append(res.CrashRecords, CrashArtifact{
				InputHash:        hash,
				StackFingerprint: fp,
				MinimizedInput:   "inline-failure",
				Stack:            stack,
			})
			res.Crashes = 1
			res.Status = "crash"
		} else {
			res.Status = "error"
		}
	}

	return res
}

// FingerprintStack normalizes a crash stack by stripping directory paths, line numbers,
// test durations, hex pointers, and goroutine IDs so line shifts do not create duplicate crash rows.
func FingerprintStack(stack string) string {
	trimmed := strings.TrimSpace(stack)
	if trimmed == "" {
		return ""
	}
	normalized := reFileLine.ReplaceAllStringFunc(trimmed, func(m string) string {
		colon := strings.IndexByte(m, ':')
		if colon < 0 {
			return m
		}
		return filepath.Base(m[:colon])
	})
	normalized = reFailDuration.ReplaceAllString(normalized, "(0s)")
	normalized = reHexAddr.ReplaceAllString(normalized, "0xADDR")
	normalized = reGoroutine.ReplaceAllString(normalized, "goroutine N")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:12])
}

func persistCrashInput(srcPath, crashStoreDir string, removeOriginal bool) (string, string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	contentHash := hex.EncodeToString(sum[:16])

	if err := os.MkdirAll(crashStoreDir, 0o755); err != nil {
		return "", contentHash, err
	}
	dstPath := filepath.Join(crashStoreDir, contentHash)
	if err := os.WriteFile(dstPath, data, 0o644); err != nil {
		return "", contentHash, err
	}
	if removeOriginal {
		_ = os.Remove(srcPath)
	}
	return dstPath, contentHash, nil
}

// measureCorpusCoverage replays the cached fuzz corpus entries in an isolated temporary copy of the module
// so that the user's repository is never modified on disk. Sibling directories in the parent workspace are
// symlinked into the temporary workspace root so relative go.mod replace directives (e.g. ../sibling) resolve.
func measureCorpusCoverage(ctx context.Context, moduleDir, pkgArg, exactPattern, targetName, cacheTargetDir string) (float64, bool, error) {
	tmpRoot, err := os.MkdirTemp("", "ntfuzz-cov-*")
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = os.RemoveAll(tmpRoot) }()

	absMod, absErr := filepath.Abs(moduleDir)
	if absErr == nil {
		moduleDir = absMod
	}
	modBase := filepath.Base(moduleDir)
	parentDir := filepath.Dir(moduleDir)

	if siblings, readErr := os.ReadDir(parentDir); readErr == nil {
		for _, sib := range siblings {
			name := sib.Name()
			if name == modBase || strings.HasPrefix(name, ".") {
				continue
			}
			_ = os.Symlink(filepath.Join(parentDir, name), filepath.Join(tmpRoot, name))
		}
	}

	tmpModDir := filepath.Join(tmpRoot, modBase)
	if err := os.MkdirAll(tmpModDir, 0o755); err != nil {
		return 0, false, err
	}
	if err := copyModuleTree(moduleDir, tmpModDir); err != nil {
		return 0, false, err
	}

	tmpPkgDir := tmpModDir
	if pkgArg != "." && pkgArg != "" {
		tmpPkgDir = filepath.Join(tmpModDir, strings.TrimPrefix(pkgArg, "./"))
	}

	cachedFiles := listRegularFiles(cacheTargetDir)
	if len(cachedFiles) > 0 {
		seedDir := filepath.Join(tmpPkgDir, "testdata", "fuzz", targetName)
		if mkErr := os.MkdirAll(seedDir, 0o755); mkErr == nil {
			for _, src := range cachedFiles {
				dst := filepath.Join(seedDir, "ntfuzz_cache_"+filepath.Base(src))
				_ = copyFile(src, dst)
			}
		}
	}

	covCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(covCtx, "go", "test",
		"-parallel=1",
		"-run="+exactPattern,
		"-cover",
		pkgArg,
	)
	cmd.Dir = tmpModDir
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 5 * time.Second

	out, _ := cmd.CombinedOutput()
	if m := reCoverage.FindStringSubmatch(string(out)); m != nil {
		cov, parseErr := strconv.ParseFloat(m[1], 64)
		return cov, parseErr == nil, parseErr
	}
	return 0, false, nil
}

func copyModuleTree(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(srcRoot, path)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "bin" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dstRoot, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, filepath.Join(dstRoot, rel))
	})
}

func listRegularFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()

	_, err = io.Copy(out, in)
	return err
}

func extractCrashStack(output string) string {
	lines := strings.Split(output, "\n")
	var keep []string
	capturing := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- FAIL:") || strings.HasPrefix(trimmed, "panic:") || strings.Contains(trimmed, "Error:") {
			capturing = true
		}
		if capturing {
			if trimmed == "" ||
				strings.HasPrefix(trimmed, "exit status ") ||
				trimmed == "FAIL" ||
				strings.HasPrefix(trimmed, "Failing input written to ") ||
				strings.HasPrefix(trimmed, "To re-run:") ||
				strings.HasPrefix(trimmed, "go test ") {
				continue
			}
			keep = append(keep, line)
			if len(keep) >= 16 {
				break
			}
		}
	}
	if len(keep) == 0 {
		trimmed := strings.TrimSpace(output)
		if len(trimmed) > 500 {
			trimmed = trimmed[len(trimmed)-500:]
			for len(trimmed) > 0 && !utf8.RuneStart(trimmed[0]) {
				trimmed = trimmed[1:]
			}
		}
		return strings.ToValidUTF8(trimmed, "")
	}
	return strings.ToValidUTF8(strings.TrimSpace(strings.Join(keep, "\n")), "")
}

func sanitizePathSegment(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(s)
}
