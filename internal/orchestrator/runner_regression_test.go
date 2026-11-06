package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestLocalRunnerSubpackage(t *testing.T) {
	for _, rootGo := range []bool{true, false} {
		t.Run(map[bool]string{true: "root-package", false: "empty-root"}[rootGo], func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", "module example.com/fixture\n\ngo 1.25\n")
			if rootGo {
				write("root.go", "package fixture\n")
			}
			write("sub/fuzz_test.go", `package sub
import ("testing"; "os"; "flag"; "path/filepath")
func FuzzSub(f *testing.F) {
 cache := flag.Lookup("test.fuzzcachedir").Value.String()
 // Coverage replay has no fuzz cache. Only the actual fuzz process writes proof.
 if cache != "" { if err := os.WriteFile(filepath.Join(cache,"received"),[]byte(cache),0600); err != nil {f.Fatal(err)} }
 f.Add("seed")
 f.Fuzz(func(t *testing.T, s string) {
 if s == "cached-only" && cache != "" { if err := os.WriteFile(filepath.Join(cache,"consumed"),[]byte(s),0600); err != nil {t.Fatal(err)} }
})
}
`)
			corpus := filepath.Join(t.TempDir(), "corpus")
			job := JobSpec{RepoDir: dir, PkgImport: "example.com/fixture/sub", PkgRelDir: "sub", TargetName: "FuzzSub", FuzzTime: time.Second, CorpusDir: corpus}
			cacheRoot := filepath.Join(corpus, "cache", sanitizePathSegment(job.PkgImport))
			if err := os.MkdirAll(filepath.Join(cacheRoot, job.TargetName), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cacheRoot, job.TargetName, "cachedseed"), []byte("go test fuzz v1\nstring(\"cached-only\")\n"), 0600); err != nil {
				t.Fatal(err)
			}
			res, err := NewLocalRunner().RunTarget(context.Background(), job)
			if err != nil || res.Status != "ok" || res.Execs == 0 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			expected := filepath.Join(corpus, "cache", sanitizePathSegment(job.PkgImport))
			got, err := os.ReadFile(filepath.Join(expected, "received"))
			if err != nil || string(got) != expected {
				t.Fatalf("cache receipt %q err=%v", got, err)
			}
			if got, err := os.ReadFile(filepath.Join(expected, "consumed")); err != nil || string(got) != "cached-only" {
				t.Fatalf("configured cache not consumed: %q %v", got, err)
			}
			t.Logf("executions=%d cache=%s", res.Execs, got)
			job.TargetName = "FuzzAbsent"
			res, err = NewLocalRunner().RunTarget(context.Background(), job)
			if err != nil || res.Status != "missing_target" {
				t.Fatalf("missing result=%+v err=%v", res, err)
			}
		})
	}
}

func TestFuzzOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, out, status string
		err               error
	}{
		{"missing", "testing: warning: no fuzz tests to fuzz\nPASS", "missing_target", nil},
		{"seed-only", "fuzz: elapsed: 1s, gathering baseline coverage: 0/10 completed\nPASS", "incomplete", nil},
		{"zero", "fuzz: elapsed: 1s, execs: 0 (0/sec)\nPASS", "ok", nil},
		{"compile", "# package\nbuild failed", "error", errors.New("exit 1")},
		{"infrastructure", "--- FAIL: FuzzX\nfailed to start worker: resource temporarily unavailable", "error", errors.New("exit 1")},
		{"seed-crash", "--- FAIL: FuzzX (0.0s)\n bad seed", "crash", errors.New("exit 1")},
		{"cancel", "--- FAIL: FuzzX\npanic: interrupted", "canceled", context.Canceled},
		{"timeout", "--- FAIL: FuzzX", "error", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseFuzzOutput(tc.out, time.Now(), 1, tc.err)
			if r.Status != tc.status {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestBoundedDiagnostic(t *testing.T) {
	var b boundedOutput
	_, _ = b.Write([]byte("first\n" + strings.Repeat("x", 100000) + "\nlast"))
	if len(b.String()) > 16500 || !strings.Contains(b.String(), "first") || !strings.Contains(b.String(), "last") {
		t.Fatal("unbounded or missing context")
	}
	d := sanitizeDiagnostic("\x1b password=abc https://user:pass@host/path /private/repo/file", "/private/repo")
	if strings.Contains(d, "abc") || strings.Contains(d, "user:pass") || strings.Contains(d, "/private/repo") || strings.Contains(d, "\x1b") {
		t.Fatal(d)
	}
}

// Optional real-project campaigns use disposable copies; no historical DB or corpus is opened.
func TestLocalRunnerRepresentativeProjects(t *testing.T) {
	root := os.Getenv("NTFUZZ_PROJECTS")
	if root == "" {
		t.Skip("set NTFUZZ_PROJECTS to the directory containing gloss, nthtml, nttypeset")
	}
	for _, tc := range []struct{ repo, pkg, target string }{
		{"gloss", "internal/document", "FuzzReadText"},
		{"nthtml", "document", "FuzzRender"},
		{"nttypeset", ".", "FuzzWorkerMetadata"},
	} {
		t.Run(tc.repo, func(t *testing.T) {
			workspace := t.TempDir()
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.IsDir() && e.Name() != tc.repo {
					if err := os.Symlink(filepath.Join(root, e.Name()), filepath.Join(workspace, e.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			dir := filepath.Join(workspace, tc.repo)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := copyModuleTree(filepath.Join(root, tc.repo), dir); err != nil {
				t.Fatal(err)
			}
			res, err := NewLocalRunner().RunTarget(context.Background(), JobSpec{RepoDir: dir, PkgImport: "nimbleterminal.dev/" + tc.repo + "/" + tc.pkg, PkgRelDir: tc.pkg, TargetName: tc.target, FuzzTime: 3 * time.Second, CorpusDir: filepath.Join(t.TempDir(), "corpus")})
			t.Logf("target=%s status=%s executions=%d duration=%.2fs diagnostic=%s", tc.target, res.Status, res.Execs, res.DurationSec, res.Output)
			if err != nil || res.Status != "ok" || res.Execs == 0 {
				t.Fatalf("campaign failed: %v", err)
			}
		})
	}
}

func TestBoundedOutputPreservesSmallOutputVerbatim(t *testing.T) {
	var b boundedOutput
	in := strings.Repeat("x", 8190) + "\n--- FAIL: FuzzX (0.1s)\nPASS\n"
	_, _ = b.Write([]byte(in))
	if got := b.String(); got != in {
		t.Fatalf("expected verbatim output when nothing was dropped, got %q", got)
	}
}

func TestBoundedOutputCutsOnLineBoundaries(t *testing.T) {
	var b boundedOutput
	var sb strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&sb, "line%05d\n", i)
	}
	_, _ = b.Write([]byte(sb.String()))
	out := b.String()
	if !strings.Contains(out, "[diagnostic tail") {
		t.Fatal("expected omission marker when bytes were dropped")
	}
	full := regexp.MustCompile(`^line\d{5}$`)
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(line, "[diagnostic tail") {
			continue
		}
		if !full.MatchString(line) {
			t.Fatalf("partial line in bounded output: %q", line)
		}
	}
}

func TestInfraPhrasesInTargetOutputStillCrash(t *testing.T) {
	out := "fuzz: elapsed: 1s, execs: 10 (10/sec)\n" +
		"--- FAIL: FuzzDecode (0.10s)\n" +
		"    --- FAIL: FuzzDecode (0.00s)\n" +
		"        panic: failed to unmarshal header: index out of range\n" +
		"FAIL\n"
	r := ParseFuzzOutput(out, time.Now(), 1, errors.New("exit 1"))
	if r.Status != "crash" || len(r.CrashRecords) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestMissingTargetPhraseInTargetOutputStillCrash(t *testing.T) {
	out := "fuzz: elapsed: 1s, execs: 10 (10/sec)\n" +
		"--- FAIL: FuzzRunner (1.00s)\n" +
		"    runner_test.go:9: no tests to run\n" +
		"    Failing input written to testdata/fuzz/FuzzRunner/abc\n" +
		"FAIL\n"
	r := ParseFuzzOutput(out, time.Now(), 1, errors.New("exit 1"))
	if r.Status != "crash" || len(r.CrashRecords) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestDeadlineKeepsWrittenCrash(t *testing.T) {
	out := "fuzz: elapsed: 3s, execs: 10 (3/sec)\n" +
		"--- FAIL: FuzzX (3.00s)\n" +
		"    Failing input written to testdata/fuzz/FuzzX/abc\n"
	r := ParseFuzzOutput(out, time.Now(), 3, context.DeadlineExceeded)
	if r.Status != "crash" || len(r.CrashRecords) != 1 || r.CrashRecords[0].MinimizedInput != "testdata/fuzz/FuzzX/abc" {
		t.Fatalf("%+v", r)
	}
}
