package api

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

func (s *Server) handleCrashIndexHTML(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListCrashes(r.Context(), true, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	crashes := make([]Crash, 0, len(rows))
	for _, row := range rows {
		crashes = append(crashes, CrashFromStore(row))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(renderCrashIndexHTML(crashes, s.snapshot != nil)))
}

func renderCrashIndexHTML(crashes []Crash, readOnly bool) string {
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Open crashes · ntfuzz</title>
  <style>
    :root { color-scheme: dark; --bg: #0d1017; --panel: #141923; --border: #334155; --text: #e5e9f0; --muted: #a6b0c1; --accent: #7dd3fc; --red: #f87171; --amber: #fbbf24; }
    * { box-sizing: border-box; }
    body { margin: 0; padding: 24px 16px 48px; background: var(--bg); color: var(--text); font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; line-height: 1.55; }
    main { max-width: 980px; margin: 0 auto; }
    header { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; margin-bottom: 24px; }
    a { color: var(--accent); }
    a:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
    .wordmark { color: var(--accent); font-weight: 700; letter-spacing: .08em; text-decoration: none; }
    nav { display: flex; flex-wrap: wrap; gap: 4px 16px; }
    nav a { display: inline-flex; align-items: center; min-height: 44px; }
    h1 { margin: 0; font-size: 1.65rem; }
    h2 { margin: 0 0 8px; font-size: 1.15rem; overflow-wrap: anywhere; }
    p { margin: 8px 0; }
    .muted { color: var(--muted); }
    .crash-list { display: grid; gap: 14px; padding: 0; margin: 20px 0 0; list-style: none; }
    .crash-card { min-width: 0; padding: 18px; background: var(--panel); border: 1px solid var(--border); border-radius: 8px; }
    .crash-title { color: var(--text); text-decoration: none; }
    .crash-title:hover { color: var(--accent); }
    .badge { display: inline-block; padding: 2px 8px; border: 1px solid currentColor; border-radius: 4px; color: var(--red); font-size: .75rem; font-weight: 700; }
    .badge.reported { color: var(--amber); }
    .summary, .blame { overflow-wrap: anywhere; }
    .summary { color: var(--muted); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .9rem; white-space: pre-wrap; }
    .button { display: inline-flex; align-items: center; min-height: 44px; margin-top: 10px; padding: 6px 12px; border: 1px solid var(--accent); border-radius: 6px; color: var(--accent); text-decoration: none; font-weight: 600; }
    .empty { padding: 24px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel); }
    @media (max-width: 480px) {
      body { padding: 14px 10px 36px; }
      header { margin-bottom: 16px; }
      .crash-card { padding: 14px; }
    }
  </style>
</head>
<body>
<main>
  <header>
    <a class="wordmark" href="/">NTFUZZ</a>
    <nav aria-label="Crash navigation">
      <a href="/">Dashboard</a>
      <a href="/api/crashes?open=true">Open crashes JSON</a>
      <a href="/api/docs">API documentation</a>
    </nav>
  </header>
  <h1>Open crashes</h1>
  <p class="muted">`)
	fmt.Fprintf(&b, "%d open %s", len(crashes), crashNoun(len(crashes)))
	if readOnly {
		b.WriteString(` · Read-only snapshot`)
	}
	b.WriteString(`.</p>`)
	if len(crashes) == 0 {
		b.WriteString(`<p class="empty">No open crashes in this data. This does not prove that every target is bug-free.</p>`)
	} else {
		b.WriteString(`<ul class="crash-list">`)
		for _, c := range crashes {
			state := "Unreported"
			stateClass := ""
			if strings.TrimSpace(c.IssueURL) != "" {
				state = "Reported"
				stateClass = " reported"
			}
			b.WriteString(`<li><article class="crash-card">`)
			fmt.Fprintf(&b, `<h2><a class="crash-title" href="/crashes/%d">Crash #%d — %s / %s</a></h2>`, c.ID, c.ID, html.EscapeString(c.ProjectName), html.EscapeString(c.TargetName))
			fmt.Fprintf(&b, `<span class="badge%s">%s</span>`, stateClass, state)
			if c.BlamedModule != "" {
				fmt.Fprintf(&b, `<p class="blame"><strong>Blamed source:</strong> %s`, html.EscapeString(c.BlamedModule))
				if c.BlamedVersion != "" {
					fmt.Fprintf(&b, `@%s`, html.EscapeString(c.BlamedVersion))
				}
				if c.BlamedFile != "" {
					fmt.Fprintf(&b, ` · %s`, html.EscapeString(c.BlamedFile))
					if c.BlamedLine > 0 {
						fmt.Fprintf(&b, `:%d`, c.BlamedLine)
					}
				}
				b.WriteString(`</p>`)
			}
			if summary := crashIndexSummary(c.Stack); summary != "" {
				fmt.Fprintf(&b, `<p class="summary">%s</p>`, html.EscapeString(summary))
			}
			if !c.LastSeen.IsZero() {
				fmt.Fprintf(&b, `<p class="muted">Last seen <time datetime="%s">%s</time></p>`, html.EscapeString(c.LastSeen.UTC().Format(timeRFC3339)), html.EscapeString(c.LastSeen.UTC().Format("2006-01-02 15:04:05 UTC")))
			}
			fmt.Fprintf(&b, `<a class="button" href="/crashes/%d">Open crash report</a>`, c.ID)
			b.WriteString(`</article></li>`)
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(`
</main>
</body>
</html>`)
	return b.String()
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func crashNoun(count int) string {
	if count == 1 {
		return "crash"
	}
	return "crashes"
}

func crashIndexSummary(stack string) string {
	lines := strings.Split(stack, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "panic:") || strings.Contains(trimmed, "fatal error:") {
			return truncateCrashIndexSummary(trimmed)
		}
	}
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateCrashIndexSummary(trimmed)
		}
	}
	return ""
}

func truncateCrashIndexSummary(summary string) string {
	const limit = 240
	runes := []rune(summary)
	if len(runes) <= limit {
		return summary
	}
	return string(runes[:limit-1]) + "…"
}
