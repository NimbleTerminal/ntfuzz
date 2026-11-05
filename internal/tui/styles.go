package tui

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	colorBorder = lipgloss.Color("#2d3f56")
	colorAccent = lipgloss.Color("#38bdf8")
	colorGreen  = lipgloss.Color("#22c55e")
	colorRed    = lipgloss.Color("#ef4444")
	colorYellow = lipgloss.Color("#eab308")
	colorText   = lipgloss.Color("#e2e8f0")
	colorSubtle = lipgloss.Color("#94a3b8")
	colorDim    = lipgloss.Color("#64748b")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#0f172a")).
			Background(colorAccent).
			Padding(0, 1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorSubtle).
			PaddingLeft(1)

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1).
			MarginRight(1)

	cardTitleStyle = lipgloss.NewStyle().
			Foreground(colorSubtle)

	cardValueStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	activePanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Padding(0, 1)

	headerRowStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorBorder)

	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#ffffff")).
				Background(lipgloss.Color("#1e3a5f"))

	normalRowStyle = lipgloss.NewStyle().
			Foreground(colorText)

	statusOKStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorGreen)

	statusCrashStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorRed)

	statusRunningStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	statusWarnStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorYellow)

	statusPendingStyle = lipgloss.NewStyle().
				Foreground(colorDim)

	subtleTextStyle = lipgloss.NewStyle().
			Foreground(colorSubtle)

	errorBannerStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorRed)

	trendBars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	trendCols = []string{
		"#38bdf8",
		"#34d399",
		"#22c55e",
		"#4ade80",
		"#86efac",
		"#a3e635",
		"#facc15",
		"#fb923c",
	}
)

// RenderStatusBadge returns a color-coded status indicator string.
func RenderStatusBadge(status string, isRunning bool) string {
	if isRunning {
		return statusRunningStyle.Render("⚡ FUZZ")
	}
	switch strings.ToLower(status) {
	case "ok", "pass", "healthy":
		return statusOKStyle.Render("● OK   ")
	case "crash", "crashes_detected", "fail":
		return statusCrashStyle.Render("✖ CRASH")
	case "error":
		return statusWarnStyle.Render("! ERROR")
	case "fuzzing", "running":
		return statusRunningStyle.Render("⚡ FUZZ")
	default:
		return statusPendingStyle.Render("○ PEND ")
	}
}

// RenderCoverageTrend renders a compact colored Lipgloss bar chart for a slice of coverage percentages.
func RenderCoverageTrend(points []float64, width int) string {
	if width <= 0 {
		width = 8
	} else if width > 256 {
		width = 256
	}
	clean := make([]float64, 0, len(points))
	for _, v := range points {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		clean = append(clean, v)
	}
	points = clean
	if len(points) == 0 {
		return statusPendingStyle.Render(strings.Repeat("·", width))
	}

	if len(points) > width {
		points = points[len(points)-width:]
	}

	minVal, maxVal := points[0], points[0]
	for _, v := range points {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	var b strings.Builder
	pad := width - len(points)
	if pad > 0 {
		b.WriteString(statusPendingStyle.Render(strings.Repeat("·", pad)))
	}

	span := maxVal - minVal
	for _, v := range points {
		var idx int
		if span < 0.01 {
			// Flat non-zero coverage: scale relative to 100%
			idx = int(math.Round((math.Min(math.Max(v, 0), 100) / 100.0) * float64(len(trendBars)-1)))
		} else {
			norm := (v - minVal) / span
			idx = int(math.Round(norm * float64(len(trendBars)-1)))
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= len(trendBars) {
			idx = len(trendBars) - 1
		}
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(trendCols[idx]))
		b.WriteString(st.Render(string(trendBars[idx])))
	}
	return b.String()
}

// FormatExecRate formats an execs/sec float into a compact human-readable string.
func FormatExecRate(rate float64) string {
	switch {
	case math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0:
		return "0/s"
	case rate >= 1_000_000:
		return fmt.Sprintf("%.1fM/s", rate/1_000_000)
	case rate >= 1_000:
		return fmt.Sprintf("%.1fk/s", rate/1_000)
	default:
		return fmt.Sprintf("%.0f/s", rate)
	}
}
