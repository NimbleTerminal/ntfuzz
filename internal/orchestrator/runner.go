package orchestrator

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	WorkerID   string
	Project    string
	RepoDir    string
	PkgImport  string
	PkgRelDir  string
	TargetName string
	FuzzTime   time.Duration
	CorpusDir  string
}

// CrashArtifact holds details of a crash discovered during a fuzz run.
type CrashArtifact struct {
	InputHash      string
	MinimizedInput string
	Stack          string
}

// JobResult contains parsed metrics and crash details from a completed fuzz run.
type JobResult struct {
	Started        time.Time
	DurationSec    float64
	Execs          int64
	ExecsPerSec    float64
	NewInteresting int
	Crashes        int
	CoveragePct    float64
	Status         string
	Output         string
	CrashRecords   []CrashArtifact
}

// WorkerRunner abstracts fuzz job execution so remote workers can be added alongside LocalRunner.
type WorkerRunner interface {
	RunTarget(ctx context.Context, job JobSpec) (JobResult, error)
}

// LocalRunner executes `go test -fuzz=...` in local subprocesses.
type LocalRunner struct{}

// NewLocalRunner creates a new LocalRunner.
func NewLocalRunner() *LocalRunner {
	return &LocalRunner{}
}

var (
	// Matches: fuzz: elapsed: 3s, execs: 145203 (48392/sec), new interesting: 4 (total: 16)
	reFuzzProgress = regexp.MustCompile(`execs:\s*(\d+)\s*\((\d+(?:\.\d+)?)/sec\)(?:,\s*new interesting:\s*(\d+))?`)
	// Matches: Failing input written to testdata/fuzz/FuzzFoo/1234abcd...
	reFailingInput = regexp.MustCompile(`Failing input written to\s+(\S+)`)
	// Matches: coverage: 76.5% of statements
	reCoverage = regexp.MustCompile(`coverage:\s*(\d+(?:\.\d+)?)%\s*of statements`)
)

// RunTarget executes `go test -fuzz` for the specified target and parses the output.
func (r *LocalRunner) RunTarget(ctx context.Context, job JobSpec) (JobResult, error) {
	started := time.Now().UTC()
	fuzzTime := job.FuzzTime
	if fuzzTime <= 0 {
		fuzzTime = 10 * time.Second
	}

	targetCorpusDir := filepath.Join(job.CorpusDir, sanitizePathSegment(job.PkgImport), job.TargetName)
	if absCorpus, err := filepath.Abs(targetCorpusDir); err == nil {
		targetCorpusDir = absCorpus
	}
	if err := os.MkdirAll(targetCorpusDir, 0o755); err != nil {
		return JobResult{Started: started, Status: "error"}, fmt.Errorf("mkdir corpus %q: %w", targetCorpusDir, err)
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
		"-run=" + exactPattern,
		"-fuzz=" + exactPattern,
		"-fuzztime=" + fuzzTime.String(),
		"-test.fuzzcachedir=" + targetCorpusDir,
		pkgArg,
	}

	runCtx, cancel := context.WithTimeout(ctx, fuzzTime+45*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "go", args...)
	cmd.Dir = job.RepoDir
	outBytes, runErr := cmd.CombinedOutput()
	duration := time.Since(started).Seconds()
	output := string(outBytes)

	res := ParseFuzzOutput(output, started, duration, runErr)

	// Measure statement coverage across seed + cached corpus if not already present in output
	if res.CoveragePct == 0 {
		if cov, err := measureCoverage(ctx, job.RepoDir, pkgArg, exactPattern, targetCorpusDir); err == nil && cov > 0 {
			res.CoveragePct = cov
		}
	}

	return res, nil
}

// ParseFuzzOutput extracts execution metrics, coverage, status, and crash artifacts from `go test -fuzz` output.
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
		}
		if m := reCoverage.FindStringSubmatch(line); m != nil {
			if cov, err := strconv.ParseFloat(m[1], 64); err == nil {
				res.CoveragePct = cov
			}
		}
		if m := reFailingInput.FindStringSubmatch(line); m != nil {
			artifactPath := strings.TrimSpace(m[1])
			hash := filepath.Base(artifactPath)
			res.CrashRecords = append(res.CrashRecords, CrashArtifact{
				InputHash:      hash,
				MinimizedInput: artifactPath,
				Stack:          extractCrashStack(output),
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
			sum := sha256.Sum256([]byte(stack))
			hash := hex.EncodeToString(sum[:8])
			res.CrashRecords = append(res.CrashRecords, CrashArtifact{
				InputHash:      hash,
				MinimizedInput: "inline-failure",
				Stack:          stack,
			})
			res.Crashes = 1
			res.Status = "crash"
		} else {
			res.Status = "error"
		}
	}

	return res
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
			if strings.HasPrefix(trimmed, "exit status ") || trimmed == "FAIL" {
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

func measureCoverage(ctx context.Context, repoDir, pkgArg, exactPattern, corpusDir string) (float64, error) {
	covCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(covCtx, "go", "test",
		"-run="+exactPattern,
		"-cover",
		"-test.fuzzcachedir="+corpusDir,
		pkgArg,
	)
	cmd.Dir = repoDir
	out, _ := cmd.CombinedOutput()
	if m := reCoverage.FindStringSubmatch(string(out)); m != nil {
		return strconv.ParseFloat(m[1], 64)
	}
	return 0, nil
}

func sanitizePathSegment(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(s)
}
