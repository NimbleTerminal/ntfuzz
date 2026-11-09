package orchestrator

import (
	"path/filepath"
	"strings"

	"nimbleterminal.dev/ntfuzz/internal/store"
)

// MaxCrashInputBytes is the maximum number of minimized input bytes persisted
// directly in the crashes table (64 KiB). Inputs larger than this cap are
// truncated in the database and flagged with InputTruncated = true.
const MaxCrashInputBytes = store.MaxCrashInputBytes

// StackAttribution holds dependency blame metadata extracted from a crash stack.
type StackAttribution = store.StackAttribution

// CapCrashInput caps raw corpus bytes at MaxCrashInputBytes (64 KiB) and reports
// whether truncation occurred.
func CapCrashInput(data []byte) (capped []byte, truncated bool) {
	if len(data) == 0 {
		return nil, false
	}
	if len(data) > MaxCrashInputBytes {
		out := make([]byte, MaxCrashInputBytes)
		copy(out, data[:MaxCrashInputBytes])
		return out, true
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, false
}

// deriveModulePath determines the Go module path for a job using its package
// import path and relative directory, falling back to reading go.mod in repoDir.
func deriveModulePath(repoDir, pkgImport, pkgRelDir string) string {
	pkgImport = strings.TrimSpace(pkgImport)
	pkgRelDir = strings.TrimSpace(pkgRelDir)
	if pkgImport != "" {
		rel := strings.TrimPrefix(filepath.ToSlash(pkgRelDir), "./")
		rel = strings.TrimPrefix(rel, "/")
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" || rel == "." {
			return pkgImport
		}
		if strings.HasSuffix(pkgImport, "/"+rel) {
			return strings.TrimSuffix(pkgImport, "/"+rel)
		}
	}
	if repoDir != "" {
		if mod := readModulePath(filepath.Join(repoDir, "go.mod")); mod != "" {
			return mod
		}
	}
	return pkgImport
}

// DeriveModuleRoot derives the Go module root path from a project name and package import path.
func DeriveModuleRoot(projectName, pkgImport string) string {
	return store.DeriveModuleRoot(projectName, pkgImport)
}

// ScrubStack normalizes filesystem paths in a Go crash stack before storage:
//   - Replaces the GOMODCACHE prefix so module frames read <module>@<version>/<rel-path>:<line>
//   - Replaces the work_dir/repoDir checkout prefix with modulePath
//   - Leaves GOROOT frames untouched.
func ScrubStack(stack, repoDir, modulePath, goModCache string) string {
	return store.ScrubStack(stack, repoDir, modulePath, goModCache)
}

// AttributeStack walks stack frames top-down (skipping runtime/, testing/, and reflect)
// and checks whether the first non-runtime frame is in an external module cache path
// (<module>@<version>/<rel-path>:<line>) before any frame in the project's own repo/module.
func AttributeStack(stack, repoDir, projectModule string) StackAttribution {
	return store.AttributeStack(stack, repoDir, projectModule)
}

// HasProjectFrame reports whether stack contains a recognizable in-repo project frame
// when AttributeStack returns empty.
func HasProjectFrame(stack, repoDir, projectModule string) bool {
	return store.HasProjectFrame(stack, repoDir, projectModule)
}

// DependencySourceURL builds a clickable source link for a blamed dependency frame.
// For github.com and gitlab.com modules it returns a blob link with #L<line>
// (stripping /vN major-version suffixes). For other hosts it returns a pkg.go.dev link.
func DependencySourceURL(module, version, file string, line int) string {
	return store.DependencySourceURL(module, version, file, line)
}
