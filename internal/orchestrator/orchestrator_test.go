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
fuzz: elapsed: 0s, gathering baseline coverage: 4/4 completed, now fuzzing with 1 workers
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
	if res.CorpusCount != 13 {
		t.Fatalf("expected corpus count 13, got %d", res.CorpusCount)
	}
	if !res.CoverageMeasured || res.CoveragePct != 84.6 {
		t.Fatalf("expected coverage 84.6, got %f (measured=%t)", res.CoveragePct, res.CoverageMeasured)
	}
}

func TestParseFuzzOutputWithCrashAndStackFingerprint(t *testing.T) {
	crashOut1 := `fuzz: elapsed: 0s, gathering baseline coverage: 0/2 completed
fuzz: elapsed: 1s, execs: 1420 (1420/sec), new interesting: 1 (total: 3)
--- FAIL: FuzzDecode (0.42s)
    --- FAIL: FuzzDecode (0.00s)
        /tmp/work/codec_test.go:24: unexpected header magic 0xff
    
    Failing input written to testdata/fuzz/FuzzDecode/9f8e7d6c5b4a3210
    To re-run:
    go test -run=FuzzDecode/9f8e7d6c5b4a3210
FAIL
exit status 1
`
	crashOut2 := `--- FAIL: FuzzDecode (0.42s)
    --- FAIL: FuzzDecode (0.00s)
        /another/path/codec_test.go:99: unexpected header magic 0xff
    
    Failing input written to testdata/fuzz/FuzzDecode/1111111111111111
FAIL
exit status 1
`
	res1 := ParseFuzzOutput(crashOut1, time.Now().UTC(), 1.0, errors.New("exit status 1"))
	res2 := ParseFuzzOutput(crashOut2, time.Now().UTC(), 1.0, errors.New("exit status 1"))
	if res1.Status != "crash" || len(res1.CrashRecords) != 1 {
		t.Fatalf("expected 1 crash record, got %+v", res1)
	}
	// Stack fingerprint should match despite path and line number changes
	if res1.CrashRecords[0].StackFingerprint == "" || res1.CrashRecords[0].StackFingerprint != res2.CrashRecords[0].StackFingerprint {
		t.Fatalf("expected identical stack fingerprints across line number shift, got %q vs %q",
			res1.CrashRecords[0].StackFingerprint, res2.CrashRecords[0].StackFingerprint)
	}
}

func TestDiscoverTargetsBuildTagsMultiModuleAndReconciliation(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "samplemod")
	subModDir := filepath.Join(repoDir, "submod")
	if err := os.MkdirAll(subModDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/samplemod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subModDir, "go.mod"), []byte("module example.com/samplemod/submod\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	testSrc := `package samplemod

import "testing"

func FuzzAlpha(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, s string) {})
}
`
	if err := os.WriteFile(filepath.Join(repoDir, "sample_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// File with an impossible build tag must be ignored by DiscoverTargets
	ignoredSrc := `//go:build ignore_this_fuzz_target

package samplemod

import "testing"

func FuzzIgnoredByBuildTag(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) {})
}
`
	if err := os.WriteFile(filepath.Join(repoDir, "ignored_test.go"), []byte(ignoredSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	subTestSrc := `package submod

import "testing"

func FuzzSubBeta(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {})
}
`
	if err := os.WriteFile(filepath.Join(subModDir, "sub_test.go"), []byte(subTestSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	targets, err := DiscoverTargets(ctx, repoDir)
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 discovered targets (ignoring build-tagged file), got %+v", targets)
	}
	if targets[0].FuncName != "FuzzAlpha" || targets[0].PackageImport != "example.com/samplemod" {
		t.Fatalf("unexpected target[0]: %+v", targets[0])
	}
	if targets[1].FuncName != "FuzzSubBeta" || targets[1].PackageImport != "example.com/samplemod/submod" || targets[1].ModuleDir != subModDir {
		t.Fatalf("unexpected target[1]: %+v", targets[1])
	}

	// Verify orchestrator continues when one project path is invalid and records sync_error
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
		Workers: 2,
		Runner:  stubRunner{},
	})
	orch.RunOnce(ctx)

	projects, err := st.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name == "broken-repo" && (p.SyncError == "" || p.LastSynced != nil) {
			t.Fatalf("expected broken-repo to have SyncError and nil LastSynced, got %+v", p)
		}
	}

	storedTargets, err := st.ListTargets(ctx, "valid-repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(storedTargets) != 2 {
		t.Fatalf("expected 2 targets in valid-repo, got: %+v", storedTargets)
	}
}

type stubRunner struct{}

func (stubRunner) RunTarget(ctx context.Context, job JobSpec) (JobResult, error) {
	return JobResult{
		Started:          time.Now().UTC(),
		DurationSec:      1.0,
		Execs:            10000,
		ExecsPerSec:      10000,
		NewInteresting:   2,
		CorpusCount:      8,
		CoveragePct:      75.0,
		CoverageMeasured: true,
		Status:           "ok",
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
	if !res.CoverageMeasured || res.CoveragePct <= 0 {
		t.Fatalf("expected measured positive coverage percentage, got %f (measured=%t)", res.CoveragePct, res.CoverageMeasured)
	}
}
