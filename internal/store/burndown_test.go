package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBurndownLifecycleAndSnapshots(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "events.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p, err := s.UpsertProject(ctx, "demo", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.UpsertTarget(ctx, p, "pkg", "FuzzX")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-30 * time.Minute)
	c := Crash{TargetID: target, FirstSeen: at, LastSeen: at, InputHash: "a", GitSHA: "old"}
	id, err := s.RecordCrash(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	c.ID = id
	// Duplicate observations while open must not inflate the backlog.
	if _, err = s.RecordCrash(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkCrashFixed(ctx, id, at.Add(5*time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkCrashFixed(ctx, id, at.Add(6*time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	c.LastSeen = at.Add(10 * time.Minute)
	c.GitSHA = "new"
	if _, err = s.RecordCrash(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordReplay(ctx, c, CrashReplay{GitSHA: "newer", CheckedAt: at.Add(15 * time.Minute), Outcome: "blocked"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordReplay(ctx, c, CrashReplay{GitSHA: "newer", CheckedAt: at.Add(16 * time.Minute), Outcome: "passed"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordReplay(ctx, c, CrashReplay{GitSHA: "newer", CheckedAt: at.Add(17 * time.Minute), Outcome: "passed"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordReplay(ctx, c, CrashReplay{GitSHA: "next", CheckedAt: at.Add(20 * time.Minute), Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	stats, err := s.GetBurndown(ctx, "1h", "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.OpenCrashes != 1 || stats.NewCrashes != 1 || stats.ReopenedCrashes != 2 || stats.ResolvedCrashes != 2 {
		t.Fatalf("incorrect lifecycle: %+v", stats)
	}
	for _, b := range stats.Buckets {
		if b.OpenCrashes < 0 {
			t.Fatal("negative backlog")
		}
	}
	empty, err := s.GetBurndown(ctx, "1h", "other", "")
	if err != nil || empty.OpenCrashes != 0 {
		t.Fatalf("filter: %+v %v", empty, err)
	}
	dir := t.TempDir()
	snap, err := s.PublishSnapshot(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(filepath.Join(dir, snap.File))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	public, err := ro.GetBurndown(ctx, "1h", "demo", "")
	if err != nil || public.OpenCrashes != 1 || public.ResolvedCrashes != 2 {
		t.Fatalf("snapshot: %+v %v", public, err)
	}
}

func TestBurndownMigrationLabelsReconstructedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.duckdb")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, _ := s.UpsertProject(ctx, "old", "local", "", nil)
	target, _ := s.UpsertTarget(ctx, p, "pkg", "FuzzX")
	at := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 22; i++ {
		id, e := s.RecordCrash(ctx, Crash{TargetID: target, InputHash: time.Duration(i).String(), FirstSeen: at})
		if e != nil {
			t.Fatal(e)
		}
		if i < 21 {
			if e = s.MarkCrashFixed(ctx, id, at.Add(time.Hour), ""); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, err = s.db.Exec(`DROP TABLE crash_events;DROP TABLE crash_tracking;DELETE FROM schema_migrations WHERE version=5`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stats, err := s.GetBurndown(ctx, "24h", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.OpenCrashes != 1 || stats.NewCrashes != 22 || stats.ResolvedCrashes != 21 || stats.HistoryEstimatedBefore == nil {
		t.Fatalf("incorrect reconstruction: %+v", stats)
	}
	peak := 0
	for _, b := range stats.Buckets {
		peak = max(peak, b.OpenCrashes)
	}
	if peak != 22 {
		t.Fatalf("expected 22-to-1 burndown, peak %d", peak)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err = s.GetBurndown(ctx, "24h", "", "")
	if err != nil || stats.OpenCrashes != 1 {
		t.Fatalf("migration duplicated events: %+v %v", stats, err)
	}
}
