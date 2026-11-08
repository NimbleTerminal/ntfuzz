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
	header := fmt.Sprintf("NTFUZZ · %d open crashes", m.status.OpenCrashes)
	if m.lastErr != nil {
		header = "NTFUZZ · API warning (r: retry)"
	}
	lines := []string{header, m.currentStatsQuery().Title, m.currentStatsWindow() + " · " + project + " · " + m.graphicsLabel()}
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

// Keep the landing screen useful on the same narrow grid as the graphs.
func (m Model) compactFleetView() tea.View {
	width := max(1, m.width)
	filter := m.currentProjectFilter()
	if filter == "" {
		filter = "all repos"
	}
	lines := []string{fmt.Sprintf("NTFUZZ · %d open crashes", m.status.OpenCrashes), "Targets · " + filter}
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

func compactChartNote(m Model) string {
	if m.currentStatsQuery().ID == api.StatsQueryBurndown && m.statsData.HistoryEstimatedBefore != nil {
		return "UTC · includes estimated history"
	}
	return "UTC · " + compactSeriesName(m.currentStatsQuery().ID)
}
