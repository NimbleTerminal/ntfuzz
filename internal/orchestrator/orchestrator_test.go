package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NimbleTerminal/ntfuzzy/internal/store"
)

func TestParseFuzzOutput(t *testing.T) {
	sampleOut := `fuzz: elapsed: 0s, gathering baseline coverage: 0/4 completed
fuzz: elapsed: 0s, gathering baseline coverage: 4/4 completed, now fuzzing with 8 workers
fuzz: elapsed: 3s, execs: 95400 (31800/sec), new interesting: 6 (total: 10)
fuzz: elapsed: 6s, execs: 192000 (32000/sec), new interesting: 9 (total: 13)
PASS
coverage: 84.6% of statements
ok  	example.com/demo/parser	6.120s
`
	started := time.Now().UTC()
	res := ParseFuzzOutput(sampleOut, started, 6.0, nil)
	if res.Status != "ok" {
		t.Fatalf("expected status ok, got %q", res.Status)
	}
	if res.Execs != 192000 {
		t.Fatalf("expected execs 192000, got %d", res.Execs)
	}
	if res.NewInteresting != 9 {
		t.Fatalf("expected new interesting 9, got %d", res.NewInteresting)
	}
	if res.CoveragePct != 84.6 {
		t.Fatalf("expected coverage 84.6, got %f", res.CoveragePct)
	}
}

func TestParseFuzzOutputWithCrash(t *testing.T) {
	crashOut := `fuzz: elapsed: 0s, gathering baseline coverage: 0/2 completed
fuzz: elapsed: 1s, execs: 1420 (1420/sec), new interesting: 1 (total: 3)
--- FAIL: FuzzDecode (0.42s)
    --- FAIL: FuzzDecode (0.00s)
        codec_test.go:24: unexpected header magic 0xff
    
    Failing input written to testdata/fuzz/FuzzDecode/9f8e7d6c5b4a3210
    To re-run:
    go test -run=FuzzDecode/9f8e7d6c5b4a3210
FAIL
exit status 1
`
	res := ParseFuzzOutput(crashOut, time.Now().UTC(), 1.0, errors.New("exit status 1"))
	if res.Status != "crash" {
		t.Fatalf("expected status crash, got %q", res.Status)
	}
	if res.Crashes != 1 || len(res.CrashRecords) != 1 {
		t.Fatalf("expected 1 crash record, got %+v", res.CrashRecords)
	}
	if res.CrashRecords[0].InputHash != "9f8e7d6c5b4a3210" {
		t.Fatalf("unexpected crash hash %q", res.CrashRecords[0].InputHash)
	}
}

func TestDiscoverTargetsAndOrchestratorResilience(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "samplemod")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/samplemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testSrc := `package samplemod

import "testing"

func FuzzAlpha(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, s string) {})
}

func TestNotAFuzzTarget(t *testing.T) {}
`
	if err := os.WriteFile(filepath.Join(repoDir, "sample_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	targets, err := DiscoverTargets(ctx, repoDir)
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].FuncName != "FuzzAlpha" || targets[0].PackageImport != "example.com/samplemod" {
		t.Fatalf("unexpected discovered targets: %+v", targets)
	}

	// Verify orchestrator continues when one project path is invalid
	st, err := store.Open(filepath.Join(tmp, "orch.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := Config{
		CorpusDir:       filepath.Join(tmp, "corpus"),
		WorkDir:         filepath.Join(tmp, "repos"),
		DefaultFuzzTime: "1s",
		Projects: []ProjectConfig{
			{Name: "broken-repo", Repo: filepath.Join(tmp, "does-not-exist-git-url")},
			{Name: "valid-repo", Repo: repoDir, FuzzTime: "1s"},
		},
	}

	orch := New(cfg, st, Options{
		Workers: 1,
		Runner:  stubRunner{},
	})
	orch.RunOnce(ctx)

	storedTargets, err := st.ListTargets(ctx, "valid-repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(storedTargets) != 1 || storedTargets[0].TotalRuns != 1 {
		t.Fatalf("expected valid-repo target to be fuzzed despite broken-repo failure, got: %+v", storedTargets)
	}
}

type stubRunner struct{}

func (stubRunner) RunTarget(ctx context.Context, job JobSpec) (JobResult, error) {
	return JobResult{
		Started:        time.Now().UTC(),
		DurationSec:    1.0,
		Execs:          10000,
		ExecsPerSec:    10000,
		NewInteresting: 2,
		CoveragePct:    75.0,
		Status:         "ok",
	}, nil
}

func TestLocalRunnerEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real go test -fuzz in short mode")
	}
	ctx := context.Background()
	repoDir, err := filepath.Abs("../../examples/demo-parser")
	if err != nil {
		t.Fatal(err)
	}

	runner := NewLocalRunner()
	res, err := runner.RunTarget(ctx, JobSpec{
		WorkerID:   "local-test",
		Project:    "demo-parser",
		RepoDir:    repoDir,
		PkgImport:  "example.com/demo-parser",
		PkgRelDir:  ".",
		TargetName: "FuzzParseKV",
		FuzzTime:   1 * time.Second,
		CorpusDir:  filepath.Join(t.TempDir(), "corpus"),
	})
	if err != nil {
		t.Fatalf("LocalRunner.RunTarget failed: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("expected status ok, got %q (output:\n%s)", res.Status, res.Output)
	}
	if res.CoveragePct <= 0 {
		t.Fatalf("expected positive coverage percentage, got %f", res.CoveragePct)
	}
}

