package orchestrator

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"nimbleterminal.dev/ntfuzz/configs"
)

func FuzzParseConfig(f *testing.F) {
	f.Add(configs.DefaultProjectsYAML)
	f.Add([]byte(`
corpus_dir: .ntfuzz/corpus
work_dir: .ntfuzz/repos
default_fuzztime: 15s
projects:
  - name: ntfuzz
    repo: https://github.com/NimbleTerminal/ntfuzz.git
    branch: main
    fuzztime: 10s
`))
	f.Add([]byte(`
default_fuzztime: "-5s"
projects:
  - name: "../escape"
    repo: ./examples/demo-parser
`))
	f.Add([]byte(`projects: []`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}
		cfg, err := ParseConfig(data)
		if err != nil {
			return
		}

		if strings.TrimSpace(cfg.CorpusDir) == "" {
			t.Fatalf("ParseConfig returned empty CorpusDir")
		}
		if strings.TrimSpace(cfg.WorkDir) == "" {
			t.Fatalf("ParseConfig returned empty WorkDir")
		}
		d, err := time.ParseDuration(cfg.DefaultFuzzTime)
		if err != nil || d <= 0 {
			t.Fatalf("ParseConfig returned invalid DefaultFuzzTime %q: %v", cfg.DefaultFuzzTime, err)
		}

		for i, p := range cfg.Projects {
			if p.Name == "" || p.Name != strings.TrimSpace(p.Name) {
				t.Fatalf("project[%d] has untrimmed or empty Name %q", i, p.Name)
			}
			if p.Name == "." || p.Name == ".." || strings.Contains(p.Name, "..") || strings.ContainsAny(p.Name, `/\`) {
				t.Fatalf("project[%d] allowed path traversal Name %q", i, p.Name)
			}
			for _, r := range p.Name {
				if unicode.IsControl(r) {
					t.Fatalf("project[%d] allowed control character in Name %q", i, p.Name)
				}
			}
			if p.Repo == "" || p.Repo != strings.TrimSpace(p.Repo) {
				t.Fatalf("project[%d] has untrimmed or empty Repo %q", i, p.Repo)
			}
			if eff := p.EffectiveFuzzTime(cfg.DefaultFuzzTime); eff <= 0 {
				t.Fatalf("project[%d] EffectiveFuzzTime returned %v", i, eff)
			}
			if cdir := p.EffectiveCorpusDir(cfg.CorpusDir); strings.TrimSpace(cdir) == "" {
				t.Fatalf("project[%d] EffectiveCorpusDir returned empty path", i)
			}
		}
	})
}

func FuzzParseFuzzOutput(f *testing.F) {
	f.Add(
		"fuzz: elapsed: 3s, execs: 95400 (31800/sec), new interesting: 6 (total: 10)\nPASS\ncoverage: 84.6% of statements\n",
		6.0,
		false,
	)
	f.Add(
		"--- FAIL: FuzzDecode (0.42s)\n    /tmp/work/codec_test.go:24: unexpected header\n    Failing input written to testdata/fuzz/FuzzDecode/9f8e7d6c5b4a3210\nFAIL\nexit status 1\n",
		1.2,
		true,
	)
	f.Add(
		"panic: runtime error: index out of range [3] with length 3\ngoroutine 19 [running]:\nexample.com/pkg.FuzzFoo(0x14000112233)\n\t/work/foo_test.go:19 +0x8c\n",
		0.0,
		true,
	)
	f.Add("", -1.0, false)

	started := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, output string, durationSec float64, failed bool) {
		if len(output) > 256*1024 {
			return
		}
		var runErr error
		if failed {
			runErr = errors.New("exit status 1")
		}

		res := ParseFuzzOutput(output, started, durationSec, runErr)

		switch res.Status {
		case "ok", "crash", "error", "missing_target", "incomplete", "canceled":
		default:
			t.Fatalf("unexpected status %q", res.Status)
		}

		if math.IsNaN(res.DurationSec) || math.IsInf(res.DurationSec, 0) || res.DurationSec <= 0 {
			t.Fatalf("invalid DurationSec: %v", res.DurationSec)
		}
		if math.IsNaN(res.ExecsPerSec) || math.IsInf(res.ExecsPerSec, 0) || res.ExecsPerSec < 0 {
			t.Fatalf("invalid ExecsPerSec: %v", res.ExecsPerSec)
		}
		if math.IsNaN(res.CoveragePct) || math.IsInf(res.CoveragePct, 0) {
			t.Fatalf("invalid CoveragePct: %v", res.CoveragePct)
		}
		if res.Execs < 0 || res.NewInteresting < 0 || res.CorpusCount < 0 {
			t.Fatalf("negative counters in result: %+v", res)
		}
		if res.Crashes != len(res.CrashRecords) {
			t.Fatalf("Crashes=%d does not match len(CrashRecords)=%d", res.Crashes, len(res.CrashRecords))
		}
		if res.Status == "crash" && res.Crashes == 0 {
			t.Fatalf("status is crash but Crashes == 0")
		}
		for i, c := range res.CrashRecords {
			if c.InputHash == "" {
				t.Fatalf("crash[%d] has empty InputHash", i)
			}
			if !utf8.ValidString(c.Stack) {
				t.Fatalf("crash[%d] Stack is not valid UTF-8", i)
			}
			if strings.TrimSpace(c.Stack) != "" && len(c.StackFingerprint) != 24 {
				t.Fatalf("crash[%d] unexpected StackFingerprint %q for non-empty stack", i, c.StackFingerprint)
			}
		}
	})
}

func FuzzFingerprintStack(f *testing.F) {
	f.Add("--- FAIL: FuzzDecode (0.42s)\n    /tmp/work/codec_test.go:24: unexpected header 0xdeadbeef\ngoroutine 18 [running]:\npkg.fn at /a/b/file.go:100 +0x1a\nFAIL\texample.com/pkg\t0.42s\n")
	f.Add("panic: boom\ngoroutine 1 [running]:\nmain.go:12 +0x04\n")
	f.Add("   \n\t  ")

	f.Fuzz(func(t *testing.T, stack string) {
		if len(stack) > 64*1024 {
			return
		}
		fp1 := FingerprintStack(stack)
		fp2 := FingerprintStack(stack)
		if fp1 != fp2 {
			t.Fatalf("FingerprintStack is non-deterministic: %q != %q", fp1, fp2)
		}
		if strings.TrimSpace(stack) == "" {
			if fp1 != "" {
				t.Fatalf("expected empty fingerprint for blank stack, got %q", fp1)
			}
			return
		}
		if len(fp1) != 24 {
			t.Fatalf("expected 24-hex-char fingerprint, got %q (len %d)", fp1, len(fp1))
		}
		if _, err := hex.DecodeString(fp1); err != nil {
			t.Fatalf("fingerprint %q is not valid hex: %v", fp1, err)
		}

		// Goroutine ID and duration shifts must not alter the fingerprint.
		shifted := strings.ReplaceAll(stack, "goroutine 1", "goroutine 98765")
		shifted = strings.ReplaceAll(shifted, "(0.42s)", "(9.99s)")
		shifted = strings.ReplaceAll(shifted, "FAIL\texample.com/pkg\t0.42s", "FAIL\texample.com/pkg\t9.99s")
		if fpShift := FingerprintStack(shifted); fpShift != fp1 {
			t.Fatalf("fingerprint changed across goroutine/duration normalization: %q vs %q", fp1, fpShift)
		}

		// ScrubStack must preserve FingerprintStack invariance.
		scrubbed := ScrubStack(stack, "/var/lib/ntfuzz/.ntfuzz/repos/nt3d", "nimbleterminal.dev/nt3d", "/var/lib/ntfuzz/.cache/go-mod")
		if fpScrubbed := FingerprintStack(scrubbed); fpScrubbed != fp1 {
			t.Fatalf("fingerprint changed after ScrubStack: %q vs %q (scrubbed=%q)", fp1, fpScrubbed, scrubbed)
		}
	})
}

func FuzzAttributeAndScrubStack(f *testing.F) {
	f.Add(
		"panic: runtime error: slice bounds out of range [::-1]\ngithub.com/qmuntal/gltf.(*Decoder).decodeBuffer\n    /var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf@v0.29.0/decoder.go:151\nnimbleterminal.dev/nt3d/mesh/gltf.Decode\n    /var/lib/ntfuzz/.ntfuzz/repos/nt3d/mesh/gltf/gltf.go:108\n",
		"/var/lib/ntfuzz/.ntfuzz/repos/nt3d",
		"nimbleterminal.dev/nt3d",
		"/var/lib/ntfuzz/.cache/go-mod",
	)
	f.Add(
		"panic: local bug\nnimbleterminal.dev/nt3d/mesh/gltf.Decode\n\t/var/lib/ntfuzz/.ntfuzz/repos/nt3d/mesh/gltf/gltf.go:42\n",
		"/var/lib/ntfuzz/.ntfuzz/repos/nt3d",
		"nimbleterminal.dev/nt3d",
		"/var/lib/ntfuzz/.cache/go-mod",
	)
	f.Add("", "", "", "")

	f.Fuzz(func(t *testing.T, stack, repoDir, modulePath, goModCache string) {
		if len(stack)+len(repoDir)+len(modulePath)+len(goModCache) > 32*1024 {
			return
		}
		scrubbed := ScrubStack(stack, repoDir, modulePath, goModCache)
		if FingerprintStack(scrubbed) != FingerprintStack(stack) {
			t.Fatalf("FingerprintStack mismatch after ScrubStack")
		}
		attr := AttributeStack(stack, repoDir, modulePath)
		if attr.Module != "" {
			u := DependencySourceURL(attr.Module, attr.Version, attr.File, attr.Line)
			if !strings.HasPrefix(u, "https://") {
				t.Fatalf("DependencySourceURL returned non-https URL %q for %+v", u, attr)
			}
		}
	})
}
