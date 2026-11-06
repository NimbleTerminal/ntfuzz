package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"nimbleterminal.dev/booba/actions/bubblehelp"

	"nimbleterminal.dev/ntfuzz/internal/api"
)

func TestModelPureTransitionsAndViews(t *testing.T) {
	m := NewModel(nil, time.Second)

	now := time.Now().UTC()
	next, _ := m.Update(fleetDataMsg{
		status: api.StatusResponse{
			FleetHealth:   "crashes_detected",
			OpenCrashes:   1,
			RunsLast24h:   14,
			TotalProjects: 2,
			TotalTargets:  2,
			ActiveWorkers: 1,
			MaxWorkers:    2,
		},
		projects: []api.Project{
			{ID: 1, Name: "demo-parser", GitSHA: "abc12345", Dirty: false, TargetCount: 1, OpenCrashes: 1},
			{ID: 2, Name: "demo-codec", GitSHA: "def67890", Dirty: true, TargetCount: 1, OpenCrashes: 0, SyncError: "git pull failed"},
		},
		targets: []api.Target{
			{
				ID:            10,
				ProjectID:     1,
				ProjectName:   "demo-parser",
				Package:       "example.com/demo/parser",
				Name:          "FuzzParseExpr",
				LastStarted:   &now,
				ExecsPerSec:   24500,
				CorpusCount:   18,
				CoveragePct:   78.4,
				Status:        "crash",
				GitSHA:        "abc12345",
				GoVersion:     "go1.27.3",
				OpenCrashes:   1,
				TotalRuns:     7,
				CoverageTrend: []float64{60.0, 68.5, 74.1, 78.4},
			},
			{
				ID:            11,
				ProjectID:     2,
				ProjectName:   "demo-codec",
				Package:       "example.com/demo/codec",
				Name:          "FuzzDecodeFrame",
				LastStarted:   &now,
				ExecsPerSec:   41200,
				CorpusCount:   22,
				CoveragePct:   88.0,
				Status:        "ok",
				GitSHA:        "def67890",
				Dirty:         true,
				GoVersion:     "go1.27.3",
				OpenCrashes:   0,
				TotalRuns:     7,
				CoverageTrend: []float64{80.0, 84.0, 88.0},
			},
		},
		crashes: []api.Crash{
			{
				ID:               1,
				TargetID:         10,
				ProjectName:      "demo-parser",
				TargetName:       "FuzzParseExpr",
				FirstSeen:        now,
				LastSeen:         now,
				Stack:            "panic: unexpected token",
				StackFingerprint: "fp1234567890",
				InputHash:        "1122334455667788",
				MinimizedInput:   ".ntfuzz/corpus/demo-parser/crashes/FuzzParseExpr/1122334455667788",
				GitSHA:           "abc12345",
				GoVersion:        "go1.27.3",
			},
		},
	})

	m = next.(Model)
	fleetView := m.View().Content
	if !strings.Contains(fleetView, "FuzzParseExpr") || !strings.Contains(fleetView, "abc12345") || !strings.Contains(fleetView, "git pull failed") {
		t.Fatalf("expected fleet view to contain target, revision, and sync error, got:\n%s", fleetView)
	}

	// Press 'enter' to open target detail view
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.mode != ViewDetail {
		t.Fatalf("expected ViewDetail after enter, got %v", m.mode)
	}

	// Feed detail data
	next, _ = m.Update(detailDataMsg{
		targetID: 10,
		runs: []api.Run{
			{
				ID:             101,
				TargetID:       10,
				Started:        now,
				DurationSec:    5.0,
				Execs:          122500,
				ExecsPerSec:    24500,
				NewInteresting: 3,
				CorpusCount:    18,
				Crashes:        1,
				CoveragePct:    78.4,
				Status:         "crash",
				Worker:         "local-1",
				GitSHA:         "abc12345",
				GoVersion:      "go1.27.3",
			},
		},
		crashes: m.openCrashes,
		history: []api.CoveragePoint{
			{RunID: 100, TargetID: 10, Started: now.Add(-time.Minute), CoveragePct: 68.5, CorpusCount: 15},
			{RunID: 101, TargetID: 10, Started: now, CoveragePct: 78.4, CorpusCount: 18},
		},
	})
	m = next.(Model)
	detailView := m.View().Content
	if !strings.Contains(detailView, "Recent Runs") || !strings.Contains(detailView, "1122334455667788") {
		t.Fatalf("expected detail view to render runs and crashes, got:\n%s", detailView)
	}

	// Press 'c' to switch to crashes view
	next, _ = m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = next.(Model)
	if m.mode != ViewCrashes {
		t.Fatalf("expected ViewCrashes after 'c', got %v", m.mode)
	}
	crashesView := m.View().Content
	if !strings.Contains(crashesView, "OPEN CRASHES ACROSS FLEET") {
		t.Fatalf("expected crashes view header, got:\n%s", crashesView)
	}

	// Press 'esc' to return to fleet view
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.mode != ViewFleet {
		t.Fatalf("expected ViewFleet after Escape, got %v", m.mode)
	}
}

func TestBoobaTerminalActionsSnapshot(t *testing.T) {
	for _, mode := range []ViewMode{ViewFleet, ViewDetail, ViewCrashes} {
		km := NewKeyMap(mode, false)
		snap, diags, err := bubblehelp.Snapshot(km, "ntfuzz", mode.ContextName(), 1, bubblehelp.Options{})
		if err != nil {
			t.Fatalf("bubblehelp.Snapshot(%s) failed: %v (diags=%+v)", mode.ContextName(), err, diags)
		}
		if len(diags) > 0 {
			t.Fatalf("unexpected key diagnostics for mode %s: %+v", mode.ContextName(), diags)
		}
		if len(snap.Actions) == 0 {
			t.Fatalf("expected non-empty actions for mode %s", mode.ContextName())
		}
	}

	actionModel, err := NewActionModel(nil, time.Second)
	if err != nil {
		t.Fatalf("NewActionModel failed: %v", err)
	}
	if actionModel == nil {
		t.Fatal("expected non-nil action model")
	}
}

func TestReadOnlyViewerDisablesMutations(t *testing.T) {
	m := NewModelOptions(nil, time.Second, true)
	m.mode = ViewCrashes
	m.openCrashes = []api.Crash{{ID: 1}}
	if m.keyMap().FixCrash.Enabled() {
		t.Fatal("fix key is enabled")
	}
	_, command := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if command != nil {
		t.Fatal("read-only key generated a command")
	}
	if strings.Contains(m.renderCrashesView(), "mark selected crash fixed") {
		t.Fatal("read-only help advertises mutation")
	}
	if !strings.Contains(m.renderHeader(), "read-only") {
		t.Fatal("snapshot mode is not visible")
	}
}

func TestLayoutTracksWindowWidth(t *testing.T) {
	m := NewModel(nil, time.Second)
	for _, width := range []int{130, 74, 60, 163} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		m = next.(Model)
		for name, view := range map[string]string{
			"header":  m.renderHeader(),
			"cards":   m.renderOverviewCards(),
			"fleet":   m.renderFleetView(),
			"crashes": m.renderCrashesView(),
		} {
			if got := lipgloss.Width(view); got > width {
				t.Errorf("%s width = %d after resize to %d", name, got, width)
			}
		}
	}
}

func TestSnapshotHeaderTracksWindowWidth(t *testing.T) {
	m := NewModelOptions(nil, time.Second, true)
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	m.status.SnapshotAt = &at
	for _, width := range []int{130, 74, 60, 40} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		m = next.(Model)
		if got := lipgloss.Width(m.renderHeader()); got > width {
			t.Errorf("snapshot header width=%d after resize to %d", got, width)
		}
	}
}

func TestPopulatedFleetResizesWithoutWrappingRows(t *testing.T) {
	m := NewModelOptions(nil, time.Second, true)
	m.hideHelp = true
	for i := 0; i < 14; i++ {
		m.projects = append(m.projects, api.Project{Name: fmt.Sprintf("project-%02d", i), TargetCount: 5})
	}
	for i := 0; i < 70; i++ {
		m.targets = append(m.targets, api.Target{Name: fmt.Sprintf("FuzzLongParserName%02d", i), ProjectName: "long-project-name", Status: "crash"})
	}
	m.selectedIdx = 30
	for _, width := range []int{160, 100, 60, 40, 120} {
		m.width, m.height = width, 40
		fleet := m.renderFleetView()
		for _, line := range strings.Split(fleet, "\n") {
			if strings.Contains(line, "FuzzLong") && !strings.Contains(line, "CRASH") {
				t.Errorf("width %d: target row wrapped away from its status: %s", width, line)
			}
		}
		if !strings.Contains(fleet, "▸") {
			t.Errorf("width %d: selection disappeared", width)
		}
		if got := lipgloss.Width(fleet); got > width {
			t.Errorf("width %d: fleet width %d", width, got)
		}
		view := m.View().Content
		if got := lipgloss.Height(view); got > m.height {
			t.Errorf("width %d: full view height %d exceeds %d", width, got, m.height)
		}
	}
}

func TestRunDiagnosticView(t *testing.T) {
	m := NewModel(nil, time.Second)
	m.width = 120
	m.detailTarget = &api.Target{ID: 1, Name: "FuzzSub", Status: "missing_target"}
	m.detailRuns = []api.Run{{Status: "missing_target", Diagnostic: "no fuzz tests to fuzz"}}
	m.syncDetailWidgets()
	view := m.renderDetailView()
	if !strings.Contains(view, "no fuzz tests to fuzz") || !strings.Contains(view, "! MISS") {
		t.Fatal(view)
	}
}

func TestStatusBadgesFitStatusColumn(t *testing.T) {
	for _, status := range []string{"ok", "crash", "missing_target", "incomplete", "canceled", "error", "fuzzing", "pending", "snapshot"} {
		if got := lipgloss.Width(RenderStatusBadge(status, false)); got != 7 {
			t.Fatalf("badge for %q is %d columns wide, want 7", status, got)
		}
	}
}

func TestSnapshotOverviewCardsReflectSnapshotHealth(t *testing.T) {
	m := NewModelOptions(nil, time.Second, true)
	at := time.Now().UTC().Add(-3 * time.Minute)
	m.status = api.StatusResponse{ReadOnly: true, SnapshotAt: &at, FleetHealth: "snapshot", OpenCrashes: 1}

	cards := m.renderOverviewCards()
	if strings.Contains(cards, "PEND") || !strings.Contains(cards, "CRASH") {
		t.Fatalf("snapshot with open crashes should show CRASH, not PEND:\n%s", cards)
	}
	if strings.Contains(cards, "Active Workers") || !strings.Contains(cards, "Snapshot Age") || !strings.Contains(cards, "3m") {
		t.Fatalf("read-only cards should replace workers with snapshot age:\n%s", cards)
	}

	m.status.OpenCrashes = 0
	if cards = m.renderOverviewCards(); !strings.Contains(cards, "OK") {
		t.Fatalf("snapshot without crashes should show OK:\n%s", cards)
	}
}

func TestSnapshotBadgeIsNotPending(t *testing.T) {
	badge := RenderStatusBadge("snapshot", false)
	if strings.Contains(badge, "PEND") || lipgloss.Width(badge) != 7 {
		t.Fatalf("badge=%q width=%d", badge, lipgloss.Width(badge))
	}
}

func TestColumnSortingAndDirectionToggle(t *testing.T) {
	m := NewModel(nil, time.Second)
	m.width = 160
	m.height = 40

	t1 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	next, _ := m.Update(fleetDataMsg{
		targets: []api.Target{
			{
				ID:          1,
				ProjectName: "zeta",
				Package:     "example.com/zeta",
				Name:        "FuzzBeta",
				Status:      "ok",
				OpenCrashes: 0,
				ExecsPerSec: 500,
				LastStarted: &t1,
				CoveragePct: 20.0,
				GitSHA:      "bbbb2222",
				CorpusCount: 10,
			},
			{
				ID:          2,
				ProjectName: "alpha",
				Package:     "example.com/alpha",
				Name:        "FuzzAlpha",
				Status:      "crash",
				OpenCrashes: 3,
				ExecsPerSec: 5000,
				LastStarted: &t2,
				CoveragePct: 80.0,
				GitSHA:      "aaaa1111",
				CorpusCount: 90,
			},
		},
	})
	m = next.(Model)

	// Initial order is preserved when SortNone is active.
	if m.targets[0].ID != 1 {
		t.Fatalf("expected initial order to preserve API order (ID 1 first), got ID %d", m.targets[0].ID)
	}

	// 1. Press 's' -> SortStatus (default ▼: crash first).
	next, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	m = next.(Model)
	if m.sortCol != SortStatus || !m.sortDesc || m.targets[0].ID != 2 {
		t.Fatalf("expected SortStatus ▼ with crash target (ID 2) first, got col=%v desc=%v first=%d", m.sortCol, m.sortDesc, m.targets[0].ID)
	}
	if !strings.Contains(m.renderFleetView(), "STATUS▼") {
		t.Fatalf("expected STATUS▼ header indicator in fleet view:\n%s", m.renderFleetView())
	}

	// Press 'S' -> reverse to SortStatus ▲ (ok first).
	next, _ = m.Update(tea.KeyPressMsg{Code: 'S', Text: "S"})
	m = next.(Model)
	if m.sortCol != SortStatus || m.sortDesc || m.targets[0].ID != 1 {
		t.Fatalf("expected SortStatus ▲ with ok target (ID 1) first, got col=%v desc=%v first=%d", m.sortCol, m.sortDesc, m.targets[0].ID)
	}
	if !strings.Contains(m.renderFleetView(), "STATUS▲") {
		t.Fatalf("expected STATUS▲ header indicator in fleet view:\n%s", m.renderFleetView())
	}

	// 2. Press 's' -> SortProject ▲ (alpha first).
	next, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	m = next.(Model)
	if m.sortCol != SortProject || m.sortDesc || m.targets[0].ProjectName != "alpha" {
		t.Fatalf("expected SortProject ▲ with alpha first, got col=%v desc=%v first=%s", m.sortCol, m.sortDesc, m.targets[0].ProjectName)
	}
	if !strings.Contains(m.renderFleetView(), "PROJECT ▲") {
		t.Fatalf("expected PROJECT ▲ header indicator in fleet view:\n%s", m.renderFleetView())
	}

	// Press 'S' -> SortProject ▼ (zeta first).
	next, _ = m.Update(tea.KeyPressMsg{Code: 'S', Text: "S"})
	m = next.(Model)
	if !m.sortDesc || m.targets[0].ProjectName != "zeta" {
		t.Fatalf("expected SortProject ▼ with zeta first, got desc=%v first=%s", m.sortDesc, m.targets[0].ProjectName)
	}

	// Cycle through remaining columns: TARGET, CRASH, EXECS/SEC, LAST RUN, COV %, REVISION, CORP.
	expectedCols := []SortColumn{
		SortTarget,
		SortCrashes,
		SortExecRate,
		SortLastRun,
		SortCoverage,
		SortRevision,
		SortCorpus,
	}
	for _, wantCol := range expectedCols {
		next, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
		m = next.(Model)
		if m.sortCol != wantCol {
			t.Fatalf("expected sortCol %v, got %v", wantCol, m.sortCol)
		}
		if m.targets[0].ID != 2 {
			t.Fatalf("for %v (default desc=%v), expected target ID 2 first, got ID %d", wantCol.Title(), m.sortDesc, m.targets[0].ID)
		}
	}
}

func TestCrashDrillDownToDetailAndBackToCrashes(t *testing.T) {
	m := NewModel(nil, time.Second)
	m.width = 140
	m.height = 40

	now := time.Now().UTC()
	multiLineStack := "--- FAIL: FuzzValidateAndRenderBranding (9.66s)\n    --- FAIL: FuzzValidateAndRenderBranding (0.00s)\n        fuzz_test.go:79: rendered HTML contained unescaped angle brackets\n        branding.go:151: RenderIndexHTML"

	next, _ := m.Update(fleetDataMsg{
		targets: []api.Target{
			{
				ID:          82,
				ProjectID:   18,
				ProjectName: "ntfuzz",
				Package:     "nimbleterminal.dev/ntfuzz/internal/booba",
				Name:        "FuzzValidateAndRenderBranding",
				Status:      "crash",
				OpenCrashes: 1,
				GitSHA:      "1d257f2c",
				GoVersion:   "go1.27.3",
			},
		},
		crashes: []api.Crash{
			{
				ID:               6,
				TargetID:         82,
				ProjectName:      "ntfuzz",
				Package:          "nimbleterminal.dev/ntfuzz/internal/booba",
				TargetName:       "FuzzValidateAndRenderBranding",
				FirstSeen:        now,
				LastSeen:         now,
				Stack:            multiLineStack,
				StackFingerprint: "1832bb5bf3bf1db6",
				InputHash:        "2ecfc5401aa9973e",
				MinimizedInput:   "/corpus/crashes/2ecfc5401aa9973e",
				GitSHA:           "1d257f2c",
				GoVersion:        "go1.27.3",
			},
		},
	})
	m = next.(Model)

	// Switch to Crashes view ('c').
	next, _ = m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = next.(Model)
	if m.mode != ViewCrashes {
		t.Fatalf("expected ViewCrashes, got %v", m.mode)
	}
	crashesView := m.View().Content
	// Crashes view summary skips the outer "--- FAIL:" wrapper to show the actual failure line.
	if !strings.Contains(crashesView, "fuzz_test.go:79: rendered HTML contained unescaped angle brackets") {
		t.Fatalf("expected crash summary in crashes view to show failure line, got:\n%s", crashesView)
	}

	// Press 'enter' on the selected crash to drill down into Target Detail view.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.mode != ViewDetail {
		t.Fatalf("expected ViewDetail after pressing Enter in ViewCrashes, got %v", m.mode)
	}
	if m.detailTarget == nil || m.detailTarget.ID != 82 {
		t.Fatalf("expected detailTarget ID 82, got %+v", m.detailTarget)
	}

	detailView := m.View().Content
	// Detail view must show the full multi-line stack trace panel, including deeper lines.
	for _, want := range []string{
		"FuzzValidateAndRenderBranding",
		"2ecfc5401aa9973e",
		"fuzz_test.go:79: rendered HTML contained unescaped angle brackets",
		"branding.go:151: RenderIndexHTML",
	} {
		if !strings.Contains(detailView, want) {
			t.Fatalf("expected detail view after crash drill-down to contain %q, got:\n%s", want, detailView)
		}
	}

	// Press 'esc' in Detail view -> should return to ViewCrashes (where we drilled down from).
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.mode != ViewCrashes {
		t.Fatalf("expected Escape from crash-drilled Detail view to return to ViewCrashes, got %v", m.mode)
	}

	// Press 'esc' again in ViewCrashes -> returns to ViewFleet.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.mode != ViewFleet {
		t.Fatalf("expected Escape from ViewCrashes to return to ViewFleet, got %v", m.mode)
	}
}
