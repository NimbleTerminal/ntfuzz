package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/NimbleMarkets/ntcharts/v2/linechart/timeserieslinechart"
	"github.com/charmbracelet/x/ansi"
	"nimbleterminal.dev/ntfuzz/internal/api"
)

// spaceBudget is deliberately internal until a second application proves the API.
// Width selects presentation; height controls how much content fits in it.
type spaceBudget struct{ Width, Rows int }

func (s spaceBudget) chart(reserve int) int {
	if s.Rows-reserve < 5 {
		return 0
	}
	return min(16, s.Rows-reserve)
}
func (m Model) compactStats() bool         { return m.mode == ViewStats && (m.width < 80 || m.height < 24) }
func (m Model) compactBudget() spaceBudget { return spaceBudget{max(1, m.width), max(0, m.height-5)} }
func (m Model) compactChartHeight() int {
	if m.currentStatsQuery().ID == api.StatsQueryRevisions {
		return 0
	}
	return m.compactBudget().chart(4)
}

// recordPages wraps first, then packs complete records. Oversized records are
// split into continuations so even keyboard-sized viewports can read every line.
func recordPages(records []string, width, rows int) []string {
	rows = max(1, rows)
	width = max(1, width)
	var pages []string
	var lines []string
	flush := func() {
		if len(lines) > 0 {
			pages = append(pages, strings.Join(lines, "\n"))
			lines = nil
		}
	}
	for _, record := range records {
		next := strings.Split(ansi.Wrap(record, width, ""), "\n")
		if len(lines)+len(next) > rows {
			flush()
		}
		for _, line := range next {
			if len(lines) == rows {
				flush()
			}
			lines = append(lines, line)
		}
	}
	flush()
	if len(pages) == 0 {
		return []string{"No observations in this window."}
	}
	return pages
}

func (m Model) compactStatsView() tea.View {
	budget := m.compactBudget()
	project := m.currentProjectFilter()
	if project == "" {
		project = "all repos"
	}
	header := "NTFUZZ · " + openCrashSummary(m.status.OpenCrashes)
	if m.lastErr != nil {
		header = "NTFUZZ · API warning (r: retry)"
	}
	lines := []string{header}
	if status := m.compactDataStatus(); status != "" {
		lines = append(lines, status)
	}
	lines = append(lines, m.currentStatsQuery().Title, m.currentStatsWindow()+" · "+project+" · "+m.graphicsLabel())
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], budget.Width, "…")
	}
	var body string
	if m.currentStatsQuery().ID == api.StatsQueryRevisions {

		pages := m.compactRevisionPages()
		page := max(0, min(m.revisionPage, len(pages)-1))
		body = pages[page] + "\n" + ansi.Truncate(fmt.Sprintf("Page %d/%d · [ / ]", page+1, len(pages)), budget.Width, "")
	} else {
		s := m.statsData
		summary := fmt.Sprintf("%d runs · %s", s.TotalRuns, FormatExecRate(s.AvgExecsPerSec))
		switch m.currentStatsQuery().ID {
		case api.StatsQueryBurndown:
			openUnreported, openReported := m.openCrashSplit(m.currentProjectFilter(), s.OpenCrashes)
			summary = fmt.Sprintf("Open %d (%du/%dr) · New %d\nReopened %d · Resolved %d", s.OpenCrashes, openUnreported, openReported, s.NewCrashes, s.ReopenedCrashes, s.ResolvedCrashes)
		case api.StatsQueryCoverage:
			summary = fmt.Sprintf("Coverage %.1f%%\nCorpus %d · New inputs %d", s.AvgCoveragePct, s.CorpusCount, s.NewInteresting)
		case api.StatsQueryOutcomes:
			summary = fmt.Sprintf("OK %d · Crash %d\nError/incomplete %d · Canceled %d", s.OKRuns, s.CrashRuns, s.ErrorRuns+s.IncompleteRuns, s.CanceledRuns)
		}
		summary = ansi.Hardwrap(summary, budget.Width, true)
		// Reserve actual wrapped summary rows, plus a UTC note. Both renderers use
		// the same fixed budget; unusually large counters suppress the chart.
		h := m.compactChartHeight()
		if len(strings.Split(summary, "\n"))+h+1 > budget.Rows {
			h = 0
		}
		body = summary
		if len(s.Buckets) == 0 {
			body += "\nNo history in this window."
		}
		if h > 0 && len(s.Buckets) > 0 {
			points := make([]timeserieslinechart.TimePoint, 0, len(s.Buckets))
			for _, b := range s.Buckets {
				value := b.AvgExecsPerSec
				switch m.currentStatsQuery().ID {
				case api.StatsQueryBurndown:
					value = float64(b.OpenCrashes)
				case api.StatsQueryCoverage:
					value = b.AvgCoveragePct
				case api.StatsQueryOutcomes:
					value = float64(b.CrashRuns)
				}
				points = append(points, timeserieslinechart.TimePoint{Time: b.BucketStart, Value: value})
			}
			chart := renderDualTimeSeriesChart(budget.Width, h, points, nil, statusCrashStyle, statusOKStyle, func(_ int, v float64) string { return formatCompactNumber(v) }, func(_ int, v float64) string { return formatCompactNumber(v) }, 0, 0)
			body += "\n" + m.graphOrText(chart) + "\n" + ansi.Truncate(compactChartNote(m), budget.Width, "")
		}
	}
	lines = append(lines, body)
	lines = append(lines, ansi.Truncate("↑↓ query · w time · p repo · g fleet", budget.Width, "…"))
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
func compactSeriesName(query string) string {
	switch query {
	case api.StatsQueryBurndown:
		return "Open crash backlog"
	case api.StatsQueryCoverage:
		return "Coverage %"
	case api.StatsQueryOutcomes:
		return "Crash runs"
	default:
		return "Execs/sec"
	}
}

func (m Model) compactRevisionPages() []string {
	budget := m.compactBudget()
	var records []string
	for _, r := range m.statsData.Revisions {
		kind := "update"
		if r.Baseline {
			kind = "baseline"
		}
		if r.Current {
			kind += " current"
		}
		sha := r.GitSHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		sync := "never"
		if r.LastSynced != nil {
			sync = r.LastSynced.UTC().Format("01-02 15:04")
		}
		record := fmt.Sprintf("%s @ %s (%s)\nDetected %s UTC\nSync %s UTC\nRuns: %d OK / %d crash / %d error\nReplay: %d pass / %d fail\nReplay: %d blocked / %d pending", r.Project, sha, kind, r.DetectedAt.UTC().Format("01-02 15:04"), sync, r.OKRuns, r.CrashRuns, r.ErrorRuns, r.ReplayPassed, r.ReplayFailed, r.ReplayBlocked, r.ReplayPending)
		record += fmt.Sprintf("\n%d targets tested", r.TestedTargets)
		if r.SyncError != "" {
			record += "\nSync error: " + api.RedactSyncError(r.SyncError)
		}
		records = append(records, record)
	}
	return recordPages(records, budget.Width, max(1, budget.Rows-1))
}

func openCrashSummary(count int) string {
	noun := "crashes"
	if count == 1 {
		noun = "crash"
	}
	return fmt.Sprintf("%d open %s", count, noun)
}

func (m Model) compactDataStatus() string {
	if !m.readOnly && !m.status.ReadOnly {
		return ""
	}
	status := "Snapshot · read-only"
	if m.status.SnapshotAt != nil {
		status += " · " + formatRelativeTime(*m.status.SnapshotAt)
	}
	return status
}

// Keep the landing screen useful on the same narrow grid as the graphs.
func (m Model) compactFleetView() tea.View {
	width := max(1, m.width)
	filter := m.currentProjectFilter()
	if filter == "" {
		filter = "all repos"
	}
	lines := []string{"NTFUZZ · " + openCrashSummary(m.status.OpenCrashes)}
	if status := m.compactDataStatus(); status != "" {
		lines = append(lines, status)
	}
	lines = append(lines, "Targets · "+filter)
	if m.lastErr != nil {
		lines = append(lines, "API warning · r to retry")
	}
	for _, p := range m.projects {
		if p.SyncError != "" {
			lines = append(lines, "Sync warning: "+p.Name)
		}
	}
	maxWarnings := max(2, min(4, m.height-5))
	if len(lines) > maxWarnings {
		lines = append(lines[:maxWarnings-1], "More sync warnings · see Revisions")
	}
	rows := max(1, (m.height-len(lines)-2)/2)
	start := max(0, m.selectedIdx-rows+1)
	for i := start; i < min(len(m.targets), start+rows); i++ {
		target := m.targets[i]
		cursor := "  "
		if i == m.selectedIdx {
			cursor = "› "
		}
		lines = append(lines, cursor+target.ProjectName+" / "+target.Name, fmt.Sprintf("  %s · %d crashes · %.1f%% cov", target.Status, target.OpenCrashes, target.CoveragePct))
	}
	if len(m.targets) == 0 {
		lines = append(lines, "No targets in this filter.")
	}
	lines = append(lines, "↑↓ target · enter detail · g graphs")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

func (m Model) compactCrashes() bool {
	return m.mode == ViewCrashes && (m.width < 80 || m.height < 24)
}

func (m Model) compactDetail() bool {
	return m.mode == ViewDetail && (m.width < 80 || m.height < 24)
}

func (m Model) compactCrashPages() []string {
	budget := spaceBudget{Width: max(1, m.width), Rows: max(1, m.height)}
	if len(m.openCrashes) == 0 {
		return recordPages([]string{"NTFUZZ · 0 open crashes\nNo open crashes in this data."}, budget.Width, max(1, budget.Rows-1))
	}
	m.crashIdx = max(0, min(m.crashIdx, len(m.openCrashes)-1))
	c := m.openCrashes[m.crashIdx]
	state := "UNREPORTED"
	if strings.TrimSpace(c.IssueURL) != "" {
		state = "REPORTED"
	}
	first := fmt.Sprintf("NTFUZZ · %s\nCrash %d/%d · #%d · %s\n%s / %s",
		openCrashSummary(len(m.openCrashes)), m.crashIdx+1, len(m.openCrashes), c.ID, state, c.ProjectName, c.TargetName)
	if status := m.compactDataStatus(); status != "" {
		first += "\n" + status
	}
	records := []string{first}
	if c.BlamedModule != "" {
		blame := "Blame: " + c.BlamedModule
		if c.BlamedVersion != "" {
			blame += "@" + c.BlamedVersion
		}
		if c.BlamedFile != "" {
			blame += " · " + c.BlamedFile
			if c.BlamedLine > 0 {
				blame += fmt.Sprintf(":%d", c.BlamedLine)
			}
		}
		records = append(records, blame)
	}
	if !c.LastSeen.IsZero() {
		records = append(records, "Last seen: "+c.LastSeen.UTC().Format("2006-01-02 15:04 UTC"))
	}
	records = append(records, fmt.Sprintf("Report: /crashes/%d\nStack: %s", c.ID, crashSummaryLine(c.Stack)))
	return recordPages(records, budget.Width, max(1, budget.Rows-2))
}

func (m Model) compactCrashesView() tea.View {
	width := max(1, m.width)
	pages := m.compactCrashPages()
	page := max(0, min(m.crashPage, len(pages)-1))
	lines := []string{pages[page]}
	if len(m.openCrashes) == 0 {
		lines = append(lines, ansi.Truncate("c fleet · g graphs", width, "…"))
	} else {
		lines = append(lines,
			ansi.Truncate(fmt.Sprintf("Page %d/%d · [/] pages", page+1, len(pages)), width, "…"),
			ansi.Truncate("↑↓ crash · enter detail · c fleet", width, "…"),
		)
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

func (m Model) compactDetailPages() []string {
	budget := spaceBudget{Width: max(1, m.width), Rows: max(1, m.height)}
	header := "NTFUZZ · Target detail"
	if status := m.compactDataStatus(); status != "" {
		header += "\n" + status
	}

	selected := -1
	for i, c := range m.detailCrashes {
		if m.selectedCrashID != 0 && c.ID == m.selectedCrashID {
			selected = i
			break
		}
	}

	var primary string
	if selected >= 0 {
		c := m.detailCrashes[selected]
		state := "UNREPORTED"
		if strings.TrimSpace(c.IssueURL) != "" {
			state = "REPORTED"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Crash #%d · %s\n%s / %s\nReport: /crashes/%d\nMarkdown: /api/crashes/%d/report.md", c.ID, state, c.ProjectName, c.TargetName, c.ID, c.ID)
		if c.BlamedModule != "" {
			blame := c.BlamedModule
			if c.BlamedVersion != "" {
				blame += "@" + c.BlamedVersion
			}
			if c.BlamedFile != "" {
				blame += " · " + c.BlamedFile
				if c.BlamedLine > 0 {
					blame += fmt.Sprintf(":%d", c.BlamedLine)
				}
			}
			fmt.Fprintf(&b, "\nBlame: %s", blame)
		}
		if !c.LastSeen.IsZero() {
			fmt.Fprintf(&b, "\nLast seen: %s", c.LastSeen.UTC().Format("2006-01-02 15:04 UTC"))
		}
		fmt.Fprintf(&b, "\nInput: %s\nFingerprint: %s\nRevision: %s", nonEmpty(c.InputHash, "n/a"), nonEmpty(c.StackFingerprint, "n/a"), formatRevision(c.GitSHA, c.Dirty))
		if strings.TrimSpace(c.IssueURL) != "" {
			fmt.Fprintf(&b, "\nIssue: %s", c.IssueURL)
		}
		if stack := strings.TrimSpace(c.Stack); stack != "" {
			fmt.Fprintf(&b, "\nStack:\n%s", stack)
		}
		primary = b.String()
	} else if m.detailTarget != nil {
		primary = compactTargetSummary(*m.detailTarget)
	} else {
		primary = "No target selected."
	}
	records := []string{header + "\n" + primary}

	if selected >= 0 && m.detailTarget != nil {
		records = append(records, compactTargetSummary(*m.detailTarget))
	}
	if len(m.detailHistory) == 0 {
		records = append(records, "Coverage history: no runs recorded.")
	} else {
		first := m.detailHistory[0]
		last := m.detailHistory[len(m.detailHistory)-1]
		records = append(records, fmt.Sprintf("Coverage history · %d runs\nCoverage: %.1f%% → %.1f%%\nCorpus: %d → %d", len(m.detailHistory), first.CoveragePct, last.CoveragePct, first.CorpusCount, last.CorpusCount))
	}

	records = append(records, fmt.Sprintf("Recent runs (%d)", len(m.detailRuns)))
	for i, r := range m.detailRuns {
		if i == 5 {
			records = append(records, fmt.Sprintf("%d more runs: /api/runs?target=%d", len(m.detailRuns)-i, r.TargetID))
			break
		}
		cursor := "  "
		if i == m.runsTable.Cursor() {
			cursor = "› "
		}
		run := fmt.Sprintf("%sRun #%d · %s\n%s · %.1fs · %s\n%d execs · %.1f%% cov · corpus %d (+%d)\nRevision: %s · Worker: %s",
			cursor, r.ID, strings.ToUpper(r.Status), r.Started.UTC().Format("2006-01-02 15:04 UTC"), r.DurationSec, FormatExecRate(r.ExecsPerSec), r.Execs, r.CoveragePct, r.CorpusCount, r.NewInteresting, formatRevision(r.GitSHA, r.Dirty), nonEmpty(r.Worker, "n/a"))
		if strings.TrimSpace(r.Diagnostic) != "" {
			run += "\nDiagnostic:\n" + r.Diagnostic
		}
		records = append(records, run)
	}
	for i, c := range m.detailCrashes {
		if i == selected {
			continue
		}
		records = append(records, fmt.Sprintf("Crash #%d\nReport: /crashes/%d\nStack: %s", c.ID, c.ID, crashSummaryLine(c.Stack)))
	}
	return recordPages(records, budget.Width, max(1, budget.Rows-2))
}

func compactTargetSummary(t api.Target) string {
	return fmt.Sprintf("Target: %s\nProject: %s\nPackage: %s\nStatus: %s · %.1f%% coverage\nRate: %s · Corpus: %d · Runs: %d\nRevision: %s · Go: %s",
		t.Name, t.ProjectName, nonEmpty(t.Package, "n/a"), strings.ToUpper(t.Status), t.CoveragePct, FormatExecRate(t.ExecsPerSec), t.CorpusCount, t.TotalRuns, formatRevision(t.GitSHA, t.Dirty), nonEmpty(t.GoVersion, "n/a"))
}

func (m Model) compactDetailView() tea.View {
	width := max(1, m.width)
	pages := m.compactDetailPages()
	page := max(0, min(m.detailPage, len(pages)-1))
	lines := []string{
		pages[page],
		ansi.Truncate(fmt.Sprintf("Page %d/%d · [/] pages", page+1, len(pages)), width, "…"),
		ansi.Truncate("↑↓ run · esc back · c crashes", width, "…"),
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

func compactChartNote(m Model) string {
	if m.currentStatsQuery().ID == api.StatsQueryBurndown && m.statsData.HistoryEstimatedBefore != nil {
		return "UTC · includes estimated history"
	}
	return "UTC · " + compactSeriesName(m.currentStatsQuery().ID)
}
