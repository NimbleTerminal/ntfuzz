package tui

import (
	"charm.land/lipgloss/v2"
	"fmt"
	"nimbleterminal.dev/ntfuzz/internal/api"
	"strings"
	"testing"
	"time"
)

func TestCompactStatsFitAndRecoveryPaging(t *testing.T) {
	for _, width := range []int{32, 40, 48, 79, 120} {
		for _, height := range []int{12, 20, 23, 30, 45} {
			for query := range CannedQueries {
				t.Run(fmt.Sprintf("%dx%d/%d", width, height, query), func(t *testing.T) {
					m := NewModel(nil, time.Second)
					m.mode = ViewStats
					m.width = width
					m.height = height
					m.statsQueryIdx = query
					m.statsData = api.StatsResponse{Query: CannedQueries[query].ID, Window: "24h", Buckets: []api.StatsBucket{{BucketStart: time.Now(), OpenCrashes: 3}, {BucketStart: time.Now().Add(time.Hour), OpenCrashes: 2}}, Revisions: []api.Revision{{Project: "a-long-project-name-with-unicode-界", GitSHA: "1234567890abcdef", ReplayPending: 17, ReplayPassed: 21, SyncError: "upstream unavailable; retry scheduled"}, {Project: "second-repo", GitSHA: "abcdef", ReplayFailed: 3}}}
					if !m.compactStats() {
						return
					}
					pages := []string{m.View().Content}
					if query == 3 {
						pages = nil
						for page := range m.compactRevisionPages() {
							m.revisionPage = page
							pages = append(pages, m.View().Content)
						}
						all := strings.Join(pages, "\n")
						for _, want := range []string{"second-repo", "21 pass", "17 pending", "3 fail", "retry", "scheduled"} {
							if !strings.Contains(all, want) {
								t.Errorf("lost recovery data %q", want)
							}
						}
					}
					for _, view := range pages {
						if lipgloss.Width(view) > width || lipgloss.Height(view) > height {
							t.Fatalf("overflow %dx%d wanted %dx%d:\n%s", lipgloss.Width(view), lipgloss.Height(view), width, height, view)
						}
					}
				})
			}
		}
	}
}

func TestRecordPagesPreserveWrappedContent(t *testing.T) {
	pages := recordPages([]string{"123456789\nabcdefgh", "last"}, 4, 2)
	if got := strings.Join(pages, "|"); got != "1234\n5678|9\nabcd|efgh\nlast" {
		t.Fatal(got)
	}
}
