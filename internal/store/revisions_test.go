package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRevisionRecoveryLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().UTC().Add(-time.Minute)
	in := ProjectSyncInput{Name: "repo", Repo: "https://example.test/repo", GitSHA: "old", SyncedAt: &at}
	pid, err := s.SyncProject(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := s.UpsertTarget(ctx, pid, "example.test/repo", "FuzzX")
	if err != nil {
		t.Fatal(err)
	}
	cid, err := s.RecordCrash(ctx, Crash{TargetID: tid, InputHash: "hash", GitSHA: "old", FirstSeen: at})
	if err != nil {
		t.Fatal(err)
	}
	in.GitSHA = "new"
	at = at.Add(time.Second)
	in.SyncedAt = &at
	if _, err = s.SyncProject(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncProject(ctx, in); err != nil {
		t.Fatal(err)
	}
	// Failed pulls must not invent a detection event.
	failed := in
	failed.GitSHA = "failed"
	failed.SyncedAt = nil
	failed.SyncError = "offline"
	if _, err = s.SyncProject(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncProject(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InsertRun(ctx, Run{TargetID: tid, GitSHA: "new", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InsertRun(ctx, Run{TargetID: tid, GitSHA: "new", Status: "ok", Dirty: true}); err != nil {
		t.Fatal(err)
	}
	stats, err := s.GetRevisionStats(ctx, "24h", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Revisions) != 2 || stats.Revisions[0].Baseline || !stats.Revisions[1].Baseline || stats.Revisions[0].PreviousSHA != "old" || stats.Revisions[0].OKRuns != 1 || stats.Revisions[0].ReplayPending != 1 {
		t.Fatalf("bad stats: %+v", stats.Revisions)
	}
	detected, tested := 0, 0
	for _, b := range stats.Buckets {
		detected += b.DetectedRevisions
		tested += b.TestedRevisions
	}
	if detected != 1 || tested != 1 {
		t.Fatalf("counts %d/%d", detected, tested)
	}
	crashes, err := s.ListCrashes(ctx, true, "")
	if err != nil || len(crashes) != 1 {
		t.Fatalf("ordinary passing run closed crash: %v %v", crashes, err)
	}
	c := crashes[0]
	for _, outcome := range []string{"blocked", "failed", "passed"} {
		r := CrashReplay{GitSHA: "new", CheckedAt: time.Now().UTC(), Outcome: outcome}
		if err = s.RecordReplay(ctx, c, r); err != nil {
			t.Fatal(err)
		}
		open, err := s.ListCrashes(ctx, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if (len(open) == 0) != (outcome == "passed") {
			t.Fatalf("%s produced %d open crashes", outcome, len(open))
		}
	}
	// Automatic verification retains the crash's original provenance.
	all, err := s.ListCrashes(ctx, false, "")
	if err != nil || all[0].GitSHA != "old" || all[0].ID != cid {
		t.Fatalf("lost provenance: %+v %v", all, err)
	}
	// A later recurrence reopens the same record.
	if _, err = s.RecordCrash(ctx, Crash{TargetID: tid, InputHash: "hash", GitSHA: "new"}); err != nil {
		t.Fatal(err)
	}
	open, err := s.ListCrashes(ctx, true, "")
	if err != nil || len(open) != 1 {
		t.Fatalf("recurrence not reopened: %+v %v", open, err)
	}
	// Aggregates survive raw run pruning/deletion.
	if _, err = s.db.Exec(`DELETE FROM runs`); err != nil {
		t.Fatal(err)
	}
	stats, err = s.GetRevisionStats(ctx, "24h", "repo")
	if err != nil || stats.Revisions[0].OKRuns != 1 {
		t.Fatalf("lost durable totals: %+v %v", stats, err)
	}
	// The public reader must support the same correlated queries and nullable timestamps.
	dir := t.TempDir()
	snap, err := s.PublishSnapshot(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(filepath.Join(dir, snap.File))
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	visible, err := ro.GetRevisionStats(ctx, "24h", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(visible.Revisions) != 2 || visible.Revisions[0].OKRuns != 1 {
		t.Fatalf("snapshot mismatch: %+v", visible)
	}
	replays, err := ro.ListCrashReplays(ctx)
	if err != nil || len(replays) != 1 {
		t.Fatalf("replays: %+v %v", replays, err)
	}
	if err = ro.RecordReplay(ctx, c, CrashReplay{Outcome: "passed"}); err != ErrReadOnly {
		t.Fatalf("read-only mutation: %v", err)
	}
}

func TestRevisionMigrationFromV3(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.duckdb")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	p, err := s.SyncProject(ctx, ProjectSyncInput{Name: "existing", Repo: "local", GitSHA: "abc", SyncedAt: &now})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.UpsertTarget(ctx, p, "local", "FuzzX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.InsertRun(ctx, Run{TargetID: target, GitSHA: "abc", Status: "ok", Started: now}); err != nil {
		t.Fatal(err)
	}
	// Restore the exact v3 table set while preserving the existing fleet/run data.
	if _, err = s.db.Exec(`DROP TABLE revisions; DROP TABLE revision_targets; DROP TABLE crash_replays; DELETE FROM schema_migrations WHERE version=4`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stats, err := s.GetRevisionStats(ctx, "24h", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Revisions) != 1 {
		t.Fatal(stats)
	}
	v := stats.Revisions[0]
	if !v.Baseline || v.DetectedAt.Before(before) || v.OKRuns != 1 || v.FirstRun == nil || !v.FirstRun.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("incorrect baseline migration: %+v", v)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err = s.GetRevisionStats(ctx, "24h", "")
	if err != nil || len(stats.Revisions) != 1 || stats.Revisions[0].OKRuns != 1 {
		t.Fatalf("migration not idempotent: %+v %v", stats, err)
	}
}
