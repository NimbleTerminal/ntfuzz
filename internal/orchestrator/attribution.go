package orchestrator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// MaxCrashInputBytes is the maximum number of minimized input bytes persisted
// directly in the crashes table (64 KiB). Inputs larger than this cap are
// truncated in the database and flagged with InputTruncated = true.
const MaxCrashInputBytes = 64 * 1024

// StackAttribution holds dependency blame metadata extracted from a crash stack.
type StackAttribution struct {
	Module  string
	Version string
	File    string
	Line    int
}

var (
	// Matches a module cache segment: <module>@<version>/<rel-path>.go:<line>
	reModCacheFrame = regexp.MustCompile(`([A-Za-z0-9_.\-~!]+(?:/[A-Za-z0-9_.\-~!]+)+)@(v[^\s/:]+?)/([^\s:]+\.go):(\d+)`)

	// Matches a module cache path without line number: <module>@<version>/<rel-path>.go
	reModCacheFilePath = regexp.MustCompile(`([A-Za-z0-9_.\-~!]+(?:/[A-Za-z0-9_.\-~!]+)+)@(v[A-Za-z0-9_.\-~!+]+)/([A-Za-z0-9_./\-~!+]+\.go)$`)

	// Matches a file:line token using the exact character class of FingerprintStack's reFileLine.
	reScrubFileLine = regexp.MustCompile(`([A-Za-z0-9_./\\@!~+-]+\.go):(\d+)`)

	// Matches a file:line reference in a stack frame.
	reStackFileLine = regexp.MustCompile(`([^\s:]+\.go):(\d+)`)

	// Matches a Go pseudo-version ending in -YYYYMMDDHHMMSS-<commit>.
	rePseudoVersion = regexp.MustCompile(`-\d{14}-([0-9a-fA-F]{12,40})$`)
)

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

// ScrubStack normalizes filesystem paths in a Go crash stack before storage:
//   - Replaces the GOMODCACHE prefix so module frames read <module>@<version>/<rel-path>:<line>
//   - Replaces the work_dir/repoDir checkout prefix with modulePath
//   - Leaves GOROOT frames untouched.
func ScrubStack(stack, repoDir, modulePath, goModCache string) string {
	if stack == "" {
		return ""
	}

	cleanRepo := cleanSlashPath(repoDir)
	cleanMod := strings.Trim(strings.TrimSpace(modulePath), "/")
	cleanModCache := cleanSlashPath(goModCache)

	lines := strings.Split(stack, "\n")
	var prevFuncPkg string
	for i, rawLine := range lines {
		hasCR := strings.HasSuffix(rawLine, "\r")
		line := strings.TrimSuffix(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			prevFuncPkg = ""
			continue
		}

		var scrubbed string
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "    ") {
			prevFuncPkg = extractFuncPackagePath(trimmed)
			scrubbed = scrubSingleLine(line, cleanRepo, cleanMod, cleanModCache, "")
		} else {
			scrubbed = scrubSingleLine(line, cleanRepo, cleanMod, cleanModCache, prevFuncPkg)
			prevFuncPkg = ""
		}
		if hasCR {
			scrubbed += "\r"
		}
		lines[i] = scrubbed
	}

	return strings.Join(lines, "\n")
}

func scrubSingleLine(line, cleanRepo, cleanMod, cleanModCache, prevFuncPkg string) string {
	if !strings.Contains(line, ".go:") {
		return line
	}
	matches := reScrubFileLine.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return line
	}

	var b strings.Builder
	lastIdx := 0
	for _, m := range matches {
		pathStart, pathEnd := m[2], m[3]
		origPath := line[pathStart:pathEnd]
		newPath := origPath
		if strings.HasPrefix(origPath, "/") && len(filepath.Base(origPath)) > len(".go") {
			candidate := scrubSingleFilePath(origPath, cleanRepo, cleanMod, cleanModCache, prevFuncPkg)
			if candidate != "" && candidate != origPath && isValidScrubFilePath(candidate) && filepath.Base(candidate) == filepath.Base(origPath) {
				newPath = candidate
			}
		}
		b.WriteString(line[lastIdx:pathStart])
		b.WriteString(newPath)
		lastIdx = pathEnd
	}
	b.WriteString(line[lastIdx:])
	return b.String()
}

func scrubSingleFilePath(origPath, cleanRepo, cleanMod, cleanModCache, prevFuncPkg string) string {
	slashPath := filepath.ToSlash(origPath)

	// 1. Explicit GOMODCACHE prefix replacement if provided.
	if cleanModCache != "" && strings.HasPrefix(slashPath, cleanModCache+"/") {
		slashPath = strings.TrimPrefix(slashPath, cleanModCache+"/")
	}

	// 2. Normalize any module cache path prefix before <module>@<version>/<rel>.go.
	if strings.Contains(slashPath, "@v") {
		if sub := reModCacheFilePath.FindStringSubmatch(slashPath); sub != nil {
			modPath := unescapeGoModulePath(stripFilesystemPrefixFromModule(sub[1]))
			ver := unescapeGoModulePath(sub[2])
			relFile := sub[3]
			return fmt.Sprintf("%s@%s/%s", modPath, ver, relFile)
		}
	}

	// 3. Explicit repoDir -> modulePath replacement.
	if cleanRepo != "" && cleanMod != "" && strings.HasPrefix(slashPath, cleanRepo+"/") {
		rel := strings.TrimPrefix(slashPath, cleanRepo+"/")
		return cleanMod + "/" + rel
	}

	// 4. If a project frame still has a local absolute path and we know the modulePath
	// (or preceding function package path), scrub the checkout prefix while leaving GOROOT alone.
	if strings.HasPrefix(slashPath, "/") && !strings.Contains(slashPath, "@v") && !isGOROOTPath(slashPath) {
		targetPkg := ""
		if cleanMod != "" && (prevFuncPkg == cleanMod || strings.HasPrefix(prevFuncPkg, cleanMod+"/")) {
			targetPkg = prevFuncPkg
		} else if cleanMod == "" && hasDomainPrefix(prevFuncPkg) {
			targetPkg = prevFuncPkg
		}
		if targetPkg != "" {
			return targetPkg + "/" + filepath.Base(slashPath)
		}
	}

	return slashPath
}

func isValidScrubFilePath(p string) bool {
	if p == "" || !strings.HasSuffix(p, ".go") {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		ok := (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '/' || c == '\\' ||
			c == '@' || c == '!' || c == '~' || c == '+' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// stripFilesystemPrefixFromModule extracts the Go module path from a slash-separated
// path that may start with local filesystem directories (e.g., "var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf").
func stripFilesystemPrefixFromModule(raw string) string {
	raw = strings.TrimPrefix(filepath.ToSlash(raw), "/")
	// Look for known modcache directory markers first.
	for _, marker := range []string{"/pkg/mod/cache/download/", "/pkg/mod/", "/go-mod/", "/mod/"} {
		if idx := strings.LastIndex("/"+raw, marker); idx >= 0 {
			candidate := ("/" + raw)[idx+len(marker):]
			if candidate != "" {
				raw = candidate
				break
			}
		}
	}
	// Find the first path segment that looks like a domain name (contains a dot, not starting with a dot).
	parts := strings.Split(raw, "/")
	for i, seg := range parts {
		if seg == "" || strings.HasPrefix(seg, ".") {
			continue
		}
		if strings.Contains(seg, ".") {
			return strings.Join(parts[i:], "/")
		}
	}
	return raw
}

// AttributeStack walks stack frames top-down (skipping runtime/, testing/, and reflect)
// and checks whether the first non-runtime frame is in an external module cache path
// (<module>@<version>/<rel-path>:<line>) before any frame in the project's own repo/module.
func AttributeStack(stack, repoDir, projectModule string) StackAttribution {
	if stack == "" {
		return StackAttribution{}
	}
	normalized := strings.ReplaceAll(stack, "\r\n", "\n")
	cleanRepo := cleanSlashPath(repoDir)
	cleanMod := strings.Trim(strings.TrimSpace(projectModule), "/")

	lines := strings.Split(normalized, "\n")
	var prevFunc string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			prevFunc = ""
			continue
		}

		// Header / metadata lines that are not stack frames.
		if isStackMetadataLine(trimmed) {
			prevFunc = ""
			continue
		}

		isIndented := strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
		if !isIndented && !strings.Contains(trimmed, ".go:") {
			prevFunc = trimmed
			continue
		}

		// Check if this line contains a <file>.go:<line> frame.
		if !strings.Contains(trimmed, ".go:") {
			prevFunc = ""
			continue
		}

		fn := prevFunc
		prevFunc = ""

		// Skip runtime/, testing/, and reflect frames.
		if isSkippedRuntimeFrame(fn, trimmed) {
			continue
		}

		// Check if this frame matches a module cache pattern <module>@<version>/<rel-path>.go:<line>.
		if sub := reModCacheFrame.FindStringSubmatch(trimmed); sub != nil {
			modPath := unescapeGoModulePath(stripFilesystemPrefixFromModule(sub[1]))
			ver := unescapeGoModulePath(sub[2])
			relFile := filepath.ToSlash(sub[3])
			lineNum, _ := strconv.Atoi(sub[4])

			if cleanMod != "" && modPath == cleanMod {
				// Frame belongs to the project's own module.
				return StackAttribution{}
			}
			return StackAttribution{
				Module:  modPath,
				Version: ver,
				File:    relFile,
				Line:    lineNum,
			}
		}

		// Check if this frame is inside the project's own repo/module.
		if isProjectFrame(fn, trimmed, cleanRepo, cleanMod) {
			return StackAttribution{}
		}
	}

	return StackAttribution{}
}

// DependencySourceURL builds a clickable source link for a blamed dependency frame.
// For github.com and gitlab.com modules it returns a blob link with #L<line>
// (stripping /vN major-version suffixes). For other hosts it returns a pkg.go.dev link.
func DependencySourceURL(module, version, file string, line int) string {
	module = strings.Trim(strings.TrimSpace(module), "/")
	version = strings.TrimSpace(version)
	file = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(file)), "/")
	if module == "" {
		return ""
	}

	parts := strings.Split(module, "/")
	if len(parts) >= 3 && (parts[0] == "github.com" || parts[0] == "gitlab.com") && parts[1] != "" && parts[2] != "" {
		host := parts[0]
		owner := parts[1]
		repo := parts[2]
		subparts := parts[3:]
		if len(subparts) > 0 && isMajorVersionSuffix(subparts[len(subparts)-1]) {
			subparts = subparts[:len(subparts)-1]
		}

		relPath := file
		if len(subparts) > 0 {
			if relPath != "" {
				relPath = strings.Join(subparts, "/") + "/" + relPath
			} else {
				relPath = strings.Join(subparts, "/")
			}
		}

		ref := normalizeGitRef(version)
		if ref != "" && relPath != "" {
			u := fmt.Sprintf("https://%s/%s/%s/blob/%s/%s", host, owner, repo, ref, relPath)
			if line > 0 {
				u += fmt.Sprintf("#L%d", line)
			}
			return u
		}
		if ref != "" {
			return fmt.Sprintf("https://%s/%s/%s/tree/%s", host, owner, repo, ref)
		}
		return fmt.Sprintf("https://%s/%s/%s", host, owner, repo)
	}

	if version != "" {
		return fmt.Sprintf("https://pkg.go.dev/%s@%s", module, version)
	}
	return fmt.Sprintf("https://pkg.go.dev/%s", module)
}

func isMajorVersionSuffix(seg string) bool {
	if len(seg) < 2 || seg[0] != 'v' {
		return false
	}
	if seg[1] < '2' || seg[1] > '9' {
		if len(seg) == 2 {
			return false
		}
	}
	for i := 1; i < len(seg); i++ {
		if seg[i] < '0' || seg[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(seg[1:])
	return err == nil && n >= 2
}

func normalizeGitRef(version string) string {
	ref := strings.TrimSpace(version)
	ref = strings.TrimSuffix(ref, "+incompatible")
	ref = strings.TrimSuffix(ref, "+dirty")
	if m := rePseudoVersion.FindStringSubmatch(ref); m != nil {
		return m[1]
	}
	return ref
}

func unescapeGoModulePath(s string) string {
	if !strings.Contains(s, "!") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '!' && i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z' {
			b.WriteByte(byte(unicode.ToUpper(rune(s[i+1]))))
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func cleanSlashPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimSuffix(p, "/")
	if p == "." || p == "" {
		return ""
	}
	return p
}

func extractFuncPackagePath(funcLine string) string {
	if isStackMetadataLine(funcLine) {
		return ""
	}
	// Strip arguments (...) from the end of a function call line.
	if paren := strings.IndexByte(funcLine, '('); paren > 0 {
		// Might be receiver method like pkg.(*Type).Method(...) or pkg.Func(...)
		prefix := funcLine[:paren]
		if strings.HasSuffix(prefix, ".") {
			// Receiver: pkg.(*Type).Method(...)
			funcLine = strings.TrimSuffix(prefix, ".")
		} else {
			funcLine = prefix
		}
	}
	lastSlash := strings.LastIndexByte(funcLine, '/')
	searchFrom := 0
	if lastSlash >= 0 {
		searchFrom = lastSlash + 1
	}
	dot := strings.IndexByte(funcLine[searchFrom:], '.')
	if dot <= 0 {
		return ""
	}
	pkg := funcLine[:searchFrom+dot]
	if strings.ContainsAny(pkg, " \t:") {
		return ""
	}
	return pkg
}

func isStackMetadataLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "panic:") ||
		strings.HasPrefix(trimmed, "fatal error:") ||
		strings.HasPrefix(trimmed, "goroutine ") ||
		strings.HasPrefix(trimmed, "--- FAIL:") ||
		strings.HasPrefix(trimmed, "FAIL\t") ||
		strings.HasPrefix(trimmed, "FAIL ") ||
		strings.HasPrefix(trimmed, "exit status ") ||
		strings.HasPrefix(trimmed, "created by ")
}

func isSkippedRuntimeFrame(funcLine, fileLine string) bool {
	pkg := extractFuncPackagePath(funcLine)
	if pkg == "runtime" || strings.HasPrefix(pkg, "runtime/") ||
		pkg == "testing" || strings.HasPrefix(pkg, "testing/") ||
		pkg == "reflect" || strings.HasPrefix(pkg, "reflect/") ||
		pkg == "internal/fuzz" || strings.HasPrefix(pkg, "internal/fuzz/") {
		return true
	}

	m := reStackFileLine.FindStringSubmatch(fileLine)
	if m == nil {
		return false
	}
	fp := filepath.ToSlash(m[1])
	for _, prefix := range []string{
		"/src/runtime/", "runtime/",
		"/src/testing/", "testing/",
		"/src/reflect/", "reflect/",
		"/src/internal/fuzz/", "internal/fuzz/",
	} {
		if strings.Contains(fp, prefix) || strings.HasPrefix(fp, prefix) {
			return true
		}
	}
	return false
}

func isProjectFrame(funcLine, fileLine, cleanRepo, cleanMod string) bool {
	pkg := extractFuncPackagePath(funcLine)
	if cleanMod != "" && (pkg == cleanMod || strings.HasPrefix(pkg, cleanMod+"/")) {
		return true
	}

	m := reStackFileLine.FindStringSubmatch(fileLine)
	if m == nil {
		return false
	}
	fp := filepath.ToSlash(m[1])
	if cleanRepo != "" && strings.HasPrefix(fp, cleanRepo+"/") {
		return true
	}
	if cleanMod != "" && (fp == cleanMod || strings.HasPrefix(fp, cleanMod+"/")) {
		return true
	}
	if isGOROOTPath(fp) {
		return false
	}
	// A relative file path (such as "codec_test.go" or "mesh/gltf/gltf.go") or
	// a non-stdlib domain path without @version is in the project's own code.
	if !strings.HasPrefix(fp, "/") && !strings.Contains(fp, "@") {
		return true
	}
	if hasDomainPrefix(pkg) && !strings.Contains(fp, "@") {
		return true
	}
	return false
}

func isGOROOTPath(fp string) bool {
	fp = filepath.ToSlash(fp)
	if strings.Contains(fp, "/src/runtime/") || strings.Contains(fp, "/src/testing/") || strings.Contains(fp, "/src/reflect/") {
		return true
	}
	// Standard GOROOT layout has /src/<stdlib-pkg>/<file>.go where <stdlib-pkg> has no domain dot.
	if idx := strings.Index(fp, "/src/"); idx >= 0 {
		rest := fp[idx+len("/src/"):]
		firstSeg, _, _ := strings.Cut(rest, "/")
		if firstSeg != "" && !strings.Contains(firstSeg, ".") {
			return true
		}
	}
	return false
}

func hasDomainPrefix(pkg string) bool {
	if pkg == "" {
		return false
	}
	first, _, ok := strings.Cut(pkg, "/")
	if !ok {
		return false
	}
	return strings.Contains(first, ".") && !strings.HasPrefix(first, ".")
}
