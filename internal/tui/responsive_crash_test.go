package tui

import (
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
		for _, want := range []string{"2 open crashes", "Snapshot · read-only", "Crash 1/2 · #23", "/crashes/23", "Open the report with Crash"} {
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
