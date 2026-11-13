package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"nimbleterminal.dev/ntfuzz/internal/api"
)

func TestCompactCrashViewWrapsAndKeepsReportPath(t *testing.T) {
	m := NewModelOptions(nil, time.Second, true)
	at := time.Now().UTC().Add(-3 * time.Minute)
	m.status = api.StatusResponse{ReadOnly: true, SnapshotAt: &at, OpenCrashes: 2}
	m.mode = ViewCrashes
	m.openCrashes = []api.Crash{
		{ID: 23, ProjectName: "nt3d", TargetName: "FuzzDecode", Stack: "panic: slice bounds out of range", BlamedModule: "github.com/qmuntal/gltf", BlamedVersion: "v0.29.0", BlamedFile: "decoder.go", BlamedLine: 151},
		{ID: 24, ProjectName: "gloss", TargetName: "FuzzParsePages", Stack: "panic: malformed page"},
	}
	for _, width := range []int{32, 40, 79} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m = next.(Model)
		view := m.View().Content
		for _, want := range []string{"2 open crashes", "Snapshot · read-only", "Crash 1/2 · #23", "/crashes/23", "enter detail"} {
			if !strings.Contains(view, want) {
				t.Errorf("width %d compact crash view missing %q:\n%s", width, want, view)
			}
		}
		if strings.Contains(view, "1 open crashes") {
			t.Errorf("width %d uses incorrect plural:\n%s", width, view)
		}
		if got := lipgloss.Width(view); got > width {
			t.Errorf("width %d compact crash view is %d cells wide:\n%s", width, got, view)
		}
	}

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(Model)
	if view := m.View().Content; !strings.Contains(view, "Crash 2/2 · #24") || !strings.Contains(view, "/crashes/24") {
		t.Fatalf("compact crash navigation did not select the next report:\n%s", view)
	}
}

func TestCompactFleetViewRetainsSnapshotFreshness(t *testing.T) {
	m := NewModelOptions(nil, time.Second, true)
	at := time.Now().UTC().Add(-2 * time.Minute)
	m.status = api.StatusResponse{ReadOnly: true, SnapshotAt: &at, OpenCrashes: 1}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = next.(Model)
	view := m.View().Content
	for _, want := range []string{"1 open crash", "Snapshot · read-only", "2m ago"} {
		if !strings.Contains(view, want) {
			t.Errorf("compact fleet view missing %q:\n%s", want, view)
		}
	}
}

func TestProgramSummaryUsesSingularCrash(t *testing.T) {
	m := NewModel(nil, time.Second)
	m.status = api.StatusResponse{TotalProjects: 1, TotalTargets: 1, OpenCrashes: 1}
	description := DescribeActions(m)
	if !strings.Contains(description.Status.Summary, "1 open crash") || strings.Contains(description.Status.Summary, "1 open crashes") {
		t.Fatalf("unexpected program summary %q", description.Status.Summary)
	}
}

func TestCompactCrashViewPagesShortContent(t *testing.T) {
	for _, size := range []struct{ width, height int }{{32, 7}, {32, 12}, {40, 12}, {79, 12}, {120, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := NewModelOptions(nil, time.Second, true)
			at := time.Now().UTC().Add(-3 * time.Minute)
			m.status = api.StatusResponse{ReadOnly: true, SnapshotAt: &at, OpenCrashes: 1}
			m.mode = ViewCrashes
			m.width, m.height = size.width, size.height
			m.openCrashes = []api.Crash{{
				ID: 23, ProjectName: "nt3d", TargetName: "FuzzDecode",
				Stack: "panic: slice bounds out of range", LastSeen: at,
				BlamedModule: "github.com/qmuntal/gltf", BlamedVersion: "v0.29.0", BlamedFile: "decoder.go", BlamedLine: 151,
			}}
			pages := m.compactCrashPages()
			if size.height == 7 && len(pages) < 2 {
				t.Fatalf("seven-row crash view did not paginate:\n%s", m.View().Content)
			}
			var all strings.Builder
			for page := range pages {
				m.crashPage = page
				view := m.View().Content
				all.WriteString(view + "\n")
				if lipgloss.Width(view) > size.width || lipgloss.Height(view) > size.height {
					t.Fatalf("page %d overflows %dx%d:\n%s", page+1, size.width, size.height, view)
				}
			}
			for _, want := range []string{"Snapshot · read-only", "Blame:", "Last seen:", "/crashes/23", "Stack: panic:"} {
				if !strings.Contains(all.String(), want) {
					t.Errorf("paged crash view lost %q:\n%s", want, all.String())
				}
			}
			if len(pages) > 1 {
				m.crashPage = 0
				next, _ := m.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
				m = next.(Model)
				if m.crashPage != 1 || !strings.Contains(m.View().Content, "Page 2/") {
					t.Fatalf("next-page key did not advance:\n%s", m.View().Content)
				}
				next, _ = m.Update(tea.KeyPressMsg{Code: '[', Text: "["})
				m = next.(Model)
				if m.crashPage != 0 {
					t.Fatal("previous-page key did not return to the first page")
				}
			}
		})
	}
}

func TestCompactCrashDrillDownPagesAllDetailContent(t *testing.T) {
	m := NewModelOptions(nil, time.Second, true)
	m.width, m.height = 40, 7
	m.mode = ViewCrashes
	now := time.Now().UTC()
	m.targets = []api.Target{{
		ID: 82, ProjectName: "ntfuzz", Package: "nimbleterminal.dev/ntfuzz/internal/booba",
		Name: "FuzzValidateAndRenderBranding", Status: "crash", OpenCrashes: 1,
		GitSHA: "1d257f2c", GoVersion: "go1.27.3", CoveragePct: 72.5, CorpusCount: 9,
	}}
	m.openCrashes = []api.Crash{{
		ID: 6, TargetID: 82, ProjectName: "ntfuzz", Package: "nimbleterminal.dev/ntfuzz/internal/booba",
		TargetName: "FuzzValidateAndRenderBranding", FirstSeen: now, LastSeen: now,
		Stack:     "panic: invalid branding\nfirst-stack-frame\nfinal-stack-frame",
		InputHash: "2ecfc5401aa9973e", StackFingerprint: "1832bb5bf3bf1db6", GitSHA: "1d257f2c",
	}}

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.mode != ViewDetail || !m.compactDetail() {
		t.Fatalf("crash drill-down did not open compact detail: mode=%v", m.mode)
	}
	if first := m.View().Content; !strings.Contains(first, "Crash #6") || !strings.Contains(first, "/crashes/6") {
		t.Fatalf("drill-down did not prioritize the selected crash:\n%s", first)
	}
	m.detailRuns = []api.Run{{
		ID: 91, TargetID: 82, Started: now, Status: "crash", DurationSec: 1.5,
		Execs: 1200, ExecsPerSec: 800, CoveragePct: 72.5, CorpusCount: 9,
		Diagnostic: "diagnostic-first-line\ndiagnostic-final-line", Worker: "local-1",
	}}
	m.detailHistory = []api.CoveragePoint{{RunID: 90, CoveragePct: 60, CorpusCount: 4}, {RunID: 91, CoveragePct: 72.5, CorpusCount: 9}}
	m.syncDetailWidgets()

	pages := m.compactDetailPages()
	if len(pages) < 2 {
		t.Fatalf("short detail view did not paginate:\n%s", m.View().Content)
	}
	var all strings.Builder
	for page := range pages {
		m.detailPage = page
		view := m.View().Content
		all.WriteString(view + "\n")
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
			t.Fatalf("detail page %d overflows %dx%d:\n%s", page+1, m.width, m.height, view)
		}
	}
	for _, want := range []string{
		"final-stack-frame", "2ecfc5401aa9973e", "1832bb5bf3bf1db6",
		"nimbleterminal.dev/ntfuzz/internal/booba", "Coverage history · 2 runs",
		"Run #91", "diagnostic-final-line",
	} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("paged detail view lost %q:\n%s", want, all.String())
		}
	}
	m.detailPage = 0
	next, _ = m.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	m = next.(Model)
	if m.detailPage != 1 {
		t.Fatal("detail next-page key did not advance")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.mode != ViewCrashes {
		t.Fatalf("back from detail returned to %v instead of crashes", m.mode)
	}
}

func TestCompactCrashAndDetailPageActionsEnabled(t *testing.T) {
	for _, mode := range []ViewMode{ViewCrashes, ViewDetail} {
		m := NewModel(nil, time.Second)
		m.mode = mode
		m.width, m.height = 40, 12
		km := m.keyMap()
		if !km.PagePrev.Enabled() || !km.PageNext.Enabled() {
			t.Fatalf("%v paging actions are disabled", mode)
		}
	}
}

func TestCompactDetailLimitsRunHistory(t *testing.T) {
	m := NewModel(nil, time.Second)
	m.mode = ViewDetail
	m.width, m.height = 40, 7
	m.detailTarget = &api.Target{ID: 82, Name: "FuzzDecode"}
	for i := 1; i <= 7; i++ {
		m.detailRuns = append(m.detailRuns, api.Run{ID: int64(i), TargetID: 82})
	}
	all := strings.Join(m.compactDetailPages(), "\n")
	for _, want := range []string{"Run #1", "Run #5", "2 more runs: /api/runs?target=82"} {
		if !strings.Contains(all, want) {
			t.Errorf("compact run history missing %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Run #6") {
		t.Fatalf("compact run history did not stop at five runs:\n%s", all)
	}
}
