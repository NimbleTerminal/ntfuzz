package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/barchart"
	"github.com/NimbleMarkets/ntcharts/v2/linechart"
	"github.com/NimbleMarkets/ntcharts/v2/linechart/timeserieslinechart"
	"github.com/charmbracelet/x/ansi"

	"nimbleterminal.dev/ntfuzz/internal/api"
)

// CannedQuery describes a pre-configured time-series analytical query rendered with ntcharts.
type CannedQuery struct {
	ID       string
	Title    string
	Subtitle string
}

// CannedQueries lists the built-in stats queries available in the TUI Graphs view.
var CannedQueries = []CannedQuery{
	{
		ID:       api.StatsQueryThroughput,
		Title:    "Throughput Over Time",
		Subtitle: "Avg execution rate (execs/sec) and total executions per time bucket",
	},
	{
		ID:       api.StatsQueryCoverage,
		Title:    "Coverage & Corpus Growth",
		Subtitle: "Statement coverage (%) and corpus size progression over time",
	},
	{
		ID:       api.StatsQueryOutcomes,
		Title:    "Run Outcomes Over Time",
		Subtitle: "Completed runs per time bucket by outcome (OK, Crash, Error/Incomplete, Canceled)",
	},
	{ID: api.StatsQueryRevisions, Title: "Revisions & Recovery", Subtitle: "Observed revisions, fuzz progress, and saved-crash verification"},
	{ID: api.StatsQueryBurndown, Title: "Crash Burndown", Subtitle: "Unresolved crashes and lifecycle changes over time"},
}

// StatsWindows lists the supported lookback time windows in cycling order.
var StatsWindows = []string{
	api.StatsWindow1h,
	api.StatsWindow6h,
	api.StatsWindow24h,
	api.StatsWindow7d,
	api.StatsWindow14d,
}

func (m Model) currentStatsQuery() CannedQuery {
	idx := m.statsQueryIdx
	if idx < 0 || idx >= len(CannedQueries) {
		idx = 0
	}
	return CannedQueries[idx]
}

func (m Model) currentStatsWindow() string {
	idx := m.statsWindowIdx
	if idx < 0 || idx >= len(StatsWindows) {
		return api.StatsWindow24h
	}
	return StatsWindows[idx]
}

func (m Model) renderStatsView() string {
	var b strings.Builder
	contentWidth := max(1, m.width-6)

	// 1. Canned query tabs (↑/k, ↓/j to cycle)
	qTabs := make([]string, 0, len(CannedQueries))
	for i, q := range CannedQueries {
		lbl := fmt.Sprintf("%d:%s", i+1, q.Title)
		if i == m.statsQueryIdx {
			qTabs = append(qTabs, titleStyle.Render(lbl))
		} else {
			qTabs = append(qTabs, subtleTextStyle.Render("["+lbl+"]"))
		}
	}
	queryHeader := "Query (↑/↓): " + strings.Join(qTabs, "  ")
	if lipgloss.Width(queryHeader) > contentWidth {
		active := m.currentStatsQuery()
		queryHeader = fmt.Sprintf("Query (%d/%d): %s  (↑/↓ to cycle)", m.statsQueryIdx+1, len(CannedQueries), titleStyle.Render(active.Title))
	}
	b.WriteString(ansi.Truncate(queryHeader, contentWidth, "…") + "\n")

	// 2. Time window & project filter bar
	wTabs := make([]string, 0, len(StatsWindows))
	activeWin := m.currentStatsWindow()
	for _, w := range StatsWindows {
		if w == activeWin {
			wTabs = append(wTabs, cardValueStyle.Render("["+w+"]"))
		} else {
			wTabs = append(wTabs, subtleTextStyle.Render(w))
		}
	}
	projLabel := "All Projects"
	if pf := m.currentProjectFilter(); pf != "" {
		projLabel = pf
	}
	controls := fmt.Sprintf("Window (w): %s   •   Project (p/tab): %s",
		strings.Join(wTabs, " "),
		cardValueStyle.Render(projLabel),
	)
	controls = "Render: " + m.graphicsLabel() + " • " + controls
	b.WriteString(ansi.Truncate(controls, contentWidth, "…") + "\n")

	activeQ := m.currentStatsQuery()
	b.WriteString(headerRowStyle.Render(ansi.Truncate(activeQ.Subtitle, contentWidth, "…")) + "\n")

	// Compute available height for the chart canvas
	reserved := lipgloss.Height(m.renderHeader()) + lipgloss.Height(m.renderOverviewCards()) + 2
	if !m.hideHelp {
		reserved += 1 + lipgloss.Height(m.help.View(m.keyMap()))
	}
	if m.lastErr != nil {
		reserved++
	}
	for _, p := range m.projects {
		if p.SyncError != "" {
			reserved++
		}
	}
	// Panel borders (2) + 3 header lines + 2 summary/legend lines
	chartH := max(5, min(18, m.height-reserved-9))
	chartW := max(12, contentWidth)

	stats := m.statsData
	if activeQ.ID == api.StatsQueryRevisions {
		// Measure the actual surrounding content instead of reserving estimated
		// blank rows. Give the revision table every remaining terminal row.
		chrome := []string{m.renderHeader(), m.renderOverviewCards()}
		if m.lastErr != nil {
			chrome = append(chrome, errorBannerStyle.Render(fmt.Sprintf("  ⚠ API warning: %v", m.lastErr)))
		}
		for _, p := range m.projects {
			if p.SyncError != "" {
				chrome = append(chrome, errorBannerStyle.Render(fmt.Sprintf("  ⚠ Project %s sync error: %s", p.Name, api.RedactSyncError(p.SyncError))))
			}
		}
		if !m.hideHelp {
			chrome = append(chrome, m.help.View(m.keyMap()))
		}
		used := panelStyle.GetVerticalFrameSize() + lipgloss.Height(strings.TrimSuffix(b.String(), "\n"))
		for _, block := range chrome {
			used += lipgloss.Height(block)
		}
		b.WriteString(m.renderRevisionStats(contentWidth, max(8, m.height-used)))
		return panelStyle.Width(max(1, m.width-2)).Render(b.String())
	}
	if len(stats.Buckets) == 0 {
		msg := "No fuzz run history in the selected window (" + activeWin + ")."
		if m.loading {
			msg = "Loading canned query statistics from API..."
		}
		b.WriteString("\n" + subtleTextStyle.Render("  "+ansi.Truncate(msg, contentWidth, "…")) + "\n")
		return panelStyle.Width(max(1, m.width-2)).Render(b.String())
	}

	switch activeQ.ID {
	case api.StatsQueryBurndown:
		openUnreported, openReported := m.openCrashSplit(stats.OpenCrashes)
		b.WriteString(ansi.Truncate(fmt.Sprintf("Open: %d (%d unreported, %d reported) • New: %d • Reopened: %d • Resolved: %d", stats.OpenCrashes, openUnreported, openReported, stats.NewCrashes, stats.ReopenedCrashes, stats.ResolvedCrashes), contentWidth, "…") + "\n")
		points := make([]timeserieslinechart.TimePoint, 0, len(stats.Buckets))
		for _, bk := range stats.Buckets {
			points = append(points, timeserieslinechart.TimePoint{Time: bk.BucketStart, Value: float64(bk.OpenCrashes)})
		}
		chartStr := renderDualTimeSeriesChart(chartW, chartH, points, nil, statusCrashStyle, statusOKStyle, func(_ int, v float64) string { return fmt.Sprintf("%.0f", v) }, func(_ int, v float64) string { return fmt.Sprintf("%.0f", v) }, 0, 0)
		b.WriteString(m.graphOrText(chartStr) + "\n")
		note := "Open crash backlog at the end of each bucket (UTC)"
		if stats.HistoryEstimatedBefore != nil {
			note += " • before " + stats.HistoryEstimatedBefore.UTC().Format("01-02 15:04") + " reconstructed"
		}
		b.WriteString(ansi.Truncate(note, contentWidth, "…"))

	case api.StatsQueryCoverage:
		summary := fmt.Sprintf("Avg Coverage: %.1f%%  •  Max Coverage: %.1f%%  •  Max Corpus: %d  •  +New Inputs: +%d  •  Buckets: %d",
			stats.AvgCoveragePct, stats.MaxCoveragePct, stats.CorpusCount, stats.NewInteresting, len(stats.Buckets))
		b.WriteString(ansi.Truncate(summary, contentWidth, "…") + "\n")

		leftPts := make([]timeserieslinechart.TimePoint, 0, len(stats.Buckets))
		rightPts := make([]timeserieslinechart.TimePoint, 0, len(stats.Buckets))
		for _, bk := range stats.Buckets {
			leftPts = append(leftPts, timeserieslinechart.TimePoint{Time: bk.BucketStart, Value: bk.AvgCoveragePct})
			rightPts = append(rightPts, timeserieslinechart.TimePoint{Time: bk.BucketStart, Value: float64(bk.CorpusCount)})
		}
		chartStr := renderDualTimeSeriesChart(
			chartW, chartH,
			leftPts, rightPts,
			lipgloss.NewStyle().Foreground(colorGreen),
			lipgloss.NewStyle().Foreground(colorAccent),
			func(_ int, v float64) string { return fmt.Sprintf("%.0f%%", v) },
			func(_ int, v float64) string { return formatCompactNumber(v) },
			0, 100,
		)
		b.WriteString(m.graphOrText(chartStr) + "\n")
		legend := statusOKStyle.Render("● Avg Coverage % (left axis)") + "   " +
			statusRunningStyle.Render("● Corpus Count (right axis)") + "   " +
			subtleTextStyle.Render(fmt.Sprintf("(%s → %s UTC)",
				stats.Buckets[0].BucketStart.UTC().Format("01-02 15:04"),
				stats.Buckets[len(stats.Buckets)-1].BucketStart.UTC().Format("01-02 15:04")))
		b.WriteString(ansi.Truncate(legend, contentWidth, "…"))

	case api.StatsQueryOutcomes:
		summary := fmt.Sprintf("Total Runs: %d  •  OK: %d  •  Crash: %d  •  Error/Incmp: %d  •  Canceled: %d  •  Buckets: %d",
			stats.TotalRuns, stats.OKRuns, stats.CrashRuns, stats.ErrorRuns+stats.IncompleteRuns, stats.CanceledRuns, len(stats.Buckets))
		b.WriteString(ansi.Truncate(summary, contentWidth, "…") + "\n")

		chartStr := renderOutcomesBarChart(chartW, chartH, stats.Buckets, activeWin)
		b.WriteString(m.graphOrText(chartStr) + "\n")
		legend := statusOKStyle.Render("■ OK") + "   " +
			statusCrashStyle.Render("■ Crash") + "   " +
			statusWarnStyle.Render("■ Error/Incomplete") + "   " +
			statusPendingStyle.Render("■ Canceled") + "   " +
			subtleTextStyle.Render(fmt.Sprintf("(%s → %s UTC)",
				stats.Buckets[0].BucketStart.UTC().Format("01-02 15:04"),
				stats.Buckets[len(stats.Buckets)-1].BucketStart.UTC().Format("01-02 15:04")))
		b.WriteString(ansi.Truncate(legend, contentWidth, "…"))

	default: // api.StatsQueryThroughput
		summary := fmt.Sprintf("Total Execs: %s  •  Avg Rate: %s  •  Peak Rate: %s  •  Runs: %d  •  Buckets: %d",
			formatCompactNumber(float64(stats.TotalExecs)),
			FormatExecRate(stats.AvgExecsPerSec),
			FormatExecRate(stats.PeakExecsSec),
			stats.TotalRuns,
			len(stats.Buckets),
		)
		b.WriteString(ansi.Truncate(summary, contentWidth, "…") + "\n")

		leftPts := make([]timeserieslinechart.TimePoint, 0, len(stats.Buckets))
		rightPts := make([]timeserieslinechart.TimePoint, 0, len(stats.Buckets))
		for _, bk := range stats.Buckets {
			leftPts = append(leftPts, timeserieslinechart.TimePoint{Time: bk.BucketStart, Value: bk.AvgExecsPerSec})
			rightPts = append(rightPts, timeserieslinechart.TimePoint{Time: bk.BucketStart, Value: float64(bk.Execs)})
		}
		chartStr := renderDualTimeSeriesChart(
			chartW, chartH,
			leftPts, rightPts,
			lipgloss.NewStyle().Foreground(colorAccent),
			lipgloss.NewStyle().Foreground(colorGreen),
			func(_ int, v float64) string { return FormatExecRate(v) },
			func(_ int, v float64) string { return formatCompactNumber(v) },
			0, 0,
		)
		b.WriteString(m.graphOrText(chartStr) + "\n")
		legend := statusRunningStyle.Render("● Avg Execs/sec (left axis)") + "   " +
			statusOKStyle.Render("● Execs per Bucket (right axis)") + "   " +
			subtleTextStyle.Render(fmt.Sprintf("(%s → %s UTC)",
				stats.Buckets[0].BucketStart.UTC().Format("01-02 15:04"),
				stats.Buckets[len(stats.Buckets)-1].BucketStart.UTC().Format("01-02 15:04")))
		b.WriteString(ansi.Truncate(legend, contentWidth, "…"))
	}

	return panelStyle.Width(max(1, m.width-2)).Render(b.String())
}

// renderDualTimeSeriesChart renders a two-series braille timeserieslinechart with left and right Y axes.
func renderDualTimeSeriesChart(
	width, height int,
	leftPts, rightPts []timeserieslinechart.TimePoint,
	leftStyle, rightStyle lipgloss.Style,
	y1Fmt, y2Fmt linechart.LabelFormatter,
	fixedMinY1, fixedMaxY1 float64,
) string {
	chartW := max(12, width)
	chartH := max(4, height)

	if len(leftPts) == 0 && len(rightPts) == 0 {
		return subtleTextStyle.Render("No time-series data points.")
	}

	minT, maxT := seriesTimeBounds(leftPts, rightPts)
	if !maxT.After(minT) {
		minT = minT.Add(-30 * time.Second)
		maxT = maxT.Add(30 * time.Second)
		if len(leftPts) == 1 {
			v := leftPts[0].Value
			leftPts = []timeserieslinechart.TimePoint{{Time: minT, Value: v}, {Time: maxT, Value: v}}
		}
		if len(rightPts) == 1 {
			v := rightPts[0].Value
			rightPts = []timeserieslinechart.TimePoint{{Time: minT, Value: v}, {Time: maxT, Value: v}}
		}
	}

	minY1, maxY1 := seriesValueBounds(leftPts)
	if fixedMaxY1 > fixedMinY1 {
		// Keep coverage bounded in [0, 100] with gentle zoom around active range
		minY1 = math.Max(fixedMinY1, math.Floor(minY1-5))
		maxY1 = math.Min(fixedMaxY1, math.Ceil(maxY1+5))
		if maxY1 <= minY1 {
			minY1, maxY1 = fixedMinY1, fixedMaxY1
		}
	} else {
		minY1 = 0
		maxY1 = math.Ceil(maxY1 * 1.15)
		if maxY1 <= minY1 {
			maxY1 = 10
		}
	}

	minY2, maxY2 := seriesValueBounds(rightPts)
	minY2 = 0
	maxY2 = math.Ceil(maxY2 * 1.15)
	if maxY2 <= minY2 {
		maxY2 = 10
	}

	span := maxT.Sub(minT)
	xFmt := func(_ int, v float64) string {
		t := time.UnixMilli(int64(math.Round(v * 1e3))).UTC()
		if span <= 36*time.Hour {
			return t.Format("15:04")
		}
		return t.Format("01/02")
	}

	xStep := 2
	yStep := 2
	if chartW < 36 {
		xStep = 0
		yStep = 0
	} else if chartH < 6 {
		xStep = 0
		yStep = 1
	}

	opts := []timeserieslinechart.Option{
		timeserieslinechart.WithAxesStyles(subtleTextStyle, subtleTextStyle),
		timeserieslinechart.WithXYSteps(xStep, yStep),
		timeserieslinechart.WithXLabelFormatter(xFmt),
		timeserieslinechart.WithYLabelFormatter(y1Fmt),
		timeserieslinechart.WithTimeRange(minT, maxT),
		timeserieslinechart.WithYRange(minY1, maxY1),
	}
	if len(rightPts) > 0 && yStep > 0 && chartW >= 44 {
		opts = append(opts,
			timeserieslinechart.WithY2Axis(linechart.Axis{
				Min:            minY2,
				Max:            maxY2,
				LabelFormatter: y2Fmt,
				LabelStyle:     subtleTextStyle,
			}),
			timeserieslinechart.WithDataSetYAxis("right", linechart.YAxisRight),
		)
	}
	opts = append(opts,
		timeserieslinechart.WithDataSetStyle("left", leftStyle),
		timeserieslinechart.WithDataSetYAxis("left", linechart.YAxisLeft),
		timeserieslinechart.WithDataSetStyle("right", rightStyle),
		timeserieslinechart.WithDataSetTimeSeries("left", leftPts),
	)
	if len(rightPts) > 0 {
		opts = append(opts, timeserieslinechart.WithDataSetTimeSeries("right", rightPts))
	}

	ts := timeserieslinechart.New(chartW, chartH, opts...)
	ts.DrawBrailleAll()
	return clampLinesWidth(ts.View(), width)
}

func renderDetailHistoryChart(history []api.CoveragePoint, width, height int) string {
	leftPts := make([]timeserieslinechart.TimePoint, 0, len(history))
	rightPts := make([]timeserieslinechart.TimePoint, 0, len(history))
	for _, pt := range history {
		t := pt.Started
		if t.IsZero() {
			t = time.Unix(pt.RunID*60, 0).UTC()
		}
		leftPts = append(leftPts, timeserieslinechart.TimePoint{Time: t, Value: pt.CoveragePct})
		rightPts = append(rightPts, timeserieslinechart.TimePoint{Time: t, Value: float64(pt.CorpusCount)})
	}
	return renderDualTimeSeriesChart(
		width, height,
		leftPts, rightPts,
		lipgloss.NewStyle().Foreground(colorGreen),
		lipgloss.NewStyle().Foreground(colorAccent),
		func(_ int, v float64) string { return fmt.Sprintf("%.0f%%", v) },
		func(_ int, v float64) string { return formatCompactNumber(v) },
		0, 100,
	)
}

func renderOutcomesBarChart(width, height int, buckets []api.StatsBucket, window string) string {
	chartW := max(12, width)
	chartH := max(4, height)

	maxBars := max(4, chartW/4)
	visible := buckets
	if len(visible) > maxBars {
		visible = visible[len(visible)-maxBars:]
	}

	bars := make([]barchart.BarData, 0, len(visible))
	for _, bk := range visible {
		lbl := bk.BucketStart.UTC().Format("15:04")
		if window == api.StatsWindow7d || window == api.StatsWindow14d {
			lbl = bk.BucketStart.UTC().Format("01/02")
		}
		bars = append(bars, barchart.BarData{
			Label: lbl,
			Values: []barchart.BarValue{
				{Name: "OK", Value: float64(bk.OKRuns), Style: statusOKStyle},
				{Name: "CRASH", Value: float64(bk.CrashRuns), Style: statusCrashStyle},
				{Name: "ERR", Value: float64(bk.ErrorRuns + bk.IncompleteRuns), Style: statusWarnStyle},
				{Name: "CANC", Value: float64(bk.CanceledRuns), Style: statusPendingStyle},
			},
		})
	}

	bc := barchart.New(chartW, chartH,
		barchart.WithStyles(subtleTextStyle, subtleTextStyle),
		barchart.WithBarGap(1),
		barchart.WithDataSet(bars),
	)
	bc.Draw()
	return clampLinesWidth(bc.View(), width)
}

func seriesTimeBounds(a, b []timeserieslinechart.TimePoint) (minT, maxT time.Time) {
	for _, pt := range a {
		if minT.IsZero() || pt.Time.Before(minT) {
			minT = pt.Time
		}
		if maxT.IsZero() || pt.Time.After(maxT) {
			maxT = pt.Time
		}
	}
	for _, pt := range b {
		if minT.IsZero() || pt.Time.Before(minT) {
			minT = pt.Time
		}
		if maxT.IsZero() || pt.Time.After(maxT) {
			maxT = pt.Time
		}
	}
	if minT.IsZero() {
		now := time.Now().UTC()
		return now.Add(-time.Minute), now
	}
	return minT, maxT
}

func seriesValueBounds(pts []timeserieslinechart.TimePoint) (minV, maxV float64) {
	if len(pts) == 0 {
		return 0, 1
	}
	minV, maxV = pts[0].Value, pts[0].Value
	for _, pt := range pts[1:] {
		if pt.Value < minV {
			minV = pt.Value
		}
		if pt.Value > maxV {
			maxV = pt.Value
		}
	}
	return minV, maxV
}

func formatCompactNumber(v float64) string {
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0) || v <= 0:
		return "0"
	case v >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", v/1_000_000_000)
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fk", v/1_000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func clampLinesWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > maxWidth {
			lines[i] = ansi.Truncate(line, maxWidth, "")
		}
	}
	return strings.Join(lines, "\n")
}

// renderRevisionStats keeps the chart and a paged table together at terminal widths.
func (m Model) renderRevisionStats(width, height int) string {
	var b strings.Builder
	stats := m.statsData
	chartHeight := max(4, min(8, height/3))
	bars := make([]barchart.BarData, 0, len(stats.Buckets))
	for _, v := range stats.Buckets {
		label := v.BucketStart.UTC().Format("15:04")
		if stats.Window == "7d" || stats.Window == "14d" {
			label = v.BucketStart.UTC().Format("01/02")
		}
		bars = append(bars, barchart.BarData{Label: label, Values: []barchart.BarValue{
			{Name: "Awaiting fuzz", Value: float64(v.DetectedRevisions - v.TestedRevisions), Style: statusRunningStyle},
			{Name: "Fuzzed", Value: float64(v.TestedRevisions), Style: statusOKStyle},
		}})
	}
	chart := barchart.New(max(12, width), chartHeight, barchart.WithStyles(subtleTextStyle, subtleTextStyle), barchart.WithDataSet(bars))
	chart.Draw()
	b.WriteString(m.graphOrText(clampLinesWidth(chart.View(), width)) + "\n")
	b.WriteString(ansi.Truncate("Detected revisions: awaiting fuzz (blue) + fuzzed (green); baselines excluded", width, "…") + "\n")
	if len(stats.Revisions) == 0 {
		b.WriteString("No revision observations in this window.\n")
		return b.String()
	}
	// Two lines per record keep detection and sync times visible even on narrow screens.
	// Only the legend, column header, and pagination footer sit outside the rows.
	pageSize := max(1, (height-lipgloss.Height(chart.View())-3)/2)
	pages := (len(stats.Revisions) + pageSize - 1) / pageSize
	page := min(m.revisionPage, pages-1)
	b.WriteString(ansi.Truncate(fmt.Sprintf("REPO / REVISION          RUNS ok/crash/error    REPLAY pass/fail/blocked/pending"), width, "…") + "\n")
	start := page * pageSize
	end := min(start+pageSize, len(stats.Revisions))
	for _, v := range stats.Revisions[start:end] {
		kind := "update"
		if v.Baseline {
			kind = "baseline"
		}
		if v.Current {
			kind += " current"
		}
		sha := v.GitSHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		prev := v.PreviousSHA
		if len(prev) > 8 {
			prev = prev[:8]
		}
		if prev != "" {
			sha = prev + "→" + sha
		}
		line := fmt.Sprintf("%-12s %-22s %d/%d/%d    %d/%d/%d/%d", v.Project, sha, v.OKRuns, v.CrashRuns, v.ErrorRuns, v.ReplayPassed, v.ReplayFailed, v.ReplayBlocked, v.ReplayPending)
		b.WriteString(ansi.Truncate(line, width, "…") + "\n")
		sync := "never"
		if v.LastSynced != nil {
			sync = v.LastSynced.UTC().Format("01-02 15:04")
		}
		state := "awaiting fuzzing"
		if v.OKRuns+v.CrashRuns > 0 {
			state = "fuzzed"
		}
		if v.ErrorRuns > 0 && v.OKRuns+v.CrashRuns == 0 {
			state = "fuzz blocked"
		}
		if v.SyncError != "" {
			state = "SYNC ERROR: " + api.RedactSyncError(v.SyncError)
		}
		detail := fmt.Sprintf("  %s UTC • %s • sync %s • %s • %d targets tested", v.DetectedAt.UTC().Format("01-02 15:04"), kind, sync, state, v.TestedTargets)
		b.WriteString(subtleTextStyle.Render(ansi.Truncate(detail, width, "…")) + "\n")
	}
	b.WriteString(ansi.Truncate(fmt.Sprintf("Page %d/%d [ / ] • p: project • w: window • pass = saved input no longer reproduces", page+1, pages), width, "…"))
	return b.String()
}
