package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"nimbleterminal.dev/ntfuzz/internal/store"
)

func parseCrashIDParam(r *http.Request) (int64, error) {
	idStr := strings.TrimSpace(r.PathValue("id"))
	crashID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || crashID <= 0 {
		return 0, errors.New("invalid crash id")
	}
	return crashID, nil
}

func (s *Server) handleGetCrash(w http.ResponseWriter, r *http.Request) {
	crashID, err := parseCrashIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.store.GetCrash(r.Context(), crashID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, CrashFromStore(c))
}

func (s *Server) handleIssueCrash(w http.ResponseWriter, r *http.Request) {
	crashID, err := parseCrashIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var req IssueCrashRequest
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, errors.New("missing request body"))
		return
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON request body: %w", err))
		return
	}

	issueURL := strings.TrimSpace(req.IssueURL)
	if err := ValidateIssueURL(issueURL); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.store.SetCrashIssue(r.Context(), crashID, issueURL); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "reported",
		"id":        crashID,
		"issue_url": issueURL,
	})
}

// ValidateIssueURL validates that an issue URL is non-empty, free of control characters,
// and uses an http or https scheme.
func ValidateIssueURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("issue_url is required")
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("issue_url must not contain whitespace or control characters")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("issue_url must be a valid http or https URL")
	}
	return nil
}

func (s *Server) resolveCrashInputBytes(c store.Crash) ([]byte, bool) {
	if len(c.InputBytes) > 0 && !c.InputTruncated {
		return c.InputBytes, true
	}
	if s.snapshot == nil && c.MinimizedInput != "" && c.MinimizedInput != "inline-failure" {
		if data, err := os.ReadFile(c.MinimizedInput); err == nil && len(data) > 0 {
			return data, true
		}
	}
	if len(c.InputBytes) > 0 {
		return c.InputBytes, true
	}
	return nil, false
}

func (s *Server) canServeCrashInput(c store.Crash) bool {
	if s.snapshot == nil {
		return true
	}
	return c.PublicInputs
}

func sanitizeAttachmentFilename(hash string, crashID int64) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(hash) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return fmt.Sprintf("crash-%d", crashID)
	}
	return out
}

func (s *Server) handleCrashInput(w http.ResponseWriter, r *http.Request) {
	crashID, err := parseCrashIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.store.GetCrash(r.Context(), crashID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !s.canServeCrashInput(c) {
		writeError(w, http.StatusForbidden, errors.New("public input download is not enabled for this project"))
		return
	}
	data, ok := s.resolveCrashInputBytes(c)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("minimized input bytes not available"))
		return
	}
	filename := sanitizeAttachmentFilename(c.InputHash, c.ID)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func requestBaseURL(r *http.Request) string {
	if pub := strings.TrimSpace(r.URL.Query().Get("public_url")); pub != "" {
		return strings.TrimRight(pub, "/")
	}
	if r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) handleCrashReport(w http.ResponseWriter, r *http.Request) {
	crashID, err := parseCrashIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.store.GetCrash(r.Context(), crashID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dto := CrashFromStore(c)
	canServeInput := s.canServeCrashInput(c)
	var inputBytes []byte
	if canServeInput {
		inputBytes, _ = s.resolveCrashInputBytes(c)
	}
	md := FormatCrashReportMarkdown(dto, inputBytes, requestBaseURL(r), canServeInput)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, md)
}

// DeriveRelativePackageArg derives a relative package path (e.g. "./mesh/gltf" or ".")
// from a project name and full Go package import path.
func DeriveRelativePackageArg(projectName, pkgImport string) string {
	pkgImport = strings.Trim(strings.TrimSpace(pkgImport), "/")
	projectName = strings.Trim(strings.TrimSpace(projectName), "/")
	if pkgImport == "" {
		return "."
	}
	if projectName != "" {
		if pkgImport == projectName || strings.HasSuffix(pkgImport, "/"+projectName) {
			return "."
		}
		needle := "/" + projectName + "/"
		if idx := strings.Index(pkgImport, needle); idx >= 0 {
			rel := pkgImport[idx+len(needle):]
			if rel != "" {
				return "./" + rel
			}
		}
	}
	parts := strings.Split(pkgImport, "/")
	if len(parts) >= 4 && (parts[0] == "github.com" || parts[0] == "gitlab.com") {
		return "./" + strings.Join(parts[3:], "/")
	}
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		return "./" + strings.Join(parts[2:], "/")
	}
	return "./" + pkgImport
}

// CrashStateLabel returns a human-readable state label ("open-unreported", "open-reported", or "fixed").
func CrashStateLabel(c Crash) string {
	if c.FixedAt != nil {
		return "fixed"
	}
	if strings.TrimSpace(c.IssueURL) != "" {
		return "open-reported"
	}
	return "open-unreported"
}

// FormatCrashReportMarkdown builds a shareable Markdown crash report for upstream maintainers.
func FormatCrashReportMarkdown(c Crash, inputBytes []byte, baseURL string, allowInput bool) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	permalink := fmt.Sprintf("/crashes/%d", c.ID)
	inputURL := fmt.Sprintf("/api/crashes/%d/input", c.ID)
	if base != "" {
		permalink = base + permalink
		inputURL = base + inputURL
	}

	targetLabel := c.TargetName
	if targetLabel == "" {
		targetLabel = fmt.Sprintf("target-%d", c.TargetID)
	}
	projectLabel := c.ProjectName
	if projectLabel == "" {
		projectLabel = "project"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Crash #%d: %s %s\n\n", c.ID, projectLabel, targetLabel)

	state := CrashStateLabel(c)
	fmt.Fprintf(&sb, "- **State:** `%s`\n", state)
	if c.FixedAt != nil {
		fmt.Fprintf(&sb, "- **Fixed At:** `%s`\n", c.FixedAt.UTC().Format(time.RFC3339))
	}
	if c.IssueURL != "" {
		fmt.Fprintf(&sb, "- **Issue:** %s\n", c.IssueURL)
	}
	if c.BlamedModule != "" {
		depRef := c.BlamedModule
		if c.BlamedVersion != "" {
			depRef += "@" + c.BlamedVersion
		}
		loc := ""
		if c.BlamedFile != "" {
			if c.BlamedLine > 0 {
				loc = fmt.Sprintf(" (`%s:%d`)", c.BlamedFile, c.BlamedLine)
			} else {
				loc = fmt.Sprintf(" (`%s`)", c.BlamedFile)
			}
		}
		if c.BlamedURL != "" {
			fmt.Fprintf(&sb, "- **Blamed Dependency:** [`%s`](%s)%s\n", depRef, c.BlamedURL, loc)
		} else {
			fmt.Fprintf(&sb, "- **Blamed Dependency:** `%s`%s\n", depRef, loc)
		}
	} else {
		fmt.Fprintf(&sb, "- **Blamed Component:** `%s` (project code)\n", projectLabel)
	}

	fmt.Fprintf(&sb, "- **Project:** `%s`\n", projectLabel)
	if c.Package != "" {
		fmt.Fprintf(&sb, "- **Package:** `%s`\n", c.Package)
	}
	fmt.Fprintf(&sb, "- **Target:** `%s`\n", targetLabel)

	rev := c.GitSHA
	if rev == "" {
		rev = "unknown"
	}
	if c.Dirty {
		rev += " (dirty)"
	}
	fmt.Fprintf(&sb, "- **Project Revision:** `%s`\n", rev)
	if c.GoVersion != "" {
		fmt.Fprintf(&sb, "- **Go Version:** `%s`\n", c.GoVersion)
	}
	fmt.Fprintf(&sb, "- **First Seen:** `%s`\n", c.FirstSeen.UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "- **Last Seen:** `%s`\n", c.LastSeen.UTC().Format(time.RFC3339))
	if c.ReplayStatus != "" {
		replayLine := fmt.Sprintf("`%s`", c.ReplayStatus)
		if c.ReplayGitSHA != "" {
			replayLine += fmt.Sprintf(" on `%s`", c.ReplayGitSHA)
		}
		if c.ReplayCheckedAt != nil {
			replayLine += fmt.Sprintf(" at `%s`", c.ReplayCheckedAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(&sb, "- **Replay Status:** %s\n", replayLine)
	}
	fmt.Fprintf(&sb, "- **Permalink:** %s\n", permalink)
	if allowInput {
		fmt.Fprintf(&sb, "- **Reproducer Input URL:** %s\n", inputURL)
	}

	sb.WriteString("\n## Stack Trace\n\n```text\n")
	sb.WriteString(strings.TrimRight(c.Stack, "\n"))
	sb.WriteString("\n```\n")

	pkgArg := DeriveRelativePackageArg(c.ProjectName, c.Package)
	corpusRelDir := fmt.Sprintf("testdata/fuzz/%s/%s", targetLabel, c.InputHash)
	if pkgArg != "." && strings.HasPrefix(pkgArg, "./") {
		corpusRelDir = fmt.Sprintf("%s/testdata/fuzz/%s/%s", strings.TrimPrefix(pkgArg, "./"), targetLabel, c.InputHash)
	}
	fmt.Fprintf(&sb, "\n## Reproduction\n\nDrop the corpus file into `%s` and run:\n\n```sh\ngo test %s -run '%s/%s'\n```\n",
		corpusRelDir, pkgArg, targetLabel, c.InputHash)

	if allowInput && len(inputBytes) > 0 {
		fmt.Fprintf(&sb, "\n## Corpus File (`%s`)\n\n", c.InputHash)
		if c.InputTruncated {
			sb.WriteString("> Note: Stored input exceeded 64 KiB and was truncated.\n\n")
		}
		sb.WriteString("```text\n")
		sb.WriteString(strings.TrimRight(string(inputBytes), "\n"))
		sb.WriteString("\n```\n")
	}

	return sb.String()
}

func safeHrefURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return raw
}

func (s *Server) handleCrashHTML(w http.ResponseWriter, r *http.Request) {
	crashID, err := parseCrashIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.store.GetCrash(r.Context(), crashID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dto := CrashFromStore(c)
	canServeInput := s.canServeCrashInput(c)
	var inputBytes []byte
	var hasInput bool
	if canServeInput {
		inputBytes, hasInput = s.resolveCrashInputBytes(c)
	}

	page := renderCrashHTMLPage(dto, inputBytes, hasInput, canServeInput, s.snapshot != nil)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, page)
}

func renderCrashHTMLPage(c Crash, inputBytes []byte, hasInput, canServeInput, isReadOnly bool) string {
	esc := html.EscapeString
	project := c.ProjectName
	if project == "" {
		project = "unknown"
	}
	target := c.TargetName
	if target == "" {
		target = fmt.Sprintf("target-%d", c.TargetID)
	}
	pkg := c.Package
	if pkg == "" {
		pkg = "."
	}

	state := CrashStateLabel(c)
	badgeClass := "badge-open"
	badgeText := "OPEN · UNREPORTED"
	if c.FixedAt != nil {
		badgeClass = "badge-fixed"
		badgeText = "FIXED"
	} else if c.IssueURL != "" {
		badgeClass = "badge-reported"
		badgeText = "OPEN · REPORTED"
	}

	revText := c.GitSHA
	if revText == "" {
		revText = "unknown"
	}
	dirtyText := "false"
	if c.Dirty {
		dirtyText = "true"
		revText += " (dirty)"
	}
	goVer := c.GoVersion
	if goVer == "" {
		goVer = "unknown"
	}
	fixedAtText := "—"
	if c.FixedAt != nil {
		fixedAtText = c.FixedAt.UTC().Format(time.RFC3339)
	}

	issueHTML := `<span class="muted">unreported</span>`
	if c.IssueURL != "" {
		if safe := safeHrefURL(c.IssueURL); safe != "" {
			issueHTML = fmt.Sprintf(`<a href="%s" rel="noopener noreferrer">%s</a>`, esc(safe), esc(c.IssueURL))
		} else {
			issueHTML = fmt.Sprintf(`<code>%s</code>`, esc(c.IssueURL))
		}
	}

	blamedHTML := fmt.Sprintf(`<span class="badge badge-project">PROJECT</span> <code>%s</code> (in-repo frame)`, esc(project))
	if c.BlamedModule != "" {
		depRef := c.BlamedModule
		if c.BlamedVersion != "" {
			depRef += "@" + c.BlamedVersion
		}
		loc := c.BlamedFile
		if c.BlamedLine > 0 && loc != "" {
			loc = fmt.Sprintf("%s:%d", loc, c.BlamedLine)
		}
		if safe := safeHrefURL(c.BlamedURL); safe != "" {
			blamedHTML = fmt.Sprintf(`<span class="badge badge-dep">DEPENDENCY</span> <a href="%s" rel="noopener noreferrer"><code>%s</code></a>`, esc(safe), esc(depRef))
			if loc != "" {
				blamedHTML += fmt.Sprintf(` — <a href="%s" rel="noopener noreferrer"><code>%s</code></a>`, esc(safe), esc(loc))
			}
		} else {
			blamedHTML = fmt.Sprintf(`<span class="badge badge-dep">DEPENDENCY</span> <code>%s</code>`, esc(depRef))
			if loc != "" {
				blamedHTML += fmt.Sprintf(` (<code>%s</code>)`, esc(loc))
			}
		}
	}

	replayHTML := `<span class="muted">not replayed yet</span>`
	if c.ReplayStatus != "" {
		replayHTML = fmt.Sprintf(`<code>%s</code>`, esc(c.ReplayStatus))
		if c.ReplayGitSHA != "" {
			replayHTML += fmt.Sprintf(` on <code>%s</code>`, esc(c.ReplayGitSHA))
		}
		if c.ReplayCheckedAt != nil {
			replayHTML += fmt.Sprintf(` at <code>%s</code>`, esc(c.ReplayCheckedAt.UTC().Format(time.RFC3339)))
		}
		if c.ReplayDiagnostic != "" {
			replayHTML += fmt.Sprintf(`<div class="replay-diag"><code>%s</code></div>`, esc(c.ReplayDiagnostic))
		}
	}

	inputLinkHTML := `<span class="muted">Public input download is disabled for this project (set <code>public_inputs: true</code> in project config to expose).</span>`
	if canServeInput {
		if hasInput {
			truncNote := ""
			if c.InputTruncated {
				truncNote = ` <span class="badge badge-dep">TRUNCATED AT 64 KiB</span>`
			}
			inputLinkHTML = fmt.Sprintf(`<a class="btn btn-primary" href="/api/crashes/%d/input">Download Corpus File (%s)</a>%s`, c.ID, esc(c.InputHash), truncNote)
		} else {
			inputLinkHTML = `<span class="muted">Corpus input bytes are not stored for this crash record.</span>`
		}
	}

	pkgArg := DeriveRelativePackageArg(c.ProjectName, c.Package)
	corpusRelPath := fmt.Sprintf("testdata/fuzz/%s/%s", target, c.InputHash)
	if pkgArg != "." && strings.HasPrefix(pkgArg, "./") {
		corpusRelPath = fmt.Sprintf("%s/testdata/fuzz/%s/%s", strings.TrimPrefix(pkgArg, "./"), target, c.InputHash)
	}
	reproCmd := fmt.Sprintf("go test %s -run '%s/%s'", pkgArg, target, c.InputHash)

	corpusPreviewHTML := ""
	if canServeInput && hasInput && len(inputBytes) > 0 {
		corpusPreviewHTML = fmt.Sprintf(`
    <section class="panel">
      <h2>Corpus Input (<code>%s</code>)</h2>
      <pre><code>%s</code></pre>
    </section>`, esc(c.InputHash), esc(strings.TrimRight(string(inputBytes), "\n")))
	}

	modeLabel := "LIVE OPERATOR CONSOLE"
	if isReadOnly {
		modeLabel = "READ-ONLY SNAPSHOT VIEWER"
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <link rel="service-doc" href="/api/docs" type="text/html" />
  <link rel="service-desc" href="/openapi.json" type="application/json" />
  <link rel="describedby" href="/llms.txt" type="text/markdown" />
  <link rel="alternate" href="/api/crashes/%d" type="application/json" />
  <link rel="alternate" href="/api/crashes/%d/report.md" type="text/markdown" />
  <title>Crash #%d — %s/%s · ntfuzz</title>
  <style>
    :root {
      color-scheme: dark;
      --bg: #0d1017;
      --panel: #141923;
      --border: #232b3a;
      --text: #e5e9f0;
      --muted: #8a95a7;
      --accent: #38bdf8;
      --red: #f87171;
      --amber: #fbbf24;
      --green: #4ade80;
      --code-bg: #1a2130;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--text);
      font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      line-height: 1.55;
      padding: 28px 20px 60px;
    }
    main { max-width: 980px; margin: 0 auto; }
    header {
      border-bottom: 1px solid var(--border);
      padding-bottom: 16px;
      margin-bottom: 24px;
      display: flex;
      justify-content: space-between;
      align-items: baseline;
      flex-wrap: wrap;
      gap: 12px;
    }
    .brand { display: flex; align-items: center; gap: 10px; }
    .wordmark { font-weight: 700; letter-spacing: 0.08em; color: var(--accent); text-decoration: none; }
    .mode-pill { font-size: 0.72rem; color: var(--muted); border: 1px solid var(--border); border-radius: 4px; padding: 2px 7px; }
    nav a {
      color: var(--accent);
      text-decoration: none;
      margin-left: 14px;
      font-size: 0.9rem;
    }
    nav a:hover, a:hover { text-decoration: underline; }
    h1 { font-size: 1.45rem; margin: 0 0 10px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
    h2 { font-size: 1.05rem; margin: 0 0 12px; color: #f8fafc; }
    .panel {
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 18px 20px;
      margin-bottom: 18px;
    }
    .badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: 4px;
      font-size: 0.75rem;
      font-weight: 700;
      letter-spacing: 0.04em;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    }
    .badge-open { background: rgba(248, 113, 113, 0.16); color: var(--red); border: 1px solid rgba(248, 113, 113, 0.35); }
    .badge-reported { background: rgba(251, 191, 36, 0.16); color: var(--amber); border: 1px solid rgba(251, 191, 36, 0.35); }
    .badge-fixed { background: rgba(74, 222, 128, 0.16); color: var(--green); border: 1px solid rgba(74, 222, 128, 0.35); }
    .badge-dep { background: rgba(251, 191, 36, 0.16); color: var(--amber); border: 1px solid rgba(251, 191, 36, 0.35); }
    .badge-project { background: rgba(56, 189, 248, 0.14); color: var(--accent); border: 1px solid rgba(56, 189, 248, 0.35); }
    .actions { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 12px; align-items: center; }
    .btn {
      display: inline-block;
      padding: 6px 12px;
      border-radius: 6px;
      border: 1px solid var(--border);
      background: var(--code-bg);
      color: var(--text);
      text-decoration: none;
      font-size: 0.85rem;
      font-weight: 600;
    }
    .btn-primary { border-color: rgba(56, 189, 248, 0.5); color: var(--accent); }
    table {
      width: 100%%;
      border-collapse: collapse;
      font-size: 0.9rem;
    }
    th, td {
      text-align: left;
      padding: 8px 10px;
      border-bottom: 1px solid var(--border);
      vertical-align: top;
    }
    th { width: 180px; color: var(--muted); font-weight: 600; }
    code, pre {
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 0.85rem;
    }
    code {
      background: var(--code-bg);
      padding: 2px 6px;
      border-radius: 4px;
    }
    pre {
      background: var(--code-bg);
      border: 1px solid var(--border);
      border-radius: 6px;
      padding: 14px;
      overflow-x: auto;
      margin: 0;
    }
    pre code { background: none; padding: 0; }
    a { color: var(--accent); }
    .muted { color: var(--muted); }
    .replay-diag { margin-top: 6px; color: var(--muted); }
  </style>
</head>
<body>
  <main>
    <header>
      <div class="brand">
        <a class="wordmark" href="/">NTFUZZ</a>
        <span class="mode-pill">%s</span>
      </div>
      <nav aria-label="Crash navigation">
        <a href="/">Dashboard</a>
        <a href="/api/crashes/%d/report.md">Markdown Report</a>
        <a href="/api/crashes/%d">JSON</a>
        <a href="/api/runs?target=%d&amp;limit=5">Target Runs</a>
        <a href="/api/docs">API Docs</a>
        <a href="/llms.txt">llms.txt</a>
      </nav>
    </header>

    <section class="panel">
      <h1>
        <span>Crash #%d — %s / %s</span>
        <span class="badge %s" data-state="%s">%s</span>
      </h1>
      <p class="muted" style="margin: 0 0 12px;">
        Server-rendered crash permalink (no JavaScript required). Need interactive coverage charts or live target history?
        <a href="/">Open the ntfuzz Web Terminal Dashboard</a>.
      </p>
      <table>
        <tbody>
          <tr><th>Blamed Source</th><td>%s</td></tr>
          <tr><th>Project</th><td><code>%s</code></td></tr>
          <tr><th>Package</th><td><code>%s</code></td></tr>
          <tr><th>Target</th><td><code>%s</code> (id <code>%d</code>)</td></tr>
          <tr><th>Git Revision</th><td><code>%s</code> (dirty: <code>%s</code>)</td></tr>
          <tr><th>Go Version</th><td><code>%s</code></td></tr>
          <tr><th>First Seen</th><td><code>%s</code></td></tr>
          <tr><th>Last Seen</th><td><code>%s</code></td></tr>
          <tr><th>Fixed At</th><td><code>%s</code></td></tr>
          <tr><th>Upstream Issue</th><td>%s</td></tr>
          <tr><th>Replay Status</th><td>%s</td></tr>
          <tr><th>Stack Fingerprint</th><td><code>%s</code></td></tr>
          <tr><th>Input Hash</th><td><code>%s</code></td></tr>
        </tbody>
      </table>
      <div class="actions">
        %s
        <a class="btn" href="/api/crashes/%d/report.md">View Markdown Advisory (.md)</a>
        <a class="btn" href="/api/crashes/%d">Raw Crash JSON</a>
        <a class="btn" href="/">Open Web Terminal Dashboard</a>
      </div>
    </section>

    <section class="panel">
      <h2>Scrubbed Stack Trace</h2>
      <pre><code>%s</code></pre>
    </section>

    <section class="panel">
      <h2>Reproduction</h2>
      <p class="muted" style="margin-top: 0;">Place the downloaded corpus input in <code>%s</code> and run:</p>
      <pre><code>%s</code></pre>
    </section>
%s
  </main>
</body>
</html>
`,
		c.ID, c.ID,
		c.ID, esc(project), esc(target),
		esc(modeLabel),
		c.ID, c.ID, c.TargetID,
		c.ID, esc(project), esc(target),
		badgeClass, esc(state), esc(badgeText),
		blamedHTML,
		esc(project),
		esc(pkg),
		esc(target), c.TargetID,
		esc(revText), esc(dirtyText),
		esc(goVer),
		esc(c.FirstSeen.UTC().Format(time.RFC3339)),
		esc(c.LastSeen.UTC().Format(time.RFC3339)),
		esc(fixedAtText),
		issueHTML,
		replayHTML,
		esc(c.StackFingerprint),
		esc(c.InputHash),
		inputLinkHTML,
		c.ID, c.ID,
		esc(c.Stack),
		esc(corpusRelPath),
		esc(reproCmd),
		corpusPreviewHTML,
	)
}
