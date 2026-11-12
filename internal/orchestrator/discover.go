package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// CheckoutInfo holds the local path and revision state of a synced repository.
type CheckoutInfo struct {
	RepoDir   string
	GitSHA    string
	Dirty     bool
	GoVersion string
}

// DiscoveredTarget represents a Go fuzz target function discovered in a repository.
type DiscoveredTarget struct {
	ModuleDir     string // Directory containing the nearest ancestor go.mod
	PackageImport string // e.g. "example.com/demo/parser"
	PackageRelDir string // Relative to ModuleDir, e.g. "." or "./subpkg"
	FuncName      string // e.g. "FuzzParseExpr"
}

// EnsureCheckout ensures the project repository is available locally and returns its directory and git revision metadata.
// If a remote git pull fails, it returns the current CheckoutInfo along with a non-nil error so callers can surface
// the failure without advancing last_synced.
func EnsureCheckout(ctx context.Context, proj ProjectConfig, workDir string) (CheckoutInfo, error) {
	goVer := detectGoVersion(ctx)
	repo := strings.TrimSpace(proj.Repo)

	if info, err := os.Stat(repo); err == nil && info.IsDir() {
		abs, err := filepath.Abs(repo)
		if err != nil {
			abs = repo
		}
		sha, dirty := inspectGitRevision(ctx, abs)
		return CheckoutInfo{
			RepoDir:   abs,
			GitSHA:    sha,
			Dirty:     dirty,
			GoVersion: goVer,
		}, nil
	}

	if workDir == "" {
		workDir = filepath.Join(".ntfuzz", "repos")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return CheckoutInfo{GoVersion: goVer}, fmt.Errorf("mkdir workDir %q: %w", workDir, err)
	}

	dest := filepath.Join(workDir, proj.Name)
	if absDest, err := filepath.Abs(dest); err == nil {
		dest = absDest
	}

	gitDir := filepath.Join(dest, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		// Managed remote clones under workDir should never retain untracked or modified
		// files (such as go.sum or leftover test artifacts written by go test) that
		// would block git checkout or git pull --ff-only.
		_ = runGit(ctx, dest, "reset", "--hard")
		_ = runGit(ctx, dest, "clean", "-fd")
		if proj.Branch != "" {
			if err := runGit(ctx, dest, "checkout", proj.Branch); err != nil {
				sha, dirty := inspectGitRevision(ctx, dest)
				return CheckoutInfo{RepoDir: dest, GitSHA: sha, Dirty: dirty, GoVersion: goVer}, err
			}
		}
		if err := runGit(ctx, dest, "pull", "--ff-only"); err != nil {
			sha, dirty := inspectGitRevision(ctx, dest)
			return CheckoutInfo{RepoDir: dest, GitSHA: sha, Dirty: dirty, GoVersion: goVer}, err
		}
		sha, dirty := inspectGitRevision(ctx, dest)
		return CheckoutInfo{
			RepoDir:   dest,
			GitSHA:    sha,
			Dirty:     dirty,
			GoVersion: goVer,
		}, nil
	}

	args := []string{"clone", "--depth", "1"}
	if proj.Branch != "" {
		args = append(args, "--branch", proj.Branch)
	}
	args = append(args, repo, dest)
	if err := runGit(ctx, "", args...); err != nil {
		return CheckoutInfo{GoVersion: goVer}, err
	}

	sha, dirty := inspectGitRevision(ctx, dest)
	return CheckoutInfo{
		RepoDir:   dest,
		GitSHA:    sha,
		Dirty:     dirty,
		GoVersion: goVer,
	}, nil
}

func inspectGitRevision(ctx context.Context, dir string) (string, bool) {
	env := gitEnv()
	shaCmd := exec.CommandContext(ctx, "git", "rev-parse", "--short=12", "HEAD")
	shaCmd.Dir = dir
	shaCmd.Env = env
	shaOut, err := shaCmd.Output()
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(string(shaOut))

	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "--", ".")
	statusCmd.Dir = dir
	statusCmd.Env = env
	statusOut, err := statusCmd.Output()
	dirty := err == nil && len(strings.TrimSpace(string(statusOut))) > 0
	return sha, dirty
}

func detectGoVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	if out, err := cmd.Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v
		}
	}
	return runtime.Version()
}

const (
	defaultGitSSHCommand = "ssh -o BatchMode=yes"
	privateRepoAuthHint  = "repository may be private; see docs/deployment-cookbook.md 'Private repositories'"
)

var gitAuthFailurePatterns = []string{
	"Authentication failed",
	"Permission denied (publickey)",
	"could not read Username",
	"Repository not found",
}

func gitEnv() []string {
	return buildGitEnv(os.Environ())
}

func buildGitEnv(base []string) []string {
	out := make([]string, 0, len(base)+2)
	var gitSSH string
	var ntfuzzSSH string
	for _, kv := range base {
		switch {
		case strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT="):
			continue
		case strings.HasPrefix(kv, "NTFUZZ_GIT_SSH_COMMAND="):
			ntfuzzSSH = strings.TrimSpace(strings.TrimPrefix(kv, "NTFUZZ_GIT_SSH_COMMAND="))
			out = append(out, kv)
		case strings.HasPrefix(kv, "GIT_SSH_COMMAND="):
			gitSSH = strings.TrimSpace(strings.TrimPrefix(kv, "GIT_SSH_COMMAND="))
		default:
			out = append(out, kv)
		}
	}
	out = append(out, "GIT_TERMINAL_PROMPT=0")
	switch {
	case ntfuzzSSH != "":
		out = append(out, "GIT_SSH_COMMAND="+ntfuzzSSH)
	case gitSSH != "":
		out = append(out, "GIT_SSH_COMMAND="+gitSSH)
	default:
		out = append(out, "GIT_SSH_COMMAND="+defaultGitSSHCommand)
	}
	return out
}

func isGitAuthFailure(output string) bool {
	lower := strings.ToLower(output)
	for _, pat := range gitAuthFailurePatterns {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

func formatGitCommandError(dir string, args []string, err error, out []byte) error {
	redactedArgs := make([]string, len(args))
	for i, a := range args {
		redactedArgs[i] = sanitizeDiagnostic(RedactRepoURL(a), dir)
	}
	outStr := sanitizeDiagnostic(strings.TrimSpace(string(out)), dir)
	msg := fmt.Errorf("git %s: %w (%s)", strings.Join(redactedArgs, " "), err, outStr)
	if len(args) > 0 && (args[0] == "clone" || args[0] == "pull" || args[0] == "fetch" || args[0] == "checkout") && isGitAuthFailure(outStr) {
		return fmt.Errorf("%w: %s", msg, privateRepoAuthHint)
	}
	return msg
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return formatGitCommandError(dir, args, err, out)
	}
	return nil
}

// DiscoverTargets scans repoDir for *_test.go files matching current OS/arch build constraints
// and containing top-level FuzzXxx(f *testing.F) functions. Supports multi-module repositories.
func DiscoverTargets(ctx context.Context, repoDir string) ([]DiscoveredTarget, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err == nil {
		repoDir = absRepo
	}

	fset := token.NewFileSet()
	bctx := build.Default

	// Cache discovered go.mod paths by directory
	modCache := map[string]struct {
		modDir  string
		modPath string
	}{}

	findModule := func(pkgDir string) (string, string) {
		if cached, ok := modCache[pkgDir]; ok {
			return cached.modDir, cached.modPath
		}
		cur := pkgDir
		for {
			goModFile := filepath.Join(cur, "go.mod")
			if mp := readModulePath(goModFile); mp != "" {
				modCache[pkgDir] = struct {
					modDir  string
					modPath string
				}{cur, mp}
				return cur, mp
			}
			if cur == repoDir {
				break
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
		modCache[pkgDir] = struct {
			modDir  string
			modPath string
		}{repoDir, ""}
		return repoDir, ""
	}

	type key struct {
		pkgImport string
		name      string
	}
	seen := map[key]DiscoveredTarget{}

	err = filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if path != repoDir && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules" || name == "examples") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		pkgDir := filepath.Dir(path)
		// Respect //go:build constraints and OS/arch file suffixes
		if match, matchErr := bctx.MatchFile(pkgDir, d.Name()); matchErr != nil || !match {
			return nil
		}

		fileAst, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}

		modDir, modPath := findModule(pkgDir)
		relDir, relErr := filepath.Rel(modDir, pkgDir)
		if relErr != nil || relDir == "." {
			relDir = "."
		} else {
			relDir = "./" + filepath.ToSlash(relDir)
		}

		pkgImport := modPath
		if pkgImport == "" {
			pkgImport = fileAst.Name.Name
		} else if relDir != "." {
			pkgImport = strings.TrimRight(modPath, "/") + "/" + strings.TrimPrefix(relDir, "./")
		}

		for _, decl := range fileAst.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name == nil {
				continue
			}
			name := fn.Name.Name
			if !strings.HasPrefix(name, "Fuzz") || len(name) <= 4 {
				continue
			}
			if !isTestingFParam(fn.Type) {
				continue
			}
			k := key{pkgImport: pkgImport, name: name}
			seen[k] = DiscoveredTarget{
				ModuleDir:     modDir,
				PackageImport: pkgImport,
				PackageRelDir: relDir,
				FuncName:      name,
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %q: %w", repoDir, err)
	}

	targets := make([]DiscoveredTarget, 0, len(seen))
	for _, t := range seen {
		targets = append(targets, t)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].PackageImport != targets[j].PackageImport {
			return targets[i].PackageImport < targets[j].PackageImport
		}
		return targets[i].FuncName < targets[j].FuncName
	})
	return targets, nil
}

func isTestingFParam(ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil || len(ft.Params.List) != 1 {
		return false
	}
	param := ft.Params.List[0]
	if len(param.Names) > 1 {
		return false
	}
	star, ok := param.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkgIdent.Name == "testing" && sel.Sel.Name == "F"
}

func readModulePath(goModPath string) string {
	f, err := os.Open(goModPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	return ""
}
