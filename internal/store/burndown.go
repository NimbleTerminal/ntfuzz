package store

import (
	"context"
	"fmt"
	"time"
)

const crashEventSchema = `
CREATE SEQUENCE IF NOT EXISTS seq_crash_events START 1;
CREATE TABLE IF NOT EXISTS crash_events (
 id BIGINT PRIMARY KEY DEFAULT nextval('seq_crash_events'),
 crash_id BIGINT NOT NULL, event_at TIMESTAMP NOT NULL,
 kind VARCHAR NOT NULL, delta INTEGER NOT NULL, inferred BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS crash_tracking (id INTEGER PRIMARY KEY, started_at TIMESTAMP NOT NULL);
INSERT INTO crash_tracking SELECT 1,(CURRENT_TIMESTAMP AT TIME ZONE 'UTC') ON CONFLICT DO NOTHING;
INSERT INTO crash_events(crash_id,event_at,kind,delta,inferred)
 SELECT id,first_seen,'new',1,true FROM crashes c WHERE NOT EXISTS(SELECT 1 FROM crash_events e WHERE e.crash_id=c.id AND e.kind='new');
INSERT INTO crash_events(crash_id,event_at,kind,delta,inferred)
 SELECT id,fixed_at,'resolved',-1,true FROM crashes c WHERE fixed_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM crash_events e WHERE e.crash_id=c.id AND e.kind='resolved');
`

// GetBurndown retains every recorded transition. Before tracking started, the
// history is reconstructed from first_seen/fixed_at and explicitly labeled.
func (s *Store) GetBurndown(ctx context.Context, window, project, target string) (StatsSeries, error) {
	win, seconds, _, bucketSQL, err := ParseStatsWindow(window)
	if err != nil {
		return StatsSeries{}, err
	}
	durations := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "14d": 14 * 24 * time.Hour}
	now := time.Now().UTC()
	start := now.Add(-durations[win]).Truncate(time.Duration(seconds) * time.Second)
	before := start.Add(-time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := StatsSeries{Query: StatsQueryBurndown, Window: win, BucketSeconds: seconds, Project: project, Target: target, Buckets: []StatsBucket{}}
	var tracking time.Time
	if err = s.queryRow(ctx, `SELECT started_at FROM crash_tracking WHERE id=1`).Scan(&tracking); err != nil {
		return out, err
	}
	out.HistoryEstimatedBefore = &tracking
	rows, err := s.query(ctx, fmt.Sprintf(`SELECT CASE WHEN e.event_at < ? THEN ? ELSE time_bucket(%s,e.event_at) END AS bucket_start,
 SUM(e.delta), COUNT(*) FILTER(WHERE e.kind='new'),COUNT(*) FILTER(WHERE e.kind='reopened'),COUNT(*) FILTER(WHERE e.kind='resolved')
 FROM crash_events e JOIN crashes c ON c.id=e.crash_id JOIN targets t ON t.id=c.target_id JOIN projects p ON p.id=t.project_id
 WHERE e.event_at<=? AND (?='' OR p.name=? OR CAST(p.id AS VARCHAR)=?)
 AND (?='' OR CAST(t.id AS VARCHAR)=? OR t.name=? OR (p.name || '/' || t.name)=?)
 GROUP BY bucket_start ORDER BY bucket_start`, bucketSQL), start, before, now, project, project, project, target, target, target, target)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	buckets := map[int64]StatsBucket{}
	balance := 0
	for rows.Next() {
		var b StatsBucket
		var delta int
		if err = rows.Scan(&b.BucketStart, &delta, &b.NewCrashes, &b.ReopenedCrashes, &b.ResolvedCrashes); err != nil {
			return out, err
		}
		if b.BucketStart.Before(start) {
			balance += delta
			continue
		}
		b.OpenCrashes = delta
		buckets[b.BucketStart.Unix()] = b
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	for at := start; !at.After(now); at = at.Add(time.Duration(seconds) * time.Second) {
		b := buckets[at.Unix()]
		balance += b.OpenCrashes
		b.BucketStart = at
		b.OpenCrashes = balance
		out.NewCrashes += b.NewCrashes
		out.ReopenedCrashes += b.ReopenedCrashes
		out.ResolvedCrashes += b.ResolvedCrashes
		out.Buckets = append(out.Buckets, b)
	}
	out.OpenCrashes = balance
	return out, nil
}
