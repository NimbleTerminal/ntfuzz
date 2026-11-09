package store

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

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

	// Matches a file:line token using the exact character class of FingerprintStack's reFPFileLine.
	reScrubFileLine = regexp.MustCompile(`([A-Za-z0-9_./\\@!~+-]+\.go):(\d+)`)

	// Matches a file:line reference in a stack frame.
	reStackFileLine = regexp.MustCompile(`([^\s:]+\.go):(\d+)`)

	// Matches a Go test runner log prefix line (<file>.go:<line>: <message>).
	reGoTestLogLine = regexp.MustCompile(`^[A-Za-z0-9_./\\@!~+-]+\.go:\d+:(\s|$)`)

	// Matches a Go pseudo-version ending in -YYYYMMDDHHMMSS-<commit>.
	rePseudoVersion = regexp.MustCompile(`-\d{14}-([0-9a-fA-F]{12,40})$`)
)

// DeriveModuleRoot derives the Go module root path from a project name and package import path.
func DeriveModuleRoot(projectName, pkgImport string) string {
	pkgImport = strings.Trim(strings.TrimSpace(pkgImport), "/")
	projectName = strings.Trim(strings.TrimSpace(projectName), "/")
	if pkgImport == "" {
		return ""
	}
	if projectName != "" {
		lowerPkg := strings.ToLower(pkgImport)
		lowerProj := strings.ToLower(projectName)
		if lowerPkg == lowerProj || strings.HasSuffix(lowerPkg, "/"+lowerProj) {
			return pkgImport
		}
		needle := "/" + lowerProj + "/"
		if idx := strings.Index(lowerPkg, needle); idx >= 0 {
			return pkgImport[:idx+1+len(projectName)]
		}
	}
	return pkgImport
}

// ScrubStack normalizes filesystem paths in a Go crash stack:
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
		if isStackMetadataLine(trimmed) {
			prevFuncPkg = ""
			scrubbed = scrubSingleLine(line, cleanRepo, cleanMod, cleanModCache, "")
		} else if !strings.Contains(trimmed, ".go:") {
			prevFuncPkg = extractFuncPackagePath(trimmed)
			scrubbed = line
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
	return reScrubFileLine.ReplaceAllStringFunc(line, func(match string) string {
		colon := strings.LastIndexByte(match, ':')
		if colon <= 0 {
			return match
		}
		origPath := match[:colon]
		lineNum := match[colon:]
		if !strings.HasPrefix(origPath, "/") && !strings.HasPrefix(origPath, `\`) {
			return match
		}
		base := filepath.Base(origPath)
		if len(base) <= len(".go") || !isValidScrubFilePath(origPath) {
			return match
		}
		replaced := scrubSingleFilePath(origPath, cleanRepo, cleanMod, cleanModCache, prevFuncPkg)
		if !isValidScrubFilePath(replaced) || !isValidScrubStart(replaced) || filepath.Base(replaced) != base {
			return match
		}
		return replaced + lineNum
	})
}

func isValidScrubStart(p string) bool {
	if p == "" {
		return false
	}
	c := p[0]
	return c == '/' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

func scrubSingleFilePath(origPath, cleanRepo, cleanMod, cleanModCache, prevFuncPkg string) string {
	slashPath := filepath.ToSlash(origPath)

	// 0. Never scrub GOROOT / stdlib source paths.
	if isGOROOTPath(slashPath) {
		return slashPath
	}

	// 1. Explicit GOMODCACHE prefix.
	if cleanModCache != "" && strings.HasPrefix(slashPath, cleanModCache+"/") {
		rel := strings.TrimPrefix(slashPath, cleanModCache+"/")
		rel = strings.TrimPrefix(rel, "cache/download/")
		if rel != "" {
			return rel
		}
	}

	// 2. Heuristic module cache path containing <module>@<version>/<rel-path>.go.
	if strings.Contains(slashPath, "@v") {
		if m := reModCacheFilePath.FindStringSubmatch(slashPath); m != nil {
			modPath := stripFilesystemPrefixFromModule(m[1])
			ver := m[2]
			relFile := m[3]
			if modPath != "" && ver != "" && relFile != "" {
				return modPath + "@" + ver + "/" + relFile
			}
		}
	}

	// 3. Explicit repoDir checkout prefix.
	if cleanRepo != "" && strings.HasPrefix(slashPath, cleanRepo+"/") {
		rel := strings.TrimPrefix(slashPath, cleanRepo+"/")
		if cleanMod != "" {
			return cleanMod + "/" + rel
		}
		return rel
	}

	// 4. Heuristic /.ntfuzz/repos/<project>/<rel> checkout prefix when repoDir is unknown.
	if idx := strings.Index(slashPath, "/repos/"); idx >= 0 {
		rest := slashPath[idx+len("/repos/"):]
		if repoSeg, relAfterRepo, ok := strings.Cut(rest, "/"); ok && repoSeg != "" && relAfterRepo != "" {
			if cleanMod != "" {
				modRoot := DeriveModuleRoot(repoSeg, cleanMod)
				if strings.HasSuffix(strings.ToLower(modRoot), "/"+strings.ToLower(repoSeg)) || strings.EqualFold(modRoot, repoSeg) {
					return modRoot + "/" + relAfterRepo
				}
				relDir := path.Dir(relAfterRepo)
				if relDir != "." && strings.HasSuffix(cleanMod, "/"+relDir) {
					return strings.TrimSuffix(cleanMod, "/"+relDir) + "/" + relAfterRepo
				}
			}
		}
	}

	// 5. Fallback: if the preceding function line belongs to the project module (or a subpackage)
	// and the file path is still an absolute local path, replace its directory with the function's package path.
	if strings.HasPrefix(slashPath, "/") && prevFuncPkg != "" {
		targetPkg := ""
		if cleanMod != "" && (prevFuncPkg == cleanMod || strings.HasPrefix(prevFuncPkg, cleanMod+"/")) {
			targetPkg = prevFuncPkg
		} else if cleanRepo == "" && cleanMod == "" && hasDomainPrefix(prevFuncPkg) {
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
	for _, marker := range []string{"/pkg/mod/cache/download/", "/pkg/mod/", "/go-mod/", "/mod/"} {
		if idx := strings.LastIndex("/"+raw, marker); idx >= 0 {
			candidate := ("/" + raw)[idx+len(marker):]
			if candidate != "" {
				raw = candidate
				break
			}
		}
	}
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

		// Header / metadata / testing.go log prefix lines that are not stack frames.
		if isStackMetadataLine(trimmed) {
			prevFunc = ""
			continue
		}

		if !strings.Contains(trimmed, ".go:") {
			prevFunc = trimmed
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

			if cleanMod != "" && (modPath == cleanMod || strings.HasPrefix(cleanMod, modPath+"/")) {
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

// HasProjectFrame reports whether stack contains a recognizable in-repo project frame
// (as opposed to an unattributed or empty stack) when AttributeStack returns empty.
func HasProjectFrame(stack, repoDir, projectModule string) bool {
	if stack == "" {
		return false
	}
	if attr := AttributeStack(stack, repoDir, projectModule); attr.Module != "" {
		return false
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
		if reGoTestLogLine.MatchString(trimmed) {
			prevFunc = ""
			if m := reStackFileLine.FindStringSubmatch(trimmed); m != nil {
				base := filepath.Base(filepath.ToSlash(m[1]))
				if base != "testing.go" && base != "fuzz.go" && !isSkippedRuntimeFrame("", trimmed) {
					return true
				}
			}
			continue
		}
		if isStackMetadataLine(trimmed) {
			prevFunc = ""
			continue
		}
		if !strings.Contains(trimmed, ".go:") {
			prevFunc = trimmed
			continue
		}
		fn := prevFunc
		prevFunc = ""
		if isSkippedRuntimeFrame(fn, trimmed) {
			continue
		}
		if sub := reModCacheFrame.FindStringSubmatch(trimmed); sub != nil {
			modPath := unescapeGoModulePath(stripFilesystemPrefixFromModule(sub[1]))
			return cleanMod != "" && (modPath == cleanMod || strings.HasPrefix(cleanMod, modPath+"/"))
		}
		if isProjectFrame(fn, trimmed, cleanRepo, cleanMod) {
			return true
		}
	}
	return false
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
			subDir := strings.Join(subparts, "/")
			if relPath != "" && !strings.HasPrefix(relPath, subDir+"/") {
				relPath = subDir + "/" + relPath
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

// extractFuncPackagePath extracts the Go package import path from a stack trace
// function call line (e.g. "github.com/qmuntal/gltf.(*Decoder).decodeBuffer(0x1)" -> "github.com/qmuntal/gltf").
func extractFuncPackagePath(funcLine string) string {
	line := strings.TrimSpace(funcLine)
	if line == "" || isStackMetadataLine(line) {
		return ""
	}
	if paren := strings.IndexByte(line, '('); paren >= 0 {
		if slashAfter := strings.IndexByte(line[paren:], '/'); slashAfter < 0 {
			line = line[:paren]
		} else if prevDot := strings.LastIndexByte(line[:paren], '.'); prevDot > 0 {
			line = line[:prevDot]
		}
	}
	lastSlash := strings.LastIndexByte(line, '/')
	if lastSlash >= 0 {
		rem := line[lastSlash+1:]
		if dot := strings.IndexByte(rem, '.'); dot > 0 {
			return line[:lastSlash+1+dot]
		}
		return ""
	}
	if dot := strings.IndexByte(line, '.'); dot > 0 {
		return line[:dot]
	}
	return ""
}

func isStackMetadataLine(trimmed string) bool {
	if strings.HasPrefix(trimmed, "panic:") ||
		strings.HasPrefix(trimmed, "fatal error:") ||
		strings.HasPrefix(trimmed, "goroutine ") ||
		strings.HasPrefix(trimmed, "--- FAIL:") ||
		strings.HasPrefix(trimmed, "FAIL\t") ||
		strings.HasPrefix(trimmed, "FAIL ") ||
		strings.HasPrefix(trimmed, "exit status ") ||
		strings.HasPrefix(trimmed, "created by ") {
		return true
	}
	return reGoTestLogLine.MatchString(trimmed)
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
	if fp == "testing.go" || fp == "fuzz.go" {
		return true
	}
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
	if cleanMod != "" && (pkg == cleanMod || strings.HasPrefix(pkg, cleanMod+"/") || strings.HasPrefix(cleanMod, pkg+"/")) {
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
	if strings.Contains(fp, "/repos/") && !strings.Contains(fp, "@") {
		return true
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
