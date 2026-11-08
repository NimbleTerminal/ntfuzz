package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const revisionSchema = `
CREATE TABLE IF NOT EXISTS revisions (
 project_id BIGINT NOT NULL, git_sha VARCHAR NOT NULL, previous_sha VARCHAR NOT NULL,
 detected_at TIMESTAMP NOT NULL, baseline BOOLEAN NOT NULL,
 PRIMARY KEY(project_id, git_sha)
);
CREATE TABLE IF NOT EXISTS revision_targets (
 project_id BIGINT NOT NULL, git_sha VARCHAR NOT NULL, target_id BIGINT NOT NULL,
 first_run TIMESTAMP NOT NULL, runs BIGINT NOT NULL, ok_runs BIGINT NOT NULL,
 crash_runs BIGINT NOT NULL, error_runs BIGINT NOT NULL,
 PRIMARY KEY(project_id, git_sha, target_id)
);
CREATE TABLE IF NOT EXISTS crash_replays (
 crash_id BIGINT NOT NULL, git_sha VARCHAR NOT NULL, input_hash VARCHAR NOT NULL,
 checked_at TIMESTAMP NOT NULL, outcome VARCHAR NOT NULL, diagnostic VARCHAR NOT NULL,
 PRIMARY KEY(crash_id, git_sha, input_hash)
);`

// Revision is a durable observation, with run totals retained after raw run pruning.
// Baselines are first observations, not claims about when a commit was published.
type Revision struct {
	Project       string     `json:"project"`
	GitSHA        string     `json:"git_sha"`
	PreviousSHA   string     `json:"previous_sha"`
	DetectedAt    time.Time  `json:"detected_at"`
	Baseline      bool       `json:"baseline"`
	Current       bool       `json:"current"`
	LastSynced    *time.Time `json:"last_synced,omitempty"`
	SyncError     string     `json:"sync_error,omitempty"`
	FirstRun      *time.Time `json:"first_run,omitempty"`
	Runs          int        `json:"runs"`
	OKRuns        int        `json:"ok_runs"`
	CrashRuns     int        `json:"crash_runs"`
	ErrorRuns     int        `json:"error_runs"`
	TestedTargets int        `json:"tested_targets"`
	ActiveTargets int        `json:"active_targets"`
	ReplayPassed  int        `json:"replay_passed"`
	ReplayFailed  int        `json:"replay_failed"`
	ReplayBlocked int        `json:"replay_blocked"`
	ReplayPending int        `json:"replay_pending"`
}

func (s *Store) recordRevisionLocked(ctx context.Context, id int64, in ProjectSyncInput) error {
	if in.SyncedAt == nil || in.SyncError != "" || in.GitSHA == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO revisions
 SELECT ?, ?, COALESCE((SELECT git_sha FROM revisions WHERE project_id=? ORDER BY detected_at DESC LIMIT 1), ''), ?,
 NOT EXISTS(SELECT 1 FROM revisions WHERE project_id=?)
 ON CONFLICT DO NOTHING`, id, in.GitSHA, id, in.SyncedAt.UTC(), id)
	return err
}

// GetRevisionStats returns observations in the selected window plus each current
// revision, so quiet repositories retain visible sync health.
// Target filtering is intentionally unsupported: revisions belong to projects.
func (s *Store) GetRevisionStats(ctx context.Context, window, project string) (StatsSeries, error) {
	win, seconds, lookback, _, err := ParseStatsWindow(window)
	if err != nil {
		return StatsSeries{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.query(ctx, fmt.Sprintf(`SELECT p.name, v.git_sha, v.previous_sha, v.detected_at, v.baseline,
 v.git_sha=COALESCE(p.git_sha,''), p.last_synced, COALESCE(p.sync_error,''),
 (SELECT MIN(first_run) FROM revision_targets x WHERE x.project_id=v.project_id AND x.git_sha=v.git_sha),
 COALESCE((SELECT SUM(runs) FROM revision_targets x WHERE x.project_id=v.project_id AND x.git_sha=v.git_sha),0),
 COALESCE((SELECT SUM(ok_runs) FROM revision_targets x WHERE x.project_id=v.project_id AND x.git_sha=v.git_sha),0),
 COALESCE((SELECT SUM(crash_runs) FROM revision_targets x WHERE x.project_id=v.project_id AND x.git_sha=v.git_sha),0),
 COALESCE((SELECT SUM(error_runs) FROM revision_targets x WHERE x.project_id=v.project_id AND x.git_sha=v.git_sha),0),
 (SELECT COUNT(*) FROM revision_targets x WHERE x.project_id=v.project_id AND x.git_sha=v.git_sha AND x.ok_runs+x.crash_runs>0),
 (SELECT COUNT(*) FROM targets t WHERE t.project_id=p.id AND t.active),
 (SELECT COUNT(*) FROM crash_replays r JOIN crashes c ON c.id=r.crash_id JOIN targets t ON t.id=c.target_id WHERE t.project_id=p.id AND r.git_sha=v.git_sha AND r.input_hash=c.input_hash AND r.outcome='passed' AND r.checked_at>=c.last_seen),
 (SELECT COUNT(*) FROM crash_replays r JOIN crashes c ON c.id=r.crash_id JOIN targets t ON t.id=c.target_id WHERE t.project_id=p.id AND r.git_sha=v.git_sha AND r.input_hash=c.input_hash AND r.outcome='failed'),
 (SELECT COUNT(*) FROM crash_replays r JOIN crashes c ON c.id=r.crash_id JOIN targets t ON t.id=c.target_id WHERE t.project_id=p.id AND r.git_sha=v.git_sha AND r.input_hash=c.input_hash AND r.outcome='blocked'),
 (SELECT COUNT(*) FROM crashes c JOIN targets t ON t.id=c.target_id WHERE t.project_id=p.id AND COALESCE(c.git_sha,'')<>v.git_sha AND NOT EXISTS(SELECT 1 FROM crash_replays r WHERE r.crash_id=c.id AND r.git_sha=v.git_sha AND r.input_hash=c.input_hash))
 FROM revisions v JOIN projects p ON p.id=v.project_id
 WHERE (v.detected_at >= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC') - %s OR v.git_sha=p.git_sha) AND (?='' OR p.name=? OR CAST(p.id AS VARCHAR)=?)
 ORDER BY v.detected_at DESC, p.name`, lookback), project, project, project)
	if err != nil {
		return StatsSeries{}, err
	}
	defer func() { _ = rows.Close() }()
	out := StatsSeries{Query: StatsQueryRevisions, Window: win, BucketSeconds: seconds, Project: project, Revisions: []Revision{}, Buckets: []StatsBucket{}}
	counts := map[int64]*StatsBucket{}
	for rows.Next() {
		var v Revision
		var synced, first sql.NullTime
		if err := rows.Scan(&v.Project, &v.GitSHA, &v.PreviousSHA, &v.DetectedAt, &v.Baseline, &v.Current, &synced, &v.SyncError, &first, &v.Runs, &v.OKRuns, &v.CrashRuns, &v.ErrorRuns, &v.TestedTargets, &v.ActiveTargets, &v.ReplayPassed, &v.ReplayFailed, &v.ReplayBlocked, &v.ReplayPending); err != nil {
			return StatsSeries{}, err
		}
		v.SyncError = RedactSyncError(v.SyncError)
		if synced.Valid {
			v.LastSynced = &synced.Time
		}
		if first.Valid {
			v.FirstRun = &first.Time
		}
		out.Revisions = append(out.Revisions, v)
		bucket := v.DetectedAt.Unix() / int64(seconds) * int64(seconds)
		if counts[bucket] == nil {
			counts[bucket] = &StatsBucket{BucketStart: time.Unix(bucket, 0).UTC()}
		}
		if !v.Baseline {
			counts[bucket].DetectedRevisions++
			if v.OKRuns+v.CrashRuns > 0 {
				counts[bucket].TestedRevisions++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return StatsSeries{}, err
	}
	// Include empty buckets, so quiet periods are visible rather than interpolated away.
	durations := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "14d": 14 * 24 * time.Hour}
	now := time.Now().UTC()
	start := now.Add(-durations[win]).Unix() / int64(seconds) * int64(seconds)
	for at := start; at <= now.Unix(); at += int64(seconds) {
		b := counts[at]
		if b == nil {
			b = &StatsBucket{BucketStart: time.Unix(at, 0).UTC()}
		}
		out.Buckets = append(out.Buckets, *b)
	}
	return out, nil
}

type CrashReplay struct {
	CrashID    int64     `json:"crash_id"`
	GitSHA     string    `json:"git_sha"`
	InputHash  string    `json:"input_hash"`
	CheckedAt  time.Time `json:"checked_at"`
	Outcome    string    `json:"outcome"`
	Diagnostic string    `json:"diagnostic,omitempty"`
}

// NeedsReplay retries blocked checks after an hour; completed checks are once per input/revision.
func (s *Store) NeedsReplay(ctx context.Context, c Crash, sha string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM crash_replays WHERE crash_id=? AND git_sha=? AND input_hash=? AND (outcome<>'blocked' OR checked_at>(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')-INTERVAL '1 hour')`, c.ID, sha, c.InputHash).Scan(&n)
	return n == 0, err
}

// RecordReplay changes crash state only on an executed reproducer result. Build/missing-input
// errors never close a crash. The transaction preserves the original crash SHA and history.
func (s *Store) RecordReplay(ctx context.Context, c Crash, r CrashReplay) error {
	if s.readonly != nil {
		return ErrReadOnly
	}
	if r.Outcome != "passed" && r.Outcome != "failed" && r.Outcome != "blocked" {
		return fmt.Errorf("invalid replay outcome")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO crash_replays VALUES(?,?,?,?,?,?) ON CONFLICT(crash_id,git_sha,input_hash) DO UPDATE SET checked_at=excluded.checked_at,outcome=excluded.outcome,diagnostic=excluded.diagnostic`, c.ID, r.GitSHA, c.InputHash, r.CheckedAt, r.Outcome, r.Diagnostic)
	if err != nil {
		return err
	}
	switch r.Outcome {
	case "passed":
		_, err = tx.ExecContext(ctx, `INSERT INTO crash_events(crash_id,event_at,kind,delta) SELECT id,CAST(? AS TIMESTAMP),'resolved',-1 FROM crashes WHERE id=? AND input_hash=? AND last_seen<=? AND fixed_at IS NULL`, r.CheckedAt.UTC().Format("2006-01-02 15:04:05.999999"), c.ID, c.InputHash, r.CheckedAt)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE crashes SET fixed_at=COALESCE(fixed_at,?) WHERE id=? AND input_hash=? AND last_seen<=?`, r.CheckedAt, c.ID, c.InputHash, r.CheckedAt)
	case "failed":
		_, err = tx.ExecContext(ctx, `INSERT INTO crash_events(crash_id,event_at,kind,delta) SELECT id,CAST(? AS TIMESTAMP),'reopened',1 FROM crashes WHERE id=? AND input_hash=? AND fixed_at IS NOT NULL`, r.CheckedAt.UTC().Format("2006-01-02 15:04:05.999999"), c.ID, c.InputHash)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE crashes SET fixed_at=NULL WHERE id=? AND input_hash=?`, c.ID, c.InputHash)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListCrashReplays(ctx context.Context) ([]CrashReplay, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.query(ctx, `SELECT crash_id,git_sha,input_hash,checked_at,outcome,diagnostic FROM crash_replays ORDER BY checked_at DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []CrashReplay{}
	for rows.Next() {
		var r CrashReplay
		if err := rows.Scan(&r.CrashID, &r.GitSHA, &r.InputHash, &r.CheckedAt, &r.Outcome, &r.Diagnostic); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
