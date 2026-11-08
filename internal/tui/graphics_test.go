package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
	"nimbleterminal.dev/ntfuzz/internal/api"
)

// Run widget commands like the Tea event loop, including ordered transmission
// before applying Kitty's placeholder grid. Raw messages are captured, not printed.
func drainGraphics(t *testing.T, m Model, cmd tea.Cmd) (Model, string) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	var raw strings.Builder
	for count := 0; len(queue) > 0; count++ {
		if count > 200 {
			t.Fatal("graphics command loop did not settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		value := reflect.ValueOf(msg)
		if value.Kind() == reflect.Slice && value.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			children := []tea.Cmd{}
			for i := 0; i < value.Len(); i++ {
				children = append(children, value.Index(i).Interface().(tea.Cmd))
			}
			queue = append(children, queue...)
			continue
		}
		if r, ok := msg.(tea.RawMsg); ok {
			_, _ = fmt.Fprint(&raw, r.Msg)
			continue
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		queue = append([]tea.Cmd{follow}, queue...)
	}
	return m, raw.String()
}

func TestKittyChartsTransmitAndFallback(t *testing.T) {
	picture.ForceKittyCapability(picture.KittyCapabilitySupported)
	defer picture.ForceKittyCapability(picture.KittyCapabilityUnknown)
	now := time.Now().UTC()
	for i, q := range CannedQueries {
		t.Run(q.ID, func(t *testing.T) {
			m := NewModel(nil, time.Second)
			m.mode = ViewStats
			m.statsQueryIdx = i
			_ = m.toggleGraphics() // do not send a probe in this test
			m.graphicsSupported = true
			m.statsData = api.StatsResponse{Query: q.ID, Window: "24h", Buckets: []api.StatsBucket{
				{BucketStart: now.Add(-time.Hour), OpenCrashes: 22, NewCrashes: 22, DetectedRevisions: 2, TestedRevisions: 1, AvgExecsPerSec: 100, Execs: 1000, AvgCoveragePct: 50, CorpusCount: 12, OKRuns: 10, CrashRuns: 1},
				{BucketStart: now, OpenCrashes: 1, ResolvedCrashes: 21, DetectedRevisions: 3, TestedRevisions: 2, AvgExecsPerSec: 200, Execs: 2000, AvgCoveragePct: 60, CorpusCount: 20, OKRuns: 20},
			}}
			cmd := m.syncGraphics()
			m, raw := drainGraphics(t, m, cmd)
			if m.graphics.Err() != nil {
				t.Fatal(m.graphics.Err())
			}
			if !strings.Contains(raw, "\x1b_G") {
				t.Fatalf("no Kitty transmission for %s", q.ID)
			}
			if !strings.Contains(m.graphOrText("fallback"), "\U0010eeee") {
				t.Fatal("missing Kitty placeholder grid")
			}
			next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 42})
			m = next.(Model)
			m, _ = drainGraphics(t, m, cmd)
			if m.graphics.Err() != nil {
				t.Fatal(m.graphics.Err())
			}
			next, cmd = m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
			m = next.(Model)
			m, raw = drainGraphics(t, m, cmd)
			if m.graphicsRequested || m.graphOrText("fallback") != "fallback" {
				t.Fatal("text toggle failed")
			}
			if !strings.Contains(raw, "\x1b_G") {
				t.Fatal("old Kitty image was not deleted")
			}
		})
	}
}

func TestUnsupportedKittyKeepsTextCharts(t *testing.T) {
	picture.ForceKittyCapability(picture.KittyCapabilityUnsupported)
	defer picture.ForceKittyCapability(picture.KittyCapabilityUnknown)
	m := NewModel(nil, time.Second)
	m.mode = ViewStats
	_ = m.toggleGraphics()
	cmd := m.syncGraphics()
	if cmd != nil {
		t.Fatal("unsupported terminal scheduled graphics")
	}
	if m.graphOrText("text chart") != "text chart" {
		t.Fatal("text fallback lost")
	}
}

func TestKittyCapabilityIsPerTerminal(t *testing.T) {
	picture.ForceKittyCapability(picture.KittyCapabilitySupported)
	defer picture.ForceKittyCapability(picture.KittyCapabilityUnknown)
	m := NewModel(nil, time.Second)
	m.mode = ViewStats
	_ = m.toggleGraphics()
	if cmd := m.syncGraphics(); cmd != nil {
		t.Fatal("another terminal's capability enabled this session")
	}
}

func TestCompactKittyResizeAndSuppression(t *testing.T) {
	picture.ForceKittyCapability(picture.KittyCapabilitySupported)
	defer picture.ForceKittyCapability(picture.KittyCapabilityUnknown)
	m := NewModel(nil, time.Second)
	m.mode = ViewStats
	m.statsQueryIdx = 4
	_ = m.toggleGraphics()
	m.graphicsSupported = true
	m.statsData = api.StatsResponse{Query: api.StatsQueryBurndown, Window: "24h", Buckets: []api.StatsBucket{{BucketStart: time.Now(), OpenCrashes: 5}, {BucketStart: time.Now().Add(time.Hour), OpenCrashes: 2}}}
	for _, size := range [][2]int{{40, 30}, {40, 12}, {120, 45}, {32, 20}} {
		next, cmd := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(Model)
		m, _ = drainGraphics(t, m, cmd)
		if m.compactStats() {
			w, h := m.graphSize()
			if w != size[0] || h != m.compactChartHeight() {
				t.Fatal("renderer budget mismatch")
			}
			if h == 0 && m.graphics.Mode() == picture.PictureKitty {
				t.Fatal("hidden chart still in Kitty mode")
			}
		}
	}
	m.statsQueryIdx = 3
	m.statsData.Query = api.StatsQueryRevisions
	m, _ = drainGraphics(t, m, m.syncGraphics())
	if m.graphics.Mode() == picture.PictureKitty {
		t.Fatal("revision cards should release graphics on a narrow screen")
	}
}
