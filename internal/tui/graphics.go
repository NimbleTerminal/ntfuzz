package tui

import (
	"encoding/json"
	"fmt"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"os"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/picture/chartpicture/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
	"github.com/go-analyze/charts"
	"nimbleterminal.dev/ntfuzz/internal/api"
)

var nextChartID atomic.Int32

type graphicsProbeTimeout struct{ ID int }

// Update forwards every message to chartpicture, including async raster results,
// Kitty probe replies, cell sizes, and image transmission completion messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ntcharts capability is process-wide, but a server has multiple terminals.
	// Gate this model on its own reply before enabling the shared renderer.
	switch event := msg.(type) {
	case uv.KittyGraphicsEvent:
		if m.graphicsInitialized && event.Options.ID == m.graphicsID && string(event.Payload) == "OK" {
			m.graphicsSupported = true
			m.graphicsProbeExpired = false
			picture.ForceKittyCapability(picture.KittyCapabilitySupported)
		}
	case graphicsProbeTimeout:
		if event.ID == m.graphicsID && !m.graphicsSupported {
			m.graphicsProbeExpired = true
		}
	}
	var imageCmd tea.Cmd
	if m.graphicsInitialized {
		imageCmd = m.graphics.Update(msg)
	}
	next, cmd := m.update(msg)
	m = next.(Model)
	syncCmd := m.syncGraphics()
	return m, tea.Batch(imageCmd, cmd, syncCmd)
}

func (m *Model) toggleGraphics() tea.Cmd {
	m.graphicsRequested = !m.graphicsRequested
	if !m.graphicsInitialized {
		m.graphicsID = 1000 + int(nextChartID.Add(1))
		m.graphics = chartpicture.NewWithConfig(chartpicture.Config{KittyID: m.graphicsID, Theme: "dark", Fit: picture.FitFill})
		m.graphicsInitialized = true
	}
	if !m.graphicsRequested || m.graphicsSupported {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NTCHARTS_KITTY"))) {
	case "supported", "true", "1", "on", "yes":
		m.graphicsSupported = true
		picture.ForceKittyCapability(picture.KittyCapabilitySupported)
		return picture.RequestCellSize()
	case "unsupported", "false", "0", "off", "no":
		m.graphicsProbeExpired = true
		return nil
	}
	m.graphicsProbeExpired = false
	id := m.graphicsID
	// A tiny, non-displaying capability query is sent only after the user's opt-in.
	query := ansi.KittyGraphics([]byte("AAAA"), "a=q", "t=d", "f=24", "s=1", "v=1", fmt.Sprintf("i=%d", id))
	passthrough := os.Getenv("TMUX") != ""
	switch strings.ToLower(os.Getenv("NTCHARTS_TMUX_PASSTHROUGH")) {
	case "true", "1", "on", "yes":
		passthrough = true
	case "false", "0", "off", "no":
		passthrough = false
	}
	if passthrough {
		query = ansi.TmuxPassthrough(query)
	}
	return tea.Batch(picture.RequestCellSize(), tea.Raw(query), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return graphicsProbeTimeout{ID: id} }))
}

func (m Model) graphicsLabel() string {
	if !m.graphicsRequested {
		return "text (i: Kitty)"
	}
	if !m.graphicsSupported {
		if m.graphicsProbeExpired {
			return "text (Kitty unavailable)"
		}
		return "text (probing Kitty)"
	}
	if m.graphics.Err() != nil {
		return "text — chart render failed"
	}
	return "Kitty (i: text)"
}

func (m Model) graphSize() (int, int) {
	if m.compactStats() {
		return max(1, m.width), m.compactChartHeight()
	}
	width := max(12, m.width-6)
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
	if m.currentStatsQuery().ID == api.StatsQueryRevisions {
		// Exact revision layout: four heading rows, panel border, and actual chrome.
		used := 6 + lipgloss.Height(m.renderHeader()) + lipgloss.Height(m.renderOverviewCards())
		if !m.hideHelp {
			used += lipgloss.Height(m.help.View(m.keyMap()))
		}
		if m.lastErr != nil {
			used += lipgloss.Height(errorBannerStyle.Render(fmt.Sprintf("  ⚠ API warning: %v", m.lastErr)))
		}
		for _, p := range m.projects {
			if p.SyncError != "" {
				used += lipgloss.Height(errorBannerStyle.Render(fmt.Sprintf("  ⚠ Project %s sync error: %s", p.Name, p.SyncError)))
			}
		}
		return width, max(4, min(8, max(8, m.height-used)/3))
	}
	return width, max(5, min(18, m.height-reserved-9))
}

func (m *Model) syncGraphics() tea.Cmd {
	if !m.graphicsInitialized {
		return nil
	}
	if !m.graphicsRequested || m.mode != ViewStats || (m.compactStats() && m.compactChartHeight() == 0) {
		m.graphicsKey = ""
		if m.graphics.Mode() == picture.PictureKitty {
			return m.graphics.Toggle()
		}
		return nil
	}
	if !m.graphicsSupported || (m.graphics.ToggleBlocked() != nil && m.graphics.Mode() != picture.PictureKitty) {
		return nil
	}
	var cmds []tea.Cmd
	if m.graphics.Mode() != picture.PictureKitty {
		cmds = append(cmds, m.graphics.Toggle())
	}
	if m.statsData.Query != m.currentStatsQuery().ID || len(m.statsData.Buckets) == 0 {
		return tea.Batch(cmds...)
	}
	width, height := m.graphSize()
	spec := imageChartData(m.statsData)
	if m.compactStats() {
		index := 0
		if m.currentStatsQuery().ID == api.StatsQueryOutcomes {
			index = 1
		}
		spec.Values = spec.Values[index : index+1]
		spec.Names = []string{compactSeriesName(m.currentStatsQuery().ID)}
		spec.Dual = false
		spec.Bar = false
	}
	raw, _ := json.Marshal(spec)
	cacheKey := fmt.Sprintf("%dx%d:%s", width, height, raw)
	if cacheKey == m.graphicsKey {
		return tea.Batch(cmds...)
	}
	m.graphicsKey = cacheKey
	cmds = append(cmds, m.graphics.SetSize(width, height))
	if spec.Bar {
		opt := charts.NewBarChartOptionWithData(spec.Values)
		opt.Theme = charts.GetTheme("dark")
		opt.CategoryAxis.Labels = spec.Labels
		opt.CategoryAxis.LabelCount = max(2, width/12)
		for i, name := range spec.Names {
			opt.SeriesList[i].Name = name
		}
		opt.Legend = charts.LegendOption{Show: charts.Ptr(true), SeriesNames: spec.Names}
		cmds = append(cmds, m.graphics.SetBarChartOption(opt))
	} else {
		series := charts.NewSeriesListLine(spec.Values)
		for i, name := range spec.Names {
			series[i].Name = name
			if spec.Dual && i == 1 {
				series[i].YAxisIndex = 1
			}
		}
		opt := charts.NewLineChartOptionWithSeries(series)
		opt.Theme = charts.GetTheme("dark")
		opt.XAxis.Labels = spec.Labels
		opt.XAxis.LabelCount = max(2, width/12)
		opt.ValueFormatter = func(v float64) string { return fmt.Sprintf("%g", v) }
		opt.Legend = charts.LegendOption{Show: charts.Ptr(true), SeriesNames: spec.Names}
		opt.StrokeSmoothingTension = 0
		cmds = append(cmds, m.graphics.SetLineChartOption(opt))
	}
	return tea.Batch(cmds...)
}

type chartData struct {
	Names, Labels []string
	Values        [][]float64
	Bar, Dual     bool
}

func imageChartData(stats api.StatsResponse) chartData {
	d := chartData{}
	switch stats.Query {
	case api.StatsQueryCoverage:
		d.Names = []string{"Coverage %", "Corpus"}
		d.Dual = true
	case api.StatsQueryOutcomes:
		d.Names = []string{"OK", "Crash", "Error/incomplete", "Canceled"}
		d.Bar = true
	case api.StatsQueryRevisions:
		d.Names = []string{"Awaiting fuzz", "Fuzzed"}
		d.Bar = true
	case api.StatsQueryBurndown:
		d.Names = []string{"Open crashes", "New", "Reopened", "Resolved"}
	default:
		d.Names = []string{"Execs/sec", "Executions"}
		d.Dual = true
	}
	d.Values = make([][]float64, len(d.Names))
	for _, b := range stats.Buckets {
		label := b.BucketStart.UTC().Format("15:04")
		if stats.Window == "7d" || stats.Window == "14d" {
			label = b.BucketStart.UTC().Format("01/02 15h")
		}
		d.Labels = append(d.Labels, label)
		var values []float64
		switch stats.Query {
		case api.StatsQueryCoverage:
			values = []float64{b.AvgCoveragePct, float64(b.CorpusCount)}
		case api.StatsQueryOutcomes:
			values = []float64{float64(b.OKRuns), float64(b.CrashRuns), float64(b.ErrorRuns + b.IncompleteRuns), float64(b.CanceledRuns)}
		case api.StatsQueryRevisions:
			values = []float64{float64(b.DetectedRevisions - b.TestedRevisions), float64(b.TestedRevisions)}
		case api.StatsQueryBurndown:
			values = []float64{float64(b.OpenCrashes), float64(b.NewCrashes), float64(b.ReopenedCrashes), float64(b.ResolvedCrashes)}
		default:
			values = []float64{b.AvgExecsPerSec, float64(b.Execs)}
		}
		for i, v := range values {
			d.Values[i] = append(d.Values[i], v)
		}
	}
	return d
}

func (m Model) graphOrText(text string) string {
	if m.graphicsRequested && m.graphicsInitialized && m.graphics.Mode() == picture.PictureKitty && m.graphics.Err() == nil && m.statsData.Query == m.currentStatsQuery().ID {
		content := m.graphics.View().Content
		// Never truncate or overlay Kitty placeholder cells: that corrupts placement.
		if strings.TrimSpace(content) != "" {
			return content
		}
	}
	return text
}
