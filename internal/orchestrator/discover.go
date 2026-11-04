package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoveredTarget represents a Go fuzz target function discovered in a repository.
type DiscoveredTarget struct {
	PackageImport string // e.g. "example.com/demo/parser"
	PackageRelDir string // e.g. "." or "./subpkg"
	FuncName      string // e.g. "FuzzParseExpr"
}

// EnsureCheckout ensures the project repository is available locally and returns its directory path.
// If proj.Repo points to an existing local directory, it is used directly.
// Otherwise, it clones or pulls the Git repository into workDir/<proj.Name>.
func EnsureCheckout(ctx context.Context, proj ProjectConfig, workDir string) (string, error) {
	repo := strings.TrimSpace(proj.Repo)
	if info, err := os.Stat(repo); err == nil && info.IsDir() {
		abs, err := filepath.Abs(repo)
		if err != nil {
			return repo, nil
		}
		// If it's a git repo and branch is specified or remote exists, we leave local checkouts untouched
		// so local uncommitted fuzz targets aren't overwritten.
		return abs, nil
	}

	if workDir == "" {
		workDir = filepath.Join(".ntfuzz", "repos")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir workDir %q: %w", workDir, err)
	}

	dest := filepath.Join(workDir, proj.Name)
	absDest, err := filepath.Abs(dest)
	if err == nil {
		dest = absDest
	}

	gitDir := filepath.Join(dest, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		// Existing clone: fetch & pull
		if proj.Branch != "" {
			_ = runGit(ctx, dest, "checkout", proj.Branch)
		}
		if err := runGit(ctx, dest, "pull", "--ff-only"); err != nil {
			// Non-fatal if offline or detached HEAD; log/return dest anyway
			return dest, nil
		}
		return dest, nil
	}

	args := []string{"clone", "--depth", "1"}
	if proj.Branch != "" {
		args = append(args, "--branch", proj.Branch)
	}
	args = append(args, repo, dest)
	if err := runGit(ctx, "", args...); err != nil {
		return "", fmt.Errorf("git clone %s: %w", repo, err)
	}
	return dest, nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DiscoverTargets scans repoDir for *_test.go files containing top-level FuzzXxx(f *testing.F) functions.
func DiscoverTargets(ctx context.Context, repoDir string) ([]DiscoveredTarget, error) {
	modPath := readModulePath(filepath.Join(repoDir, "go.mod"))
	fset := token.NewFileSet()

	type key struct {
		relDir string
		name   string
	}
	seen := map[key]DiscoveredTarget{}

	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if path != repoDir && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		fileAst, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}

		pkgDir := filepath.Dir(path)
		relDir, relErr := filepath.Rel(repoDir, pkgDir)
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
			k := key{relDir: relDir, name: name}
			seen[k] = DiscoveredTarget{
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
	defer f.Close()

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
