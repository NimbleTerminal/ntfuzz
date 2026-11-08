package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/store"
)

// replayCrash executes exactly the saved reproducer in a disposable module copy.
// A process exit of zero is insufficient: the named subtest must actually pass.
func replayCrash(ctx context.Context, job JobSpec, c store.Crash) (outcome, diagnostic string) {
	blocked := func(err error) (string, string) {
		return "blocked", sanitizeDiagnostic(err.Error(), job.RepoDir, job.CorpusDir)
	}
	var data []byte
	var err error
	if len(c.InputBytes) > 0 && !c.InputTruncated {
		data = c.InputBytes
	} else {
		data, err = os.ReadFile(c.MinimizedInput)
		if err != nil {
			return blocked(fmt.Errorf("saved input unavailable: %w", err))
		}
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:16]) != c.InputHash {
		return blocked(fmt.Errorf("saved input hash mismatch"))
	}
	root, err := os.MkdirTemp("", "ntfuzz-replay-*")
	if err != nil {
		return blocked(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	moduleDir, err := filepath.Abs(job.RepoDir)
	if err != nil {
		return blocked(err)
	}
	base := filepath.Base(moduleDir)
	// Preserve sibling module replacements, as in coverage measurement.
	if siblings, e := os.ReadDir(filepath.Dir(moduleDir)); e == nil {
		for _, s := range siblings {
			if s.Name() != base && !strings.HasPrefix(s.Name(), ".") {
				_ = os.Symlink(filepath.Join(filepath.Dir(moduleDir), s.Name()), filepath.Join(root, s.Name()))
			}
		}
	}
	copied := filepath.Join(root, base)
	if err = os.MkdirAll(copied, 0755); err != nil {
		return blocked(err)
	}
	if err = copyModuleTree(moduleDir, copied); err != nil {
		return blocked(err)
	}
	pkg := job.PkgRelDir
	if pkg == "" {
		pkg = "."
	}
	if pkg != "." && !strings.HasPrefix(pkg, "./") {
		pkg = "./" + pkg
	}
	pkgDir := filepath.Join(copied, pkg)
	seedName := fmt.Sprintf("ntfuzz_replay_%d", c.ID)
	seedDir := filepath.Join(pkgDir, "testdata", "fuzz", job.TargetName)
	if err = os.MkdirAll(seedDir, 0755); err != nil {
		return blocked(err)
	}
	if err = os.WriteFile(filepath.Join(seedDir, seedName), data, 0600); err != nil {
		return blocked(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	testName := job.TargetName + "/" + seedName
	pattern := "^" + regexp.QuoteMeta(job.TargetName) + "$/^" + regexp.QuoteMeta(seedName) + "$"
	cmd := exec.CommandContext(runCtx, "go", "test", pkg, "-json", "-count=1", "-parallel=1", "-run="+pattern)
	cmd.Dir = copied
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	if runCtx.Err() != nil {
		return blocked(runCtx.Err())
	}
	passed, failed, ran := false, false, false
	for _, line := range strings.Split(output.String(), "\n") {
		var event struct{ Action, Test string }
		if json.Unmarshal([]byte(line), &event) != nil || event.Test != testName {
			continue
		}
		switch event.Action {
		case "run":
			ran = true
		case "pass":
			passed = true
		case "fail":
			failed = true
		}
	}
	diagnostic = sanitizeDiagnostic(output.String(), copied, job.CorpusDir)
	if len(diagnostic) > 4000 {
		diagnostic = diagnostic[len(diagnostic)-4000:]
	}
	if err == nil && ran && passed && !failed {
		return "passed", "Saved input executed successfully on this revision"
	}
	if ran && failed {
		return "failed", diagnostic
	}
	return "blocked", "Reproducer did not complete (build error, missing target, or invalid input): " + diagnostic
}

func (o *Orchestrator) replayCrashes(ctx context.Context, item scheduledTarget, job JobSpec) {
	if item.checkout.GitSHA == "" {
		return
	}
	crashes, err := o.store.ListCrashes(ctx, false, fmt.Sprint(item.targetID))
	if err != nil {
		o.logger.Warn("list replay inputs failed", "err", err)
		return
	}
	for _, c := range crashes {
		if ctx.Err() != nil {
			return
		}
		if c.GitSHA == item.checkout.GitSHA {
			continue
		}
		needed, err := o.store.NeedsReplay(ctx, c, item.checkout.GitSHA)
		if err != nil {
			o.logger.Warn("check replay state failed", "err", err)
			continue
		}
		if !needed {
			continue
		}
		checked := time.Now().UTC()
		outcome, diagnostic := "blocked", "Checkout is dirty; replay cannot verify a committed revision"
		if !item.checkout.Dirty {
			outcome, diagnostic = replayCrash(ctx, job, c)
		}
		if ctx.Err() != nil {
			return
		}
		result := store.CrashReplay{GitSHA: item.checkout.GitSHA, CheckedAt: checked, Outcome: outcome, Diagnostic: diagnostic}
		if err = o.store.RecordReplay(ctx, c, result); err != nil {
			o.logger.Warn("record replay failed", "err", err)
			continue
		}
		o.logger.Info("replayed saved crash", "project", job.Project, "target", job.TargetName, "crash_id", c.ID, "sha", item.checkout.GitSHA, "outcome", outcome)
	}
}
