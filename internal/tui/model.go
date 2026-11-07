package tui

import (
	"cmp"
	"context"
	"fmt"
	"github.com/NimbleMarkets/ntcharts/picture/chartpicture/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/sparkline"
	"github.com/charmbracelet/x/ansi"
	"nimbleterminal.dev/booba/actions"
	"nimbleterminal.dev/booba/actions/bubblehelp"

	"nimbleterminal.dev/ntfuzz/internal/api"
)

type tickMsg time.Time

type fleetDataMsg struct {
	status   api.StatusResponse
	projects []api.Project
	targets  []api.Target
	crashes  []api.Crash
	err      error
}

type detailDataMsg struct {
	targetID int64
	target   *api.Target
	runs     []api.Run
	crashes  []api.Crash
	history  []api.CoveragePoint
	err      error
}

type statsDataMsg struct {
	stats api.StatsResponse
	err   error
}

// SortColumn identifies the column used to sort the fleet target table.
type SortColumn int

const (
	SortNone SortColumn = iota
	SortStatus
	SortProject
	SortTarget
	SortCrashes
	SortExecRate
	SortLastRun
	SortCoverage
	SortRevision
	SortCorpus
)

// Title returns the fleet table header title corresponding to s.
func (s SortColumn) Title() string {
	switch s {
	case SortStatus:
		return "STATUS"
	case SortProject:
		return "PROJECT"
	case SortTarget:
		return "TARGET"
	case SortCrashes:
		return "CRASH"
	case SortExecRate:
		return "EXECS/SEC"
	case SortLastRun:
		return "LAST RUN"
	case SortCoverage:
		return "COV %"
	case SortRevision:
		return "REVISION"
	case SortCorpus:
		return "CORP"
	default:
		return ""
	}
}

func (s SortColumn) next() SortColumn {
	switch s {
	case SortNone:
		return SortStatus
	case SortStatus:
		return SortProject
	case SortProject:
		return SortTarget
	case SortTarget:
		return SortCrashes
	case SortCrashes:
		return SortExecRate
	case SortExecRate:
		return SortLastRun
	case SortLastRun:
		return SortCoverage
	case SortCoverage:
		return SortRevision
	case SortRevision:
		return SortCorpus
	default:
		return SortStatus
	}
}

func (s SortColumn) defaultDesc() bool {
	switch s {
	case SortProject, SortTarget, SortRevision:
		return false
	default:
		return true
	}
}

type crashFixedMsg struct {
	crashID int64
	err     error
}

// Model is the shared Bubble Tea dashboard model for ntfuzz.
// It communicates exclusively via the HTTP API client and never imports internal/store.
type Model struct {
	graphics                                chartpicture.Model
	graphicsInitialized, graphicsRequested  bool
	graphicsSupported, graphicsProbeExpired bool
	graphicsID                              int
	graphicsKey                             string
	client                                  *api.Client
	readOnly                                bool
	refreshInterval                         time.Duration

	width    int
	height   int
	mode     ViewMode
	prevMode ViewMode
	hideHelp bool
	help     help.Model

	// Fleet state
	status      api.StatusResponse
	projects    []api.Project
	targets     []api.Target
	openCrashes []api.Crash
	projectIdx  int // 0 = All Projects, 1..N = projects[projectIdx-1]
	selectedIdx int
	crashIdx    int
	sortCol     SortColumn
	sortDesc    bool
	lastUpdated time.Time
	lastErr     error
	loading     bool

	// Target Detail state
	detailTarget    *api.Target
	selectedCrashID int64
	detailRuns      []api.Run
	detailCrashes   []api.Crash
	detailHistory   []api.CoveragePoint
	runsTable       table.Model
	covSparkline    sparkline.Model

	// Stats / Canned Queries state
	revisionPage   int
	statsQueryIdx  int
	statsWindowIdx int
	statsData      api.StatsResponse
}

// NewModel constructs a pure Bubble Tea Model backed by the HTTP API client.
func NewModel(client *api.Client, refreshInterval time.Duration) Model {
	return NewModelOptions(client, refreshInterval, false)
}
func NewModelOptions(client *api.Client, refreshInterval time.Duration, readOnly bool) Model {
	if refreshInterval <= 0 {
		refreshInterval = 2 * time.Second
	}

	cols := []table.Column{
		{Title: "Run ID", Width: 7},
		{Title: "Started", Width: 14},
		{Title: "Rev", Width: 10},
		{Title: "Dur", Width: 6},
		{Title: "Execs", Width: 9},
		{Title: "Rate", Width: 9},
		{Title: "+New/Corp", Width: 10},
		{Title: "Cov %", Width: 7},
		{Title: "Status", Width: 7},
		{Title: "Worker", Width: 8},
	}
	rt := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(8),
	)
	ts := table.DefaultStyles()
	ts.Header = ts.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorBorder).
		BorderBottom(true).
		Bold(true).
		Foreground(colorAccent)
	ts.Selected = ts.Selected.
		Foreground(lipgloss.Color("#ffffff")).
		Background(lipgloss.Color("#1e3a5f")).
		Bold(true)
	rt.SetStyles(ts)

	sp := sparkline.New(36, 3, sparkline.WithStyle(lipgloss.NewStyle().Foreground(colorGreen)))

	h := help.New()
	h.SetWidth(100)

	return Model{
		client:          client,
		readOnly:        readOnly,
		refreshInterval: refreshInterval,
		width:           116,
		height:          32,
		mode:            ViewFleet,
		help:            h,
		loading:         true,
		runsTable:       rt,
		covSparkline:    sp,
		statsWindowIdx:  2, // "24h"
	}
}

// NewActionModel wraps Model with go-booba's bubblehelp adapter so keybindings
// are published as OSC 7501 Booba Terminal Actions.
func NewActionModel(client *api.Client, refreshInterval time.Duration) (tea.Model, error) {
	return NewActionModelOptions(client, refreshInterval, false)
}
func NewActionModelOptions(client *api.Client, refreshInterval time.Duration, readOnly bool) (tea.Model, error) {
	base := NewModelOptions(client, refreshInterval, readOnly)
	return bubblehelp.NewModelWithFallback(base, DescribeActions, bubblehelp.ModelOptions{
		Adapter: bubblehelp.Options{
			Customize: func(b key.Binding, a *actions.Action) error {
				if len(a.Keys) > 1 {
					pref := 0
					a.Preferred = &pref
				}
				return nil
			},
		},
	})
}

// DescribeActions maps the current TUI state to a Booba Terminal Actions description.
func DescribeActions(inner tea.Model) bubblehelp.Description {
	m, ok := inner.(Model)
	if !ok {
		return bubblehelp.Description{
			Context: "fleet",
			Keys:    NewKeyMap(ViewFleet, false),
			Status:  actions.ProgramStatus{App: "ntfuzz", State: "idle", Summary: "ntfuzz dashboard"},
		}
	}

	km := m.keyMap()
	state := "idle"
	if m.status.ActiveWorkers > 0 {
		state = "working"
	}
	summary := fmt.Sprintf("Fleet: %d projects, %d targets, %d open crashes",
		m.status.TotalProjects, m.status.TotalTargets, m.status.OpenCrashes)
	if m.mode == ViewDetail && m.detailTarget != nil {
		summary = fmt.Sprintf("Target %s/%s (%.1f%% cov, %d corpus)",
			m.detailTarget.ProjectName, m.detailTarget.Name, m.detailTarget.CoveragePct, m.detailTarget.CorpusCount)
	} else if m.mode == ViewCrashes {
		summary = fmt.Sprintf("Open Crashes (%d)", len(m.openCrashes))
	} else if m.mode == ViewStats {
		q := m.currentStatsQuery()
		summary = fmt.Sprintf("Stats: %s (%s)", q.Title, m.currentStatsWindow())
	}

	return bubblehelp.Description{
		Context: m.mode.ContextName(),
		Keys:    km,
		Status: actions.ProgramStatus{
			App:     "ntfuzz",
			State:   state,
			Summary: summary,
		},
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchFleetCmd(),
		m.tickCmd(),
	)
}

func (m Model) tickCmd() tea.Cmd {
	d := m.refreshInterval
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) currentProjectFilter() string {
	if m.projectIdx <= 0 || m.projectIdx > len(m.projects) {
		return ""
	}
	return m.projects[m.projectIdx-1].Name
}

func (m Model) fetchFleetCmd() tea.Cmd {
	client := m.client
	projFilter := m.currentProjectFilter()
	return func() tea.Msg {
		if client == nil {
			return fleetDataMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()

		status, err := client.GetStatus(ctx)
		if err != nil {
			return fleetDataMsg{err: err}
		}
		projects, err := client.GetProjects(ctx)
		if err != nil {
			return fleetDataMsg{err: err}
		}
		targets, err := client.GetTargets(ctx, projFilter)
		if err != nil {
			return fleetDataMsg{err: err}
		}
		crashes, err := client.GetCrashes(ctx, true, "")
		if err != nil {
			return fleetDataMsg{err: err}
		}
		return fleetDataMsg{
			status:   status,
			projects: projects,
			targets:  targets,
			crashes:  crashes,
		}
	}
}

func (m Model) fetchDetailCmd(targetID int64) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		if client == nil {
			return detailDataMsg{targetID: targetID}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()

		idStr := strconv.FormatInt(targetID, 10)
		runs, err := client.GetRuns(ctx, idStr, 50)
		if err != nil {
			return detailDataMsg{targetID: targetID, err: err}
		}
		crashes, err := client.GetCrashes(ctx, true, idStr)
		if err != nil {
			return detailDataMsg{targetID: targetID, err: err}
		}
		history, err := client.GetCoverageHistory(ctx, idStr)
		if err != nil {
			return detailDataMsg{targetID: targetID, err: err}
		}
		var resolvedTarget *api.Target
		if allTargets, tErr := client.GetTargets(ctx, ""); tErr == nil {
			for i := range allTargets {
				if allTargets[i].ID == targetID {
					tCopy := allTargets[i]
					resolvedTarget = &tCopy
					break
				}
			}
		}
		return detailDataMsg{
			targetID: targetID,
			target:   resolvedTarget,
			runs:     runs,
			crashes:  crashes,
			history:  history,
		}
	}
}

func (m Model) fixCrashCmd(crashID int64) tea.Cmd {
	if m.readOnly {
		return nil
	}
	client := m.client
	return func() tea.Msg {
		if client == nil {
			return crashFixedMsg{crashID: crashID}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		err := client.MarkCrashFixed(ctx, crashID, "")
		return crashFixedMsg{crashID: crashID, err: err}
	}
}

func (m Model) fetchStatsCmd() tea.Cmd {
	client := m.client
	q := m.currentStatsQuery().ID
	w := m.currentStatsWindow()
	proj := m.currentProjectFilter()
	return func() tea.Msg {
		if client == nil {
			return statsDataMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		stats, err := client.GetStats(ctx, q, w, proj, "")
		return statsDataMsg{stats: stats, err: err}
	}
}

// Update implements tea.Model.
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
			m.help.SetWidth(max(1, msg.Width-4))
		}
		if msg.Height > 0 {
			m.height = msg.Height
			tableH := max(4, min(12, msg.Height-20))
			m.runsTable.SetHeight(tableH)
		}
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{m.tickCmd(), m.fetchFleetCmd()}
		if m.mode == ViewDetail && m.detailTarget != nil {
			cmds = append(cmds, m.fetchDetailCmd(m.detailTarget.ID))
		}
		if m.mode == ViewStats {
			cmds = append(cmds, m.fetchStatsCmd())
		}
		return m, tea.Batch(cmds...)

	case crashFixedMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			return m, nil
		}
		m.lastErr = nil
		return m, m.fetchFleetCmd()

	case fleetDataMsg:
		if msg.status.ReadOnly {
			m.readOnly = true
		}
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err
			return m, nil
		}
		m.lastErr = nil
		m.status = msg.status
		m.projects = msg.projects
		m.targets = msg.targets
		m.openCrashes = msg.crashes
		m.lastUpdated = time.Now()
		m.sortTargets()

		if m.projectIdx > len(m.projects) {
			m.projectIdx = 0
		}
		if len(m.targets) == 0 {
			m.selectedIdx = 0
		} else if m.selectedIdx >= len(m.targets) {
			m.selectedIdx = len(m.targets) - 1
		}
		if len(m.openCrashes) == 0 {
			m.crashIdx = 0
		} else if m.crashIdx >= len(m.openCrashes) {
			m.crashIdx = len(m.openCrashes) - 1
		}
		if m.detailTarget != nil {
			for i := range m.targets {
				if m.targets[i].ID == m.detailTarget.ID {
					tCopy := m.targets[i]
					m.detailTarget = &tCopy
					break
				}
			}
		}
		return m, nil

	case detailDataMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			return m, nil
		}
		if m.detailTarget == nil || m.detailTarget.ID != msg.targetID {
			return m, nil
		}
		m.lastErr = nil
		if msg.target != nil {
			tCopy := *msg.target
			m.detailTarget = &tCopy
		}
		m.detailRuns = msg.runs
		if len(msg.crashes) > 0 || m.client != nil {
			m.detailCrashes = msg.crashes
		}
		m.detailHistory = msg.history
		m.syncDetailWidgets()
		return m, nil

	case statsDataMsg:
		if msg.stats.Query != "" && (msg.stats.Query != m.currentStatsQuery().ID || msg.stats.Window != m.currentStatsWindow() || msg.stats.Project != m.currentProjectFilter()) {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err
			return m, nil
		}
		m.lastErr = nil
		m.statsData = msg.stats
		return m, nil

	case tea.KeyPressMsg:
		if m.mode == ViewStats && m.currentStatsQuery().ID == api.StatsQueryRevisions {
			switch msg.String() {
			case "[":
				m.revisionPage = max(0, m.revisionPage-1)
				return m, nil
			case "]":
				m.revisionPage = m.revisionPage + 1
				if m.compactStats() {
					m.revisionPage = min(m.revisionPage, len(m.compactRevisionPages())-1)
				} else {
					m.revisionPage = min(m.revisionPage, max(0, len(m.statsData.Revisions)-1))
				}
				m.revisionPage = max(0, m.revisionPage)
				return m, nil
			}
		}

		km := m.keyMap()
		switch {
		case key.Matches(msg, km.Quit):
			return m, tea.Quit

		case key.Matches(msg, km.Visibility):
			m.hideHelp = !m.hideHelp
			return m, nil

		case key.Matches(msg, km.More):
			m.hideHelp = false
			m.help.ShowAll = !m.help.ShowAll
			return m, nil

		case key.Matches(msg, km.Refresh):
			cmds := []tea.Cmd{m.fetchFleetCmd()}
			if m.mode == ViewDetail && m.detailTarget != nil {
				cmds = append(cmds, m.fetchDetailCmd(m.detailTarget.ID))
			}
			if m.mode == ViewStats {
				cmds = append(cmds, m.fetchStatsCmd())
			}
			return m, tea.Batch(cmds...)

		case key.Matches(msg, km.Graphs):
			if m.mode == ViewStats {
				m.mode = ViewFleet
				return m, nil
			}
			m.mode = ViewStats
			return m, m.fetchStatsCmd()

		case key.Matches(msg, km.Window):
			m.revisionPage = 0
			if m.mode == ViewStats {
				m.statsWindowIdx = (m.statsWindowIdx + 1) % len(StatsWindows)
				return m, m.fetchStatsCmd()
			}

		case key.Matches(msg, km.FixCrash):
			if m.mode == ViewCrashes && len(m.openCrashes) > 0 && m.crashIdx < len(m.openCrashes) {
				cID := m.openCrashes[m.crashIdx].ID
				return m, m.fixCrashCmd(cID)
			}

		case key.Matches(msg, km.Crashes):
			if m.mode == ViewCrashes {
				m.mode = ViewFleet
			} else {
				m.mode = ViewCrashes
			}
			return m, nil

		case key.Matches(msg, km.Back):
			if m.mode == ViewDetail {
				if m.prevMode == ViewCrashes {
					m.mode = ViewCrashes
				} else {
					m.mode = ViewFleet
				}
				return m, nil
			}
			if m.mode != ViewFleet {
				m.mode = ViewFleet
				return m, nil
			}

		case key.Matches(msg, km.Sort):
			if m.mode == ViewFleet {
				m.sortCol = m.sortCol.next()
				m.sortDesc = m.sortCol.defaultDesc()
				m.sortTargets()
				return m, nil
			}

		case key.Matches(msg, km.SortDir):
			if m.mode == ViewFleet {
				if m.sortCol == SortNone {
					m.sortCol = SortProject
					m.sortDesc = true
				} else {
					m.sortDesc = !m.sortDesc
				}
				m.sortTargets()
				return m, nil
			}

		case key.Matches(msg, km.Graphics):
			if m.mode == ViewStats {
				cmd := m.toggleGraphics()
				return m, cmd
			}
		case key.Matches(msg, km.Project):
			m.revisionPage = 0
			if m.mode == ViewFleet || m.mode == ViewStats {
				if len(m.projects) > 0 {
					m.projectIdx = (m.projectIdx + 1) % (len(m.projects) + 1)
				} else {
					m.projectIdx = 0
				}
				m.selectedIdx = 0
				if m.mode == ViewStats {
					return m, tea.Batch(m.fetchFleetCmd(), m.fetchStatsCmd())
				}
				return m, m.fetchFleetCmd()
			}

		case key.Matches(msg, km.Enter):
			if m.mode == ViewFleet && len(m.targets) > 0 && m.selectedIdx < len(m.targets) {
				selected := m.targets[m.selectedIdx]
				m.detailTarget = &selected
				m.selectedCrashID = 0
				m.prevMode = ViewFleet
				m.mode = ViewDetail
				m.detailRuns = nil
				m.detailCrashes = nil
				m.detailHistory = nil
				return m, m.fetchDetailCmd(selected.ID)
			}
			if m.mode == ViewCrashes && len(m.openCrashes) > 0 && m.crashIdx < len(m.openCrashes) {
				crash := m.openCrashes[m.crashIdx]
				selected := m.findOrBuildTargetForCrash(crash)
				m.detailTarget = &selected
				m.selectedCrashID = crash.ID
				m.prevMode = ViewCrashes
				m.mode = ViewDetail
				m.detailRuns = nil
				m.detailCrashes = m.crashesForTarget(selected.ID, crash)
				m.detailHistory = nil
				return m, m.fetchDetailCmd(selected.ID)
			}

		case key.Matches(msg, km.Up):
			switch m.mode {
			case ViewFleet:
				if m.selectedIdx > 0 {
					m.selectedIdx--
				}
			case ViewCrashes:
				if m.crashIdx > 0 {
					m.crashIdx--
				}
			case ViewStats:
				m.statsQueryIdx = (m.statsQueryIdx - 1 + len(CannedQueries)) % len(CannedQueries)
				return m, m.fetchStatsCmd()
			case ViewDetail:
				var cmd tea.Cmd
				m.runsTable, cmd = m.runsTable.Update(msg)
				return m, cmd
			}
			return m, nil

		case key.Matches(msg, km.Down):
			switch m.mode {
			case ViewFleet:
				if m.selectedIdx < len(m.targets)-1 {
					m.selectedIdx++
				}
			case ViewCrashes:
				if m.crashIdx < len(m.openCrashes)-1 {
					m.crashIdx++
				}
			case ViewStats:
				m.statsQueryIdx = (m.statsQueryIdx + 1) % len(CannedQueries)
				return m, m.fetchStatsCmd()
			case ViewDetail:
				var cmd tea.Cmd
				m.runsTable, cmd = m.runsTable.Update(msg)
				return m, cmd
			}
			return m, nil
		}
	}

	if m.mode == ViewDetail {
		var cmd tea.Cmd
		m.runsTable, cmd = m.runsTable.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) sortTargets() {
	if m.sortCol == SortNone || len(m.targets) <= 1 {
		return
	}
	activeMap := map[string]bool{}
	for _, job := range m.status.ActiveJobs {
		activeMap[job.Project+"/"+job.Target] = true
	}
	slices.SortFunc(m.targets, func(a, b api.Target) int {
		var c int
		switch m.sortCol {
		case SortStatus:
			c = cmp.Compare(
				statusSortRank(a.Status, activeMap[a.ProjectName+"/"+a.Name], a.OpenCrashes),
				statusSortRank(b.Status, activeMap[b.ProjectName+"/"+b.Name], b.OpenCrashes),
			)
		case SortProject:
			c = cmp.Compare(a.ProjectName, b.ProjectName)
		case SortTarget:
			c = cmp.Compare(a.Name, b.Name)
		case SortCrashes:
			c = cmp.Compare(a.OpenCrashes, b.OpenCrashes)
		case SortExecRate:
			c = cmp.Compare(a.ExecsPerSec, b.ExecsPerSec)
		case SortLastRun:
			c = compareTimePtr(a.LastStarted, b.LastStarted)
		case SortCoverage:
			c = cmp.Compare(a.CoveragePct, b.CoveragePct)
		case SortRevision:
			c = cmp.Compare(formatRevision(a.GitSHA, a.Dirty), formatRevision(b.GitSHA, b.Dirty))
		case SortCorpus:
			c = cmp.Compare(a.CorpusCount, b.CorpusCount)
		}
		if m.sortDesc {
			c = -c
		}
		if c != 0 {
			return c
		}
		if c = cmp.Compare(a.ProjectName, b.ProjectName); c != 0 {
			return c
		}
		if c = cmp.Compare(a.Package, b.Package); c != 0 {
			return c
		}
		if c = cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func statusSortRank(status string, isRunning bool, openCrashes int) int {
	if isRunning {
		return 4
	}
	switch strings.ToLower(status) {
	case "crash", "crashes_detected", "fail":
		return 8
	case "error":
		return 7
	case "missing_target":
		return 6
	case "incomplete":
		return 5
	case "fuzzing", "running":
		return 4
	case "canceled":
		return 3
	case "ok", "pass", "healthy":
		if openCrashes > 0 {
			return 8
		}
		return 1
	default:
		return 2
	}
}

func compareTimePtr(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	default:
		return a.Compare(*b)
	}
}

func (m Model) findOrBuildTargetForCrash(crash api.Crash) api.Target {
	for _, t := range m.targets {
		if crash.TargetID != 0 && t.ID == crash.TargetID {
			return t
		}
		if crash.TargetID == 0 && t.ProjectName == crash.ProjectName && t.Name == crash.TargetName {
			return t
		}
	}
	return api.Target{
		ID:          crash.TargetID,
		ProjectName: crash.ProjectName,
		Package:     crash.Package,
		Name:        crash.TargetName,
		Status:      "crash",
		GitSHA:      crash.GitSHA,
		Dirty:       crash.Dirty,
		GoVersion:   crash.GoVersion,
		OpenCrashes: 1,
	}
}

func (m Model) crashesForTarget(targetID int64, primary api.Crash) []api.Crash {
	out := []api.Crash{primary}
	for _, c := range m.openCrashes {
		if c.ID == primary.ID {
			continue
		}
		if targetID != 0 && c.TargetID == targetID {
			out = append(out, c)
		}
	}
	return out
}

func (m *Model) syncDetailWidgets() {
	rows := make([]table.Row, 0, len(m.detailRuns))
	for _, r := range m.detailRuns {
		rows = append(rows, table.Row{
			fmt.Sprintf("#%d", r.ID),
			r.Started.Local().Format("01-02 15:04:05"),
			formatRevision(r.GitSHA, r.Dirty),
			fmt.Sprintf("%.1fs", r.DurationSec),
			fmt.Sprintf("%d", r.Execs),
			FormatExecRate(r.ExecsPerSec),
			fmt.Sprintf("+%d/%d", r.NewInteresting, r.CorpusCount),
			fmt.Sprintf("%.1f%%", r.CoveragePct),
			strings.ToUpper(r.Status),
			r.Worker,
		})
	}
	m.runsTable.SetRows(rows)

	m.covSparkline.Clear()
	for _, pt := range m.detailHistory {
		m.covSparkline.Push(pt.CoveragePct)
	}
	m.covSparkline.DrawBraille()
}

// View implements tea.Model.
func (m Model) View() tea.View {
	if m.mode == ViewFleet && (m.width < 80 || m.height < 24) {
		return m.compactFleetView()
	}
	if m.compactStats() {
		return m.compactStatsView()
	}
	var b strings.Builder

	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderOverviewCards())
	b.WriteString("\n")

	if m.lastErr != nil {
		b.WriteString(errorBannerStyle.Render(fmt.Sprintf("  ⚠ API warning: %v", m.lastErr)))
		b.WriteString("\n")
	}

	for _, p := range m.projects {
		if p.SyncError != "" {
			b.WriteString(errorBannerStyle.Render(fmt.Sprintf("  ⚠ Project %s sync error: %s", p.Name, p.SyncError)))
			b.WriteString("\n")
		}
	}

	switch m.mode {
	case ViewDetail:
		b.WriteString(m.renderDetailView())
	case ViewCrashes:
		b.WriteString(m.renderCrashesView())
	case ViewStats:
		b.WriteString(m.renderStatsView())
	default:
		b.WriteString(m.renderFleetView())
	}

	if !m.hideHelp {
		b.WriteString("\n")
		b.WriteString(m.help.View(m.keyMap()))
	}

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func (m Model) renderHeader() string {
	apiURL := "http://127.0.0.1:8080"
	if m.client != nil {
		apiURL = m.client.BaseURL()
	}
	title := titleStyle.Render(" NTFUZZ ")
	if m.readOnly {
		label := "Public snapshot • read-only"
		if m.status.SnapshotAt != nil {
			label += " • " + m.status.SnapshotAt.UTC().Format("2006-01-02 15:04:05 UTC")
		}
		return lipgloss.NewStyle().Width(max(1, m.width)).Render(lipgloss.JoinHorizontal(lipgloss.Center, title, subtitleStyle.Render(label)))
	}
	sub := subtitleStyle.Render(fmt.Sprintf("Continuous Go Fuzzing Orchestrator • API %s", apiURL))
	if !m.lastUpdated.IsZero() {
		sub += subtleTextStyle.Render(fmt.Sprintf(" • synced %s", m.lastUpdated.Format("15:04:05")))
	}
	return lipgloss.NewStyle().Width(max(1, m.width)).Render(lipgloss.JoinHorizontal(lipgloss.Center, title, sub))
}

func (m Model) renderOverviewCards() string {
	crashVal := statusOKStyle.Render("0")
	if m.status.OpenCrashes > 0 {
		crashVal = statusCrashStyle.Render(strconv.Itoa(m.status.OpenCrashes))
	}

	runsVal := cardValueStyle.Render(fmt.Sprintf("%d", m.status.RunsLast24h))
	runsSub := subtleTextStyle.Render(fmt.Sprintf(" (%s avg)", FormatExecRate(m.status.AvgExecsSec)))

	// A snapshot viewer has no orchestrator, so derive fleet health from the published data
	// and show how fresh that data is instead of a worker count that is always zero.
	health := m.status.FleetHealth
	if health == "snapshot" {
		health = "healthy"
		if m.status.OpenCrashes > 0 {
			health = "crashes_detected"
		}
	}
	fleetVal := RenderStatusBadge(health, m.status.ActiveWorkers > 0)

	var c4 string
	if m.readOnly || m.status.ReadOnly {
		age := "unknown"
		if m.status.SnapshotAt != nil {
			age = formatRelativeTime(*m.status.SnapshotAt)
		}
		c4 = cardStyle.Render(cardTitleStyle.Render("Snapshot Age") + "\n" + cardValueStyle.Render(age))
	} else {
		workerState := fmt.Sprintf("%d / %d", m.status.ActiveWorkers, max(m.status.MaxWorkers, m.status.ActiveWorkers))
		workerVal := cardValueStyle.Render(workerState)
		if m.status.ActiveWorkers > 0 {
			workerVal = statusRunningStyle.Render("⚡ " + workerState)
		}
		c4 = cardStyle.Render(cardTitleStyle.Render("Active Workers") + "\n" + workerVal)
	}

	c1 := cardStyle.Render(cardTitleStyle.Render("Fleet Status") + "\n" + fleetVal)
	c2 := cardStyle.Render(cardTitleStyle.Render("Open Crashes") + "\n" + crashVal)
	c3 := cardStyle.Render(cardTitleStyle.Render("Runs (24h)") + "\n" + runsVal + runsSub)
	c5 := cardStyle.Render(cardTitleStyle.Render("Projects / Targets") + "\n" +
		cardValueStyle.Render(fmt.Sprintf("%d / %d", m.status.TotalProjects, m.status.TotalTargets)))

	cards := []string{c1, c2, c3, c4, c5}
	var rows []string
	var row []string
	width := 0
	for _, card := range cards {
		cardWidth := lipgloss.Width(card)
		if len(row) > 0 && width+cardWidth > m.width {
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, row...))
			row = nil
			width = 0
		}
		row = append(row, card)
		width += cardWidth
	}
	rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, row...))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) renderFleetView() string {
	var b strings.Builder

	// Project Filter Tabs
	filterTabs := []string{}
	allLabel := "All Projects"
	if m.projectIdx == 0 {
		filterTabs = append(filterTabs, titleStyle.Render(allLabel))
	} else {
		filterTabs = append(filterTabs, subtleTextStyle.Render("["+allLabel+"]"))
	}
	for i, p := range m.projects {
		rev := formatRevision(p.GitSHA, p.Dirty)
		lbl := fmt.Sprintf("%s@%s (%d)", p.Name, rev, p.TargetCount)
		if p.OpenCrashes > 0 {
			lbl += fmt.Sprintf(" ✖%d", p.OpenCrashes)
		}
		if p.SyncError != "" {
			lbl += " !sync"
		}
		if m.projectIdx == i+1 {
			filterTabs = append(filterTabs, titleStyle.Render(lbl))
		} else {
			filterTabs = append(filterTabs, subtleTextStyle.Render("["+lbl+"]"))
		}
	}
	filter := "Filter: " + strings.Join(filterTabs, "  ")
	sortIndicator := ""
	if m.sortCol != SortNone {
		arrow := "▲"
		if m.sortDesc {
			arrow = "▼"
		}
		sortIndicator = fmt.Sprintf("  •  Sort: %s %s", m.sortCol.Title(), arrow)
	}
	contentWidth := max(1, m.width-6)
	if lipgloss.Width(filter+sortIndicator) > contentWidth {
		label := "All Projects"
		if m.projectIdx > 0 && m.projectIdx <= len(m.projects) {
			label = m.projects[m.projectIdx-1].Name
		}
		filter = "Filter: " + label + "  (p/tab to cycle)"
	}
	if sortIndicator != "" && lipgloss.Width(filter+sortIndicator) <= contentWidth {
		filter += subtleTextStyle.Render(sortIndicator)
	}
	b.WriteString(lipgloss.NewStyle().MaxWidth(contentWidth).Render(filter) + "\n\n")

	// Keep each target on one terminal row. Extra metrics become visible as
	// the terminal grows; target detail retains every field at any width.
	type column struct {
		title string
		width int
	}
	columns := []column{{"STATUS", 7}, {"TARGET", 12}, {"CRASH", 6}}
	if contentWidth >= 60 {
		columns = append(columns[:1], append([]column{{"PROJECT", 14}}, columns[1:]...)...)
	}
	if contentWidth >= 75 {
		columns = append(columns, column{"EXECS/SEC", 10})
	}
	if contentWidth >= 95 {
		columns = append(columns, column{"LAST RUN", 10}, column{"COV %", 7}, column{"COV TREND", 9})
	}
	if contentWidth >= 115 {
		columns = append(columns, column{"CORP", 6}, column{"REVISION", 10})
	}
	used := 2 + 2*(len(columns)-1)
	for _, c := range columns {
		used += c.width
	}
	remaining := max(0, contentWidth-used)
	for i := range columns {
		if columns[i].title == "TARGET" {
			maxTarget := 28
			if contentWidth < 95 {
				maxTarget = 36
			}
			add := min(remaining, max(0, maxTarget-columns[i].width))
			columns[i].width += add
			remaining -= add
		}
	}
	if remaining > 0 {
		for i := range columns {
			if columns[i].title == "PROJECT" {
				add := min(remaining, max(0, 22-columns[i].width))
				columns[i].width += add
				remaining -= add
			}
		}
	}
	renderCells := func(values map[string]string) string {
		cells := make([]string, len(columns))
		for i, c := range columns {
			cells[i] = lipgloss.NewStyle().Width(c.width).MaxWidth(c.width).Render(ansi.Truncate(values[c.title], c.width, "…"))
		}
		return strings.Join(cells, "  ")
	}
	labels := map[string]string{}
	for _, c := range columns {
		label := c.title
		if m.sortCol.Title() == c.title {
			arrow := "▲"
			if m.sortDesc {
				arrow = "▼"
			}
			if lipgloss.Width(c.title)+2 <= c.width {
				label = c.title + " " + arrow
			} else {
				label = c.title + arrow
			}
		}
		labels[c.title] = label
	}
	b.WriteString(headerRowStyle.Render("  "+renderCells(labels)) + "\n")

	if len(m.targets) == 0 {
		if m.loading {
			b.WriteString(subtleTextStyle.Render("\n  Loading targets from API...\n"))
		} else {
			b.WriteString(subtleTextStyle.Render("\n  No fuzz targets discovered yet. Waiting for orchestrator sync...\n"))
		}
	} else {
		activeMap := map[string]bool{}
		for _, job := range m.status.ActiveJobs {
			activeMap[job.Project+"/"+job.Target] = true
		}

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
		// Filter (two lines), table heading and underline, panel borders.
		maxRows := max(1, m.height-reserved-6)
		if len(m.status.ActiveJobs) > 0 {
			maxRows = max(1, maxRows-3)
		}
		start := 0
		if m.selectedIdx >= maxRows {
			start = m.selectedIdx - maxRows + 1
		}
		end := min(len(m.targets), start+maxRows)

		for i := start; i < end; i++ {
			t := m.targets[i]
			cursor := "  "
			if i == m.selectedIdx {
				cursor = "▸ "
			}
			isRunning := activeMap[t.ProjectName+"/"+t.Name]
			badge := RenderStatusBadge(t.Status, isRunning)

			lastRun := "never"
			if t.LastStarted != nil {
				lastRun = formatRelativeTime(*t.LastStarted)
			}

			crashCell := subtleTextStyle.Render("-")
			if t.OpenCrashes > 0 {
				crashCell = statusCrashStyle.Render(fmt.Sprintf("✖ %d", t.OpenCrashes))
			}

			trend := RenderCoverageTrend(t.CoverageTrend, 9)
			rev := formatRevision(t.GitSHA, t.Dirty)

			rowStr := cursor + renderCells(map[string]string{
				"STATUS": badge, "PROJECT": t.ProjectName, "TARGET": t.Name,
				"REVISION": rev, "LAST RUN": lastRun, "EXECS/SEC": FormatExecRate(t.ExecsPerSec),
				"CORP": strconv.Itoa(t.CorpusCount), "COV %": fmt.Sprintf("%.1f%%", t.CoveragePct),
				"COV TREND": trend, "CRASH": crashCell,
			})
			if i == m.selectedIdx {
				b.WriteString(selectedRowStyle.Render(rowStr) + "\n")
			} else {
				b.WriteString(normalRowStyle.Render(rowStr) + "\n")
			}
		}
	}

	// Active Workers Strip
	if len(m.status.ActiveJobs) > 0 {
		var jobs []string
		for _, j := range m.status.ActiveJobs {
			jobs = append(jobs, fmt.Sprintf("[%s] %s/%s@%s (%s)",
				j.WorkerID, j.Project, j.Target, formatRevision(j.GitSHA, j.Dirty), j.FuzzTime))
		}
		b.WriteString("\n" + statusRunningStyle.Render("Active Jobs: ") + strings.Join(jobs, "  •  ") + "\n")
	}

	return panelStyle.Width(max(1, m.width-2)).Render(b.String())
}

func (m Model) renderDetailView() string {
	if m.detailTarget == nil {
		return panelStyle.Render("No target selected.")
	}
	t := m.detailTarget

	var top strings.Builder
	fmt.Fprintf(&top, "Target: %s  •  Project: %s  •  Package: %s  •  Rev: %s  •  Go: %s\n",
		cardValueStyle.Render(t.Name),
		cardValueStyle.Render(t.ProjectName),
		subtleTextStyle.Render(t.Package),
		cardValueStyle.Render(formatRevision(t.GitSHA, t.Dirty)),
		subtleTextStyle.Render(nonEmpty(t.GoVersion, "n/a")),
	)
	fmt.Fprintf(&top, "Status: %s  •  Rate: %s  •  Corpus: %d  •  Coverage: %.1f%%  •  Cov Trend (0-100%%): %s  •  Runs: %d  •  Open Crashes: %d\n",
		RenderStatusBadge(t.Status, false),
		cardValueStyle.Render(FormatExecRate(t.ExecsPerSec)),
		t.CorpusCount,
		t.CoveragePct,
		RenderCoverageTrend(t.CoverageTrend, 10),
		t.TotalRuns,
		t.OpenCrashes,
	)

	// Sparkline + Recent Runs
	sparkBox := panelStyle.Render(m.renderSparklineSummary())

	runsBox := activePanelStyle.Render(
		cardTitleStyle.Render(fmt.Sprintf("Recent Runs (%d)", len(m.detailRuns))) + "\n" + m.runsTable.View(),
	)

	if idx := m.runsTable.Cursor(); idx >= 0 && idx < len(m.detailRuns) {
		if d := m.detailRuns[idx].Diagnostic; d != "" {
			lines := strings.Split(d, "\n")
			if len(lines) > 12 {
				lines = lines[len(lines)-12:]
			}
			runsBox += "\n" + panelStyle.Width(max(1, m.width-2)).Render("Selected run diagnostic (full text: /api/runs)\n"+strings.Join(lines, "\n"))
		}
	}
	var crashB strings.Builder
	crashB.WriteString(cardTitleStyle.Render(fmt.Sprintf("Open Crashes (%d)", len(m.detailCrashes))) + "\n")
	if len(m.detailCrashes) == 0 {
		crashB.WriteString(statusOKStyle.Render("✓ No open crashes for this target."))
	} else {
		ordered := make([]api.Crash, 0, len(m.detailCrashes))
		for _, c := range m.detailCrashes {
			if m.selectedCrashID != 0 && c.ID == m.selectedCrashID {
				ordered = append(ordered, c)
			}
		}
		for _, c := range m.detailCrashes {
			if m.selectedCrashID == 0 || c.ID != m.selectedCrashID {
				ordered = append(ordered, c)
			}
		}
		for idx, c := range ordered {
			if idx >= 3 {
				crashB.WriteString(subtleTextStyle.Render(fmt.Sprintf("\n...and %d more open crashes", len(ordered)-3)))
				break
			}
			bullet := "•"
			if m.selectedCrashID != 0 && c.ID == m.selectedCrashID {
				bullet = "▸"
			}
			fmt.Fprintf(&crashB, "%s [id:%d] %s  fp:%s  rev:%s  artifact: %s  (last seen %s)\n",
				bullet,
				c.ID,
				statusCrashStyle.Render("hash:"+truncate(c.InputHash, 16)),
				subtleTextStyle.Render(truncate(c.StackFingerprint, 12)),
				subtleTextStyle.Render(formatRevision(c.GitSHA, c.Dirty)),
				subtleTextStyle.Render(c.MinimizedInput),
				formatRelativeTime(c.LastSeen),
			)
			if idx == 0 {
				stackLines := strings.Split(strings.TrimSpace(c.Stack), "\n")
				if len(stackLines) > 12 {
					stackLines = append(stackLines[:12], "...(truncated)")
				}
				for _, sl := range stackLines {
					if strings.TrimSpace(sl) == "" {
						continue
					}
					fmt.Fprintf(&crashB, "    %s\n", truncate(sl, max(20, m.width-10)))
				}
			} else {
				fmt.Fprintf(&crashB, "    %s\n", truncate(crashSummaryLine(c.Stack), max(20, m.width-10)))
			}
		}
	}
	crashBox := panelStyle.Width(max(1, m.width-2)).Render(crashB.String())

	middleSection := lipgloss.JoinHorizontal(lipgloss.Top, runsBox, sparkBox)
	if lipgloss.Width(runsBox)+lipgloss.Width(sparkBox) > m.width {
		middleSection = lipgloss.JoinVertical(lipgloss.Left, runsBox, sparkBox)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		panelStyle.Width(max(1, m.width-2)).Render(top.String()),
		middleSection,
		crashBox,
	)
}

func (m Model) renderSparklineSummary() string {
	title := cardTitleStyle.Render(fmt.Sprintf("Coverage & Corpus Trend (%d runs)", len(m.detailHistory)))
	if len(m.detailHistory) == 0 {
		return title + "\n" + subtleTextStyle.Render("No coverage history recorded yet.")
	}
	first := m.detailHistory[0]
	last := m.detailHistory[len(m.detailHistory)-1]
	minCov, maxCov := first.CoveragePct, first.CoveragePct
	for _, pt := range m.detailHistory {
		if pt.CoveragePct < minCov {
			minCov = pt.CoveragePct
		}
		if pt.CoveragePct > maxCov {
			maxCov = pt.CoveragePct
		}
	}
	deltaCov := last.CoveragePct - first.CoveragePct
	deltaCorp := last.CorpusCount - first.CorpusCount
	statsLine := fmt.Sprintf("Now: %.1f%% (%+.1f%%)  •  Min/Max: %.1f%%–%.1f%%\nCorpus: %d → %d (%+d)  •  %s  %s",
		last.CoveragePct, deltaCov, minCov, maxCov,
		first.CorpusCount, last.CorpusCount, deltaCorp,
		statusOKStyle.Render("● Cov%"),
		statusRunningStyle.Render("● Corp"),
	)
	chartW := 44
	if m.width >= 148 {
		chartW = min(64, m.width-100)
	} else if m.width < 110 {
		chartW = max(24, m.width-10)
	}
	chartView := renderDetailHistoryChart(m.detailHistory, chartW, 6)
	axisLine := subtleTextStyle.Render(fmt.Sprintf("oldest (#%d) ───────────► latest (#%d)", first.RunID, last.RunID))
	return title + "\n" + subtleTextStyle.Render(statsLine) + "\n" + chartView + "\n" + axisLine
}

func (m Model) renderCrashesView() string {
	var b strings.Builder
	title := fmt.Sprintf("OPEN CRASHES ACROSS FLEET (%d) — press 'enter' for target detail", len(m.openCrashes))
	if !m.readOnly {
		title += ", 'x' to mark selected crash fixed"
	}
	contentWidth := max(1, m.width-6)
	b.WriteString(headerRowStyle.Render(ansi.Truncate(title, contentWidth, "…")) + "\n\n")

	if len(m.openCrashes) == 0 {
		b.WriteString(statusOKStyle.Render("  ✓ Zero open crashes across the fleet.\n"))
		return panelStyle.Width(max(1, m.width-2)).Render(b.String())
	}

	for i, c := range m.openCrashes {
		cursor := "  "
		if i == m.crashIdx {
			cursor = "▸ "
		}
		line := fmt.Sprintf("%s[id:%d] %s/%s@%s  hash:%s  fp:%s  seen:%s  input:%s",
			cursor, c.ID, c.ProjectName, c.TargetName,
			formatRevision(c.GitSHA, c.Dirty),
			truncate(c.InputHash, 14),
			truncate(c.StackFingerprint, 10),
			formatRelativeTime(c.LastSeen),
			c.MinimizedInput)
		line = ansi.Truncate(line, contentWidth, "…")
		if i == m.crashIdx {
			b.WriteString(selectedRowStyle.Render(line) + "\n")
			stackPreview := ansi.Truncate("    Stack: "+crashSummaryLine(c.Stack), contentWidth, "…")
			b.WriteString(subtleTextStyle.Render(stackPreview) + "\n")
		} else {
			b.WriteString(normalRowStyle.Render(line) + "\n")
		}
	}
	return panelStyle.Width(max(1, m.width-2)).Render(b.String())
}

func formatRevision(sha string, dirty bool) string {
	if sha == "" {
		return "n/a"
	}
	if runes := []rune(sha); len(runes) > 8 {
		sha = string(runes[:8])
	}
	if dirty {
		return sha + "*"
	}
	return sha
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02")
	}
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

func crashSummaryLine(stack string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(stack), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--- FAIL:") {
			continue
		}
		return trimmed
	}
	return firstLine(stack)
}

func (m Model) keyMap() KeyMap {
	km := NewKeyMap(m.mode, m.hideHelp)
	revisions := m.mode == ViewStats && m.currentStatsQuery().ID == api.StatsQueryRevisions
	km.PagePrev.SetEnabled(revisions)
	km.PageNext.SetEnabled(revisions)
	if m.readOnly {
		km.FixCrash.SetEnabled(false)
	}
	return km
}
