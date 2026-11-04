package orchestrator

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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
// the persistent corpus tree (hashing the input bytes), and measures corpus coverage by replaying
// the cached corpus entries.
func (r *LocalRunner) RunTarget(ctx context.Context, job JobSpec) (JobResult, error) {
	started := time.Now().UTC()
	fuzzTime := job.FuzzTime
	if fuzzTime <= 0 {
		fuzzTime = 10 * time.Second
	}

	absCorpusRoot, err := filepath.Abs(job.CorpusDir)
	if err != nil {
		absCorpusRoot = job.CorpusDir
	}
	targetCacheRoot := filepath.Join(absCorpusRoot, "cache", sanitizePathSegment(job.PkgImport))
	if err := os.MkdirAll(targetCacheRoot, 0o755); err != nil {
		return JobResult{Started: started, Status: "error"}, fmt.Errorf("mkdir corpus cache %q: %w", targetCacheRoot, err)
	}

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
	// Cap child fuzzer concurrency to 1 worker process per orchestrator worker slot
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")

	outBytes, runErr := cmd.CombinedOutput()
	duration := time.Since(started).Seconds()
	output := string(outBytes)

	res := ParseFuzzOutput(output, started, duration, runErr)

	// Persist crash artifacts into the corpus tree and hash their actual bytes
	pkgDir := job.RepoDir
	if pkgArg != "." {
		pkgDir = filepath.Join(job.RepoDir, strings.TrimPrefix(pkgArg, "./"))
	}
	crashStoreDir := filepath.Join(absCorpusRoot, "crashes", sanitizePathSegment(job.PkgImport), job.TargetName)
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

	// Measure statement coverage across f.Add seeds + testdata/fuzz + cached corpus entries
	if !res.CoverageMeasured {
		if cov, measured, covErr := measureCorpusCoverage(ctx, job.RepoDir, pkgDir, pkgArg, exactPattern, job.TargetName, cacheTargetDir); covErr == nil && measured {
			res.CoveragePct = cov
			res.CoverageMeasured = true
		}
	}

	return res, nil
}

// ParseFuzzOutput extracts execution metrics, coverage, status, stack fingerprints, and crash artifacts.
func ParseFuzzOutput(output string, started time.Time, durationSec float64, runErr error) JobResult {
	if durationSec <= 0 {
		durationSec = 0.001
	}
	res := JobResult{
		Started:     started,
		DurationSec: durationSec,
		Status:      "ok",
		Output:      output,
	}

	var lastRate float64
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if m := reFuzzProgress.FindStringSubmatch(line); m != nil {
			if execs, err := strconv.ParseInt(m[1], 10, 64); err == nil && execs >= res.Execs {
				res.Execs = execs
			}
			if rate, err := strconv.ParseFloat(m[2], 64); err == nil && rate > 0 {
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
			if cov, err := strconv.ParseFloat(m[1], 64); err == nil {
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
		res.ExecsPerSec = float64(res.Execs) / durationSec
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

// measureCorpusCoverage replays the cached fuzz corpus entries by temporarily staging them
// into testdata/fuzz/<TargetName> (cleaning up staged files in defer) and running `go test -cover`.
func measureCorpusCoverage(ctx context.Context, moduleDir, pkgDir, pkgArg, exactPattern, targetName, cacheTargetDir string) (float64, bool, error) {
	seedDir := filepath.Join(pkgDir, "testdata", "fuzz", targetName)
	hadTestdataDir := dirExists(filepath.Join(pkgDir, "testdata"))
	hadFuzzDir := dirExists(filepath.Join(pkgDir, "testdata", "fuzz"))
	hadSeedDir := dirExists(seedDir)

	var stagedFiles []string
	cachedFiles := listRegularFiles(cacheTargetDir)
	if len(cachedFiles) > 0 {
		if err := os.MkdirAll(seedDir, 0o755); err == nil {
			for _, src := range cachedFiles {
				base := "ntfuzz_cache_" + filepath.Base(src)
				dst := filepath.Join(seedDir, base)
				if _, existsErr := os.Stat(dst); os.IsNotExist(existsErr) {
					if copyFile(src, dst) == nil {
						stagedFiles = append(stagedFiles, dst)
					}
				}
			}
		}
	}
	defer func() {
		for _, f := range stagedFiles {
			_ = os.Remove(f)
		}
		if !hadSeedDir {
			_ = os.Remove(seedDir)
		}
		if !hadFuzzDir {
			_ = os.Remove(filepath.Join(pkgDir, "testdata", "fuzz"))
		}
		if !hadTestdataDir {
			_ = os.Remove(filepath.Join(pkgDir, "testdata"))
		}
	}()

	covCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(covCtx, "go", "test",
		"-parallel=1",
		"-run="+exactPattern,
		"-cover",
		pkgArg,
	)
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	out, _ := cmd.CombinedOutput()
	if m := reCoverage.FindStringSubmatch(string(out)); m != nil {
		cov, err := strconv.ParseFloat(m[1], 64)
		return cov, err == nil, err
	}
	return 0, false, nil
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

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
		}
		return trimmed
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

func sanitizePathSegment(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(s)
}
