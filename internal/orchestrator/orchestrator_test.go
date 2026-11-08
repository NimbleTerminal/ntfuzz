package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/store"
)

func TestParseConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid config",
			yaml: `
default_fuzztime: 10s
projects:
  - name: p1
    repo: ./examples/demo-parser
    fuzztime: 5s
`,
			wantErr: "",
		},
		{
			name: "invalid project fuzztime missing unit",
			yaml: `
projects:
  - name: p1
    repo: ./examples/demo-parser
    fuzztime: "30"
`,
			wantErr: "invalid fuzztime",
		},
		{
			name: "invalid default_fuzztime",
			yaml: `
default_fuzztime: "-5s"
projects:
  - name: p1
    repo: ./examples/demo-parser
`,
			wantErr: "invalid default_fuzztime",
		},
		{
			name: "missing project name",
			yaml: `
projects:
  - repo: ./examples/demo-parser
`,
			wantErr: "missing name",
		},
		{
			name: "missing project repo",
			yaml: `
projects:
  - name: p1
`,
			wantErr: "missing repo",
		},
		{
			name: "repo with https user and token rejected",
			yaml: `
projects:
  - name: secret-proj
    repo: https://user:ghp_secret123@github.com/org/repo.git
`,
			wantErr: "project \"secret-proj\" repo URL must not contain embedded credentials; see docs/deployment-cookbook.md 'Private repositories'",
		},
		{
			name: "repo with https token-only userinfo rejected",
			yaml: `
projects:
  - name: secret-proj
    repo: https://ghp_secret123@github.com/org/repo.git
`,
			wantErr: "project \"secret-proj\" repo URL must not contain embedded credentials; see docs/deployment-cookbook.md 'Private repositories'",
		},
		{
			name: "repo with ssh git@host:org/repo accepted",
			yaml: `
projects:
  - name: ssh-proj
    repo: git@github.com:org/repo
`,
			wantErr: "",
		},
		{
			name: "repo with plain https accepted",
			yaml: `
projects:
  - name: https-proj
    repo: https://github.com/org/repo.git
`,
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.yaml))
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
			}
		})
	}

	cfg, err := LoadConfig("../../configs/projects.yaml")
	if err != nil {
		t.Fatalf("LoadConfig(../../configs/projects.yaml) failed: %v", err)
	}
	if len(cfg.Projects) == 0 {
		t.Fatalf("LoadConfig(../../configs/projects.yaml) returned 0 projects")
	}
}

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
FAIL	example.com/demo/codec	1.425s
`
	crashOut2 := `--- FAIL: FuzzDecode (0.91s)
    --- FAIL: FuzzDecode (0.01s)
        /another/path/codec_test.go:99: unexpected header magic 0xff
    
    Failing input written to testdata/fuzz/FuzzDecode/1111111111111111
FAIL
exit status 1
FAIL	example.com/demo/codec	9.707s
[diagnostic tail]
`
	res1 := ParseFuzzOutput(crashOut1, time.Now().UTC(), 1.0, errors.New("exit status 1"))
	res2 := ParseFuzzOutput(crashOut2, time.Now().UTC(), 1.0, errors.New("exit status 1"))
	if res1.Status != "crash" || len(res1.CrashRecords) != 1 {
		t.Fatalf("expected 1 crash record, got %+v", res1)
	}
	if strings.Contains(res1.CrashRecords[0].Stack, "Failing input written to") ||
		strings.Contains(res1.CrashRecords[0].Stack, "FAIL\t") ||
		strings.Contains(res2.CrashRecords[0].Stack, "[diagnostic tail") {
		t.Fatalf("expected extractCrashStack to strip footer, got:\n%s\nand:\n%s",
			res1.CrashRecords[0].Stack, res2.CrashRecords[0].Stack)
	}
	// Stack fingerprint should match despite path, duration, and line number changes
	if res1.CrashRecords[0].StackFingerprint == "" || res1.CrashRecords[0].StackFingerprint != res2.CrashRecords[0].StackFingerprint {
		t.Fatalf("expected identical stack fingerprints across line number shift, got %q vs %q",
			res1.CrashRecords[0].StackFingerprint, res2.CrashRecords[0].StackFingerprint)
	}
}

func TestMigrateLegacyCorpus(t *testing.T) {
	tmp := t.TempDir()
	pkgSeg := "example.com_demo-parser"
	targetName := "FuzzParseKV"

	legacyDir := filepath.Join(tmp, pkgSeg, targetName, targetName)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacyDir, "seed123")
	if err := os.WriteFile(legacyFile, []byte("go test fuzz v1\nstring(\"a=b\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	targetCacheRoot := filepath.Join(tmp, "cache", pkgSeg)
	if err := MigrateLegacyCorpus(tmp, pkgSeg, targetName, targetCacheRoot); err != nil {
		t.Fatalf("MigrateLegacyCorpus: %v", err)
	}

	migratedFile := filepath.Join(targetCacheRoot, targetName, "seed123")
	if _, err := os.Stat(migratedFile); err != nil {
		t.Fatalf("expected migrated file at %s: %v", migratedFile, err)
	}
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Fatalf("expected legacy file to be removed after migration")
	}
}

func TestSchedulerNoModuleStarvationAndBackoff(t *testing.T) {
	// Module A has 2 targets at the front of the queue; Module B has 1 target after Module A.
	// With 2 workers, Worker 1 takes Module A target 1, and Worker 2 must immediately skip
	// Module A target 2 and run Module B target 1 concurrently!
	items := []scheduledTarget{
		{
			project:  ProjectConfig{Name: "modA"},
			checkout: CheckoutInfo{RepoDir: "/tmp/modA"},
			target:   DiscoveredTarget{ModuleDir: "/tmp/modA", FuncName: "FuzzA1"},
			targetID: 1,
		},
		{
			project:  ProjectConfig{Name: "modA"},
			checkout: CheckoutInfo{RepoDir: "/tmp/modA"},
			target:   DiscoveredTarget{ModuleDir: "/tmp/modA", FuncName: "FuzzA2"},
			targetID: 2,
		},
		{
			project:  ProjectConfig{Name: "modB"},
			checkout: CheckoutInfo{RepoDir: "/tmp/modB"},
			target:   DiscoveredTarget{ModuleDir: "/tmp/modB", FuncName: "FuzzB1"},
			targetID: 3,
		},
	}

	q := newModuleQueue(items)
	ctx := context.Background()

	first, ok := q.acquire(ctx)
	if !ok || first.target.FuncName != "FuzzA1" {
		t.Fatalf("expected Worker 1 to acquire FuzzA1, got %+v", first)
	}

	// While modA is still in-flight, Worker 2 acquires next -> must get FuzzB1 without blocking!
	second, ok := q.acquire(ctx)
	if !ok || second.target.FuncName != "FuzzB1" {
		t.Fatalf("expected Worker 2 to skip locked modA and acquire FuzzB1, got %+v", second)
	}

	q.release(first.effectiveModuleDir())
	q.release(second.effectiveModuleDir())

	third, ok := q.acquire(ctx)
	if !ok || third.target.FuncName != "FuzzA2" {
		t.Fatalf("expected remaining item FuzzA2 after modA release, got %+v", third)
	}
	q.release(third.effectiveModuleDir())

	// Also verify backoff after 3 consecutive flat runs
	orch := New(Config{}, nil, Options{Workers: 2})
	for i := 0; i < 3; i++ {
		orch.recordTargetActivity(1, 0, 0) // target 1 has 3 flat runs (streak=3 -> maxSkip=1)
	}
	orch.recordTargetActivity(3, 2, 0) // target 3 found 2 new inputs (streak=0)

	due := orch.selectDueTargets([]scheduledTarget{
		{targetID: 1, priority: orch.targetPriority(1)},
		{targetID: 3, priority: orch.targetPriority(3)},
	})
	if len(due) != 1 || due[0].targetID != 3 {
		t.Fatalf("expected flat target 1 to back off for 1 round while target 3 runs, got %+v", due)
	}
	// On the next round, target 1 is due again
	dueNext := orch.selectDueTargets([]scheduledTarget{
		{targetID: 1, priority: orch.targetPriority(1)},
		{targetID: 3, priority: orch.targetPriority(3)},
	})
	if len(dueNext) != 2 {
		t.Fatalf("expected both targets due on next round, got %+v", dueNext)
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
	defer func() { _ = st.Close() }()

	cfg := Config{
		CorpusDir:       filepath.Join(tmp, "corpus"),
		WorkDir:         filepath.Join(tmp, "repos"),
		DefaultFuzzTime: "1s",
		Projects: []ProjectConfig{
			{Name: "broken-repo", Repo: filepath.Join(tmp, "does-not-exist-git-url")},
			{Name: "valid-repo", Repo: repoDir, FuzzTime: "1s"},
		},
	}

	var concurrent int32
	var maxConcurrent int32
	orch := New(cfg, st, Options{
		Workers: 2,
		Runner: &concurrencyTrackingRunner{
			concurrent:    &concurrent,
			maxConcurrent: &maxConcurrent,
		},
	})
	orch.RunOnce(ctx)

	if atomic.LoadInt32(&maxConcurrent) < 2 {
		t.Fatalf("expected 2 workers to fuzz samplemod and samplemod/submod in parallel, maxConcurrent=%d", maxConcurrent)
	}

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

type concurrencyTrackingRunner struct {
	concurrent    *int32
	maxConcurrent *int32
}

func (r *concurrencyTrackingRunner) RunTarget(ctx context.Context, job JobSpec) (JobResult, error) {
	cur := atomic.AddInt32(r.concurrent, 1)
	for {
		prev := atomic.LoadInt32(r.maxConcurrent)
		if cur <= prev || atomic.CompareAndSwapInt32(r.maxConcurrent, prev, cur) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	atomic.AddInt32(r.concurrent, -1)

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
	if testing.Short() || os.Getenv("NTFUZZ_E2E") == "" {
		t.Skip("skipping live go test -fuzz subprocess test (set NTFUZZ_E2E=1 to run)")
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

func TestDiscoverSelfTargetsSkipsExamples(t *testing.T) {
	ctx := context.Background()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	targets, err := DiscoverTargets(ctx, repoRoot)
	if err != nil {
		t.Fatalf("DiscoverTargets(repoRoot): %v", err)
	}
	if len(targets) == 0 {
		t.Fatalf("expected ntfuzz self-fuzzing targets to be discovered in %s", repoRoot)
	}
	for _, tgt := range targets {
		if !strings.HasPrefix(tgt.PackageImport, "nimbleterminal.dev/ntfuzz/") {
			t.Fatalf("expected all discovered self-fuzz targets under nimbleterminal.dev/ntfuzz/*, got %+v", tgt)
		}
	}

	// Pointing directly at an example module directory must still discover its targets.
	exampleTargets, err := DiscoverTargets(ctx, filepath.Join(repoRoot, "examples", "demo-parser"))
	if err != nil {
		t.Fatalf("DiscoverTargets(demo-parser): %v", err)
	}
	if len(exampleTargets) != 2 {
		t.Fatalf("expected 2 targets in examples/demo-parser when targeted directly, got %+v", exampleTargets)
	}
}

func TestFingerprintStackNormalizesEmbeddedDurations(t *testing.T) {
	// Regression cases found by FuzzFingerprintStack on the fleet. A fuzz target that
	// writes partial stdout without a newline leaves go test's package FAIL line with a
	// prefix, so the duration token is not at line start. When that prefix ends in a
	// path/hex fragment, the file-line pass must not consume part of the FAIL token
	// before the duration pass sees it.
	cases := []struct{ name, a, b string }{
		{"interleaved prefix", "0FAIL\texample.com/pkg\t0.42s", "0FAIL\texample.com/pkg\t9.99s"},
		{"hex-adjacent prefix", "0.go:0+0xFAIL\texample.com/pkg\t0.42s", "0.go:0+0xFAIL\texample.com/pkg\t9.99s"},
		{"line-start package fail", "FAIL\texample.com/pkg\t0.42s", "FAIL\texample.com/pkg\t9.99s"},
	}
	for _, tc := range cases {
		if fa, fb := FingerprintStack(tc.a), FingerprintStack(tc.b); fa != fb {
			t.Errorf("%s: fingerprint changed with duration: %q vs %q", tc.name, fa, fb)
		}
	}
}

func TestPrivateRepoSyncRedactionEnvAndAuthHint(t *testing.T) {
	// 1. Redaction of sync error containing https://user:ghp_abc@github.com/... and Authorization: token ...
	rawErr := "git clone https://user:ghp_abc@github.com/org/repo.git: fatal: Authentication failed; header Authorization: token ghp_xyz987"
	redacted := SanitizeDiagnostic(rawErr)
	if strings.Contains(redacted, "ghp_abc") || strings.Contains(redacted, "user:ghp_abc") || strings.Contains(redacted, "ghp_xyz987") {
		t.Fatalf("expected credentials to be redacted from sync error, got %q", redacted)
	}
	if !strings.Contains(redacted, "https://<redacted>@github.com/org/repo.git") || !strings.Contains(redacted, "Authorization: token <redacted>") {
		t.Fatalf("unexpected redacted format: %q", redacted)
	}

	// Defense-in-depth Repo URL redaction
	if got := RedactRepoURL("https://user:ghp_abc@github.com/org/repo.git"); got != "https://<redacted>@github.com/org/repo.git" {
		t.Fatalf("expected redacted repo URL, got %q", got)
	}
	if got := RedactRepoURL("git@github.com:org/repo.git"); got != "git@github.com:org/repo.git" {
		t.Fatalf("expected ssh repo URL untouched, got %q", got)
	}

	// 2. Environment variables in runGit / buildGitEnv
	t.Run("env defaults and overrides", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "")
		t.Setenv("NTFUZZ_GIT_SSH_COMMAND", "")
		envDefault := gitEnv()
		var gotPrompt, gotSSH string
		for _, kv := range envDefault {
			if strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
				gotPrompt = strings.TrimPrefix(kv, "GIT_TERMINAL_PROMPT=")
			}
			if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				gotSSH = strings.TrimPrefix(kv, "GIT_SSH_COMMAND=")
			}
		}
		if gotPrompt != "0" {
			t.Fatalf("expected GIT_TERMINAL_PROMPT=0, got %q", gotPrompt)
		}
		if gotSSH != "ssh -o BatchMode=yes" {
			t.Fatalf("expected default GIT_SSH_COMMAND='ssh -o BatchMode=yes', got %q", gotSSH)
		}

		// Respect existing GIT_SSH_COMMAND when NTFUZZ_GIT_SSH_COMMAND is unset
		t.Setenv("GIT_SSH_COMMAND", "ssh -i /custom/id_ed25519 -o BatchMode=yes")
		for _, kv := range gitEnv() {
			if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				gotSSH = strings.TrimPrefix(kv, "GIT_SSH_COMMAND=")
			}
		}
		if gotSSH != "ssh -i /custom/id_ed25519 -o BatchMode=yes" {
			t.Fatalf("expected existing GIT_SSH_COMMAND preserved, got %q", gotSSH)
		}

		// NTFUZZ_GIT_SSH_COMMAND overrides GIT_SSH_COMMAND; verify through actual runGit execution
		t.Setenv("NTFUZZ_GIT_SSH_COMMAND", "ssh -F /etc/ntfuzz/ssh_config -o BatchMode=yes")
		ctx := context.Background()
		err := runGit(ctx, t.TempDir(), "-c", "alias.printenv=!sh -c 'printf \"PROMPT=%s SSH=%s\" \"$GIT_TERMINAL_PROMPT\" \"$GIT_SSH_COMMAND\"; exit 1'", "printenv")
		if err == nil {
			t.Fatalf("expected failing git alias to return error with env output")
		}
		if !strings.Contains(err.Error(), "PROMPT=0 SSH=ssh -F /etc/ntfuzz/ssh_config -o BatchMode=yes") {
			t.Fatalf("expected runGit subprocess to receive overridden env, got: %v", err)
		}
	})

	// 3. Failure message hint on the four auth stderr patterns vs non-auth failure
	t.Run("auth failure hint", func(t *testing.T) {
		authPatterns := []string{
			"fatal: Authentication failed for 'https://github.com/org/private.git/'",
			"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
			"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			"remote: Repository not found.\nfatal: repository 'https://github.com/org/private.git/' not found",
		}
		for _, stderr := range authPatterns {
			err := formatGitCommandError("", []string{"clone", "--depth", "1", "https://github.com/org/private.git", "/tmp/dest"}, errors.New("exit status 128"), []byte(stderr))
			if !strings.Contains(err.Error(), privateRepoAuthHint) {
				t.Fatalf("expected auth hint for stderr %q, got: %v", stderr, err)
			}
		}

		nonAuthErr := formatGitCommandError("", []string{"clone", "--depth", "1", "https://github.com/org/public.git", "/tmp/dest"}, errors.New("exit status 128"), []byte("fatal: unable to access 'https://github.com/org/public.git/': Could not resolve host: github.com"))
		if strings.Contains(nonAuthErr.Error(), privateRepoAuthHint) {
			t.Fatalf("did not expect auth hint on non-auth failure, got: %v", nonAuthErr)
		}
	})
}
