package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"nimbleterminal.dev/ntduckbox"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

// CurrentSchemaVersion tracks the DuckDB schema version.
const CurrentSchemaVersion = 4

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("record not found")

// Store manages persistence of fuzzing projects, targets, runs, and crashes in DuckDB.
type Store struct {
	db        *sql.DB
	readonly  *ntduckbox.DB
	mu        sync.Mutex
	metricsMu sync.RWMutex
	metrics   SnapshotMetricsRecorder
}

// ProjectSyncInput carries repository checkout metadata when syncing a project.
type ProjectSyncInput struct {
	Name      string
	Repo      string
	Branch    string
	GitSHA    string
	Dirty     bool
	SyncedAt  *time.Time
	SyncError string
}

// TargetKey identifies a discovered target within a project.
type TargetKey struct {
	Package string
	Name    string
}

// Project represents a fuzzing project with aggregated status and revision metadata.
type Project struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Repo           string     `json:"repo"`
	Branch         string     `json:"branch,omitempty"`
	GitSHA         string     `json:"git_sha,omitempty"`
	Dirty          bool       `json:"dirty"`
	SyncError      string     `json:"sync_error,omitempty"`
	LastSynced     *time.Time `json:"last_synced,omitempty"`
	TargetCount    int        `json:"target_count"`
	TotalRuns      int        `json:"total_runs"`
	OpenCrashes    int        `json:"open_crashes"`
	LatestStatus   string     `json:"latest_status"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	AvgExecsPerSec float64    `json:"avg_execs_per_sec"`
	AvgCoveragePct float64    `json:"avg_coverage_pct"`
}

// Target represents an active Go fuzz target and its latest run status.
type Target struct {
	ID             int64      `json:"id"`
	ProjectID      int64      `json:"project_id"`
	ProjectName    string     `json:"project_name"`
	Package        string     `json:"package"`
	Name           string     `json:"name"`
	Active         bool       `json:"active"`
	LastRunID      *int64     `json:"last_run_id,omitempty"`
	LastStarted    *time.Time `json:"last_started,omitempty"`
	DurationSec    float64    `json:"duration_sec"`
	Execs          int64      `json:"execs"`
	ExecsPerSec    float64    `json:"execs_per_sec"`
	NewInteresting int        `json:"new_interesting"`
	CorpusCount    int        `json:"corpus_count"`
	Crashes        int        `json:"crashes"`
	CoveragePct    float64    `json:"coverage_pct"`
	Status         string     `json:"status"`
	Worker         string     `json:"worker"`
	GitSHA         string     `json:"git_sha,omitempty"`
	Dirty          bool       `json:"dirty"`
	GoVersion      string     `json:"go_version,omitempty"`
	OpenCrashes    int        `json:"open_crashes"`
	TotalRuns      int        `json:"total_runs"`
	CoverageTrend  []float64  `json:"coverage_trend"`
}

// Run represents a single execution of a fuzz target tied to a code revision.
type Run struct {
	Diagnostic     string    `json:"diagnostic,omitempty"`
	ID             int64     `json:"id"`
	TargetID       int64     `json:"target_id"`
	ProjectName    string    `json:"project_name,omitempty"`
	Package        string    `json:"package,omitempty"`
	TargetName     string    `json:"target_name,omitempty"`
	Started        time.Time `json:"started"`
	DurationSec    float64   `json:"duration_sec"`
	Execs          int64     `json:"execs"`
	ExecsPerSec    float64   `json:"execs_per_sec"`
	NewInteresting int       `json:"new_interesting"`
	CorpusCount    int       `json:"corpus_count"`
	Crashes        int       `json:"crashes"`
	CoveragePct    float64   `json:"coverage_pct"`
	Status         string    `json:"status"`
	Worker         string    `json:"worker"`
	GitSHA         string    `json:"git_sha,omitempty"`
	Dirty          bool      `json:"dirty"`
	GoVersion      string    `json:"go_version,omitempty"`
}

// Crash represents a fuzzing crash record stored in DuckDB.
type Crash struct {
	ID               int64      `json:"id"`
	TargetID         int64      `json:"target_id"`
	ProjectName      string     `json:"project_name,omitempty"`
	Package          string     `json:"package,omitempty"`
	TargetName       string     `json:"target_name,omitempty"`
	FirstSeen        time.Time  `json:"first_seen"`
	LastSeen         time.Time  `json:"last_seen"`
	FixedAt          *time.Time `json:"fixed_at,omitempty"`
	Stack            string     `json:"stack"`
	StackFingerprint string     `json:"stack_fingerprint,omitempty"`
	InputHash        string     `json:"input_hash"`
	MinimizedInput   string     `json:"minimized_input"`
	IssueURL         string     `json:"issue_url"`
	GitSHA           string     `json:"git_sha,omitempty"`
	Dirty            bool       `json:"dirty"`
	GoVersion        string     `json:"go_version,omitempty"`
}

// CoveragePoint represents a point in a target's coverage and corpus history.
type CoveragePoint struct {
	RunID          int64     `json:"run_id"`
	TargetID       int64     `json:"target_id"`
	Started        time.Time `json:"started"`
	CoveragePct    float64   `json:"coverage_pct"`
	ExecsPerSec    float64   `json:"execs_per_sec"`
	NewInteresting int       `json:"new_interesting"`
	CorpusCount    int       `json:"corpus_count"`
	GitSHA         string    `json:"git_sha,omitempty"`
}

// FleetStats summarizes overall store statistics for /api/status.
type FleetStats struct {
	FailedTargets int     `json:"failed_targets"`
	TotalProjects int     `json:"total_projects"`
	TotalTargets  int     `json:"total_targets"`
	OpenCrashes   int     `json:"open_crashes"`
	RunsLast24h   int     `json:"runs_last_24h"`
	ExecsLast24h  int64   `json:"execs_last_24h"`
	AvgExecsSec   float64 `json:"avg_execs_per_sec"`
	SchemaVersion int     `json:"schema_version"`
}

// Canned stats query identifiers supported by GetStats and GET /api/stats/{query}.
const (
	StatsQueryThroughput = "throughput"
	StatsQueryCoverage   = "coverage"
	StatsQueryOutcomes   = "outcomes"
	StatsQueryRevisions  = "revisions"
)

// Supported time window identifiers for canned stats queries.
const (
	StatsWindow1h  = "1h"
	StatsWindow6h  = "6h"
	StatsWindow24h = "24h"
	StatsWindow7d  = "7d"
	StatsWindow14d = "14d"
)

// ErrInvalidStatsParam is returned when an unknown stats query or time window is requested.
var ErrInvalidStatsParam = errors.New("invalid stats parameter")

// StatsBucket represents a single time-bucketed aggregation over fuzz runs.
type StatsBucket struct {
	DetectedRevisions int       `json:"detected_revisions"`
	TestedRevisions   int       `json:"tested_revisions"`
	BucketStart       time.Time `json:"bucket_start"`
	Runs              int       `json:"runs"`
	Execs             int64     `json:"execs"`
	DurationSec       float64   `json:"duration_sec"`
	AvgExecsPerSec    float64   `json:"avg_execs_per_sec"`
	PeakExecsSec      float64   `json:"peak_execs_per_sec"`
	AvgCoveragePct    float64   `json:"avg_coverage_pct"`
	MaxCoveragePct    float64   `json:"max_coverage_pct"`
	NewInteresting    int       `json:"new_interesting"`
	CorpusCount       int       `json:"corpus_count"`
	OKRuns            int       `json:"ok_runs"`
	CrashRuns         int       `json:"crash_runs"`
	ErrorRuns         int       `json:"error_runs"`
	IncompleteRuns    int       `json:"incomplete_runs"`
	CanceledRuns      int       `json:"canceled_runs"`
}

// StatsSeries holds time-bucketed metrics and window summary totals for a canned stats query.
type StatsSeries struct {
	Revisions      []Revision    `json:"revisions,omitempty"`
	Query          string        `json:"query"`
	Window         string        `json:"window"`
	BucketSeconds  int           `json:"bucket_seconds"`
	Project        string        `json:"project,omitempty"`
	Target         string        `json:"target,omitempty"`
	TotalRuns      int           `json:"total_runs"`
	TotalExecs     int64         `json:"total_execs"`
	AvgExecsPerSec float64       `json:"avg_execs_per_sec"`
	PeakExecsSec   float64       `json:"peak_execs_per_sec"`
	AvgCoveragePct float64       `json:"avg_coverage_pct"`
	MaxCoveragePct float64       `json:"max_coverage_pct"`
	NewInteresting int           `json:"new_interesting"`
	CorpusCount    int           `json:"corpus_count"`
	OKRuns         int           `json:"ok_runs"`
	CrashRuns      int           `json:"crash_runs"`
	ErrorRuns      int           `json:"error_runs"`
	IncompleteRuns int           `json:"incomplete_runs"`
	CanceledRuns   int           `json:"canceled_runs"`
	Buckets        []StatsBucket `json:"buckets"`
}

// Open opens or creates a DuckDB database at path (use "" or ":memory:" for in-memory)
// and runs versioned schema migrations.
func Open(path string) (*Store, error) {
	if path == ":memory:" {
		path = ""
	}
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("open duckdb %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate duckdb: %w", err)
	}
	return s, nil
}

// Close closes the underlying DuckDB connection.
func (s *Store) Close() error {
	if s != nil && s.readonly != nil {
		return s.readonly.Close()
	}
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SchemaVersion returns the currently applied schema version in DuckDB.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schemaVersionLocked(ctx)
}

func (s *Store) schemaVersionLocked(ctx context.Context) (int, error) {
	var v int
	err := s.queryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// Migrate creates or upgrades all required tables, sequences, indexes, and views using versioned migrations.
func (s *Store) Migrate(ctx context.Context) error {
	if s.readonly != nil {
		return ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL
		);
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	currentVersion, err := s.schemaVersionLocked(ctx)
	if err != nil {
		return fmt.Errorf("check schema version: %w", err)
	}

	type migration struct {
		version int
		stmts   []string
	}

	migrations := []migration{
		{
			version: 1,
			stmts: []string{
				`CREATE SEQUENCE IF NOT EXISTS seq_projects START 1;`,
				`CREATE SEQUENCE IF NOT EXISTS seq_targets START 1;`,
				`CREATE SEQUENCE IF NOT EXISTS seq_runs START 1;`,
				`CREATE SEQUENCE IF NOT EXISTS seq_crashes START 1;`,

				`CREATE TABLE IF NOT EXISTS projects (
					id BIGINT PRIMARY KEY DEFAULT nextval('seq_projects'),
					name VARCHAR NOT NULL UNIQUE,
					repo VARCHAR NOT NULL,
					branch VARCHAR DEFAULT '',
					last_synced TIMESTAMP
				);`,

				`CREATE TABLE IF NOT EXISTS targets (
					id BIGINT PRIMARY KEY DEFAULT nextval('seq_targets'),
					project_id BIGINT NOT NULL,
					package VARCHAR NOT NULL,
					name VARCHAR NOT NULL,
					UNIQUE(project_id, package, name)
				);`,

				`CREATE TABLE IF NOT EXISTS runs (
					id BIGINT PRIMARY KEY DEFAULT nextval('seq_runs'),
					target_id BIGINT NOT NULL,
					started TIMESTAMP NOT NULL,
					duration_sec DOUBLE NOT NULL,
					execs BIGINT NOT NULL,
					execs_per_sec DOUBLE NOT NULL,
					new_interesting INTEGER NOT NULL,
					crashes INTEGER NOT NULL,
					coverage_pct DOUBLE NOT NULL,
					status VARCHAR NOT NULL,
					worker VARCHAR NOT NULL
				);`,

				`CREATE TABLE IF NOT EXISTS crashes (
					id BIGINT PRIMARY KEY DEFAULT nextval('seq_crashes'),
					target_id BIGINT NOT NULL,
					first_seen TIMESTAMP NOT NULL,
					last_seen TIMESTAMP NOT NULL,
					fixed_at TIMESTAMP,
					stack VARCHAR NOT NULL,
					input_hash VARCHAR NOT NULL,
					minimized_input VARCHAR NOT NULL,
					issue_url VARCHAR DEFAULT ''
				);`,

				`CREATE INDEX IF NOT EXISTS idx_targets_project_id ON targets(project_id);`,
				`CREATE INDEX IF NOT EXISTS idx_runs_target_started ON runs(target_id, started);`,
				`CREATE INDEX IF NOT EXISTS idx_crashes_target_id ON crashes(target_id);`,
			},
		},
		{
			version: 2,
			stmts: []string{
				// Revision & sync health columns on projects
				`ALTER TABLE projects ADD COLUMN IF NOT EXISTS git_sha VARCHAR DEFAULT '';`,
				`ALTER TABLE projects ADD COLUMN IF NOT EXISTS dirty BOOLEAN DEFAULT FALSE;`,
				`ALTER TABLE projects ADD COLUMN IF NOT EXISTS sync_error VARCHAR DEFAULT '';`,

				// Active lifecycle column on targets for reconciliation
				`ALTER TABLE targets ADD COLUMN IF NOT EXISTS active BOOLEAN DEFAULT TRUE;`,

				// Revision, toolchain, and corpus size columns on runs
				`ALTER TABLE runs ADD COLUMN IF NOT EXISTS corpus_count INTEGER DEFAULT 0;`,
				`ALTER TABLE runs ADD COLUMN IF NOT EXISTS git_sha VARCHAR DEFAULT '';`,
				`ALTER TABLE runs ADD COLUMN IF NOT EXISTS dirty BOOLEAN DEFAULT FALSE;`,
				`ALTER TABLE runs ADD COLUMN IF NOT EXISTS go_version VARCHAR DEFAULT '';`,

				// Revision, toolchain, and normalized stack fingerprint on crashes
				`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS stack_fingerprint VARCHAR DEFAULT '';`,
				`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS git_sha VARCHAR DEFAULT '';`,
				`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS dirty BOOLEAN DEFAULT FALSE;`,
				`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS go_version VARCHAR DEFAULT '';`,
			},
		},
	}

	migrations = append(migrations, migration{version: 3, stmts: []string{`ALTER TABLE runs ADD COLUMN IF NOT EXISTS diagnostic VARCHAR DEFAULT '';`}})
	now := time.Now().UTC()
	migrations = append(migrations, migration{version: 4, stmts: []string{revisionSchema,
		`INSERT INTO revisions SELECT id,git_sha,'',(CURRENT_TIMESTAMP AT TIME ZONE 'UTC'),true FROM projects WHERE COALESCE(git_sha,'')<>'' ON CONFLICT DO NOTHING`,
		`INSERT INTO revision_targets SELECT t.project_id,r.git_sha,r.target_id,MIN(r.started),COUNT(*),COUNT(*) FILTER(WHERE r.status='ok'),COUNT(*) FILTER(WHERE r.status='crash'),COUNT(*) FILTER(WHERE r.status NOT IN ('ok','crash')) FROM runs r JOIN targets t ON t.id=r.target_id WHERE COALESCE(r.git_sha,'')<>'' AND NOT COALESCE(r.dirty,false) GROUP BY t.project_id,r.git_sha,r.target_id ON CONFLICT DO NOTHING`,
	}})

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}
		for _, stmt := range m.stmts {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration v%d (%q): %w", m.version, stmt, err)
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, now,
		); err != nil {
			return fmt.Errorf("record migration v%d: %w", m.version, err)
		}
	}

	// Always ensure the view reflects the latest schema and folds the last-10 coverage trend into a single query.
	viewStmt := `
		CREATE OR REPLACE VIEW latest_target_status AS
		WITH ranked_runs AS (
			SELECT
				r.id AS run_id,
				r.target_id,
				r.started,
				r.duration_sec,
				r.execs,
				r.execs_per_sec,
				r.new_interesting,
				COALESCE(r.corpus_count, 0) AS corpus_count,
				r.crashes,
				r.coverage_pct,
				r.status,
				r.worker,
				COALESCE(r.git_sha, '') AS git_sha,
				COALESCE(r.dirty, FALSE) AS dirty,
				COALESCE(r.go_version, '') AS go_version,
				ROW_NUMBER() OVER (PARTITION BY r.target_id ORDER BY r.started DESC, r.id DESC) AS rn
			FROM runs r
		),
		run_counts AS (
			SELECT target_id, COUNT(*) AS total_runs
			FROM runs
			GROUP BY target_id
		),
		trend_per_target AS (
			SELECT
				target_id,
				CAST(to_json(LIST(coverage_pct ORDER BY started ASC, run_id ASC)) AS VARCHAR) AS coverage_trend_json
			FROM ranked_runs
			WHERE rn <= 10
			GROUP BY target_id
		),
		open_crash_counts AS (
			SELECT target_id, COUNT(*) AS open_crashes
			FROM crashes
			WHERE fixed_at IS NULL
			GROUP BY target_id
		)
		SELECT
			t.id AS target_id,
			t.project_id,
			p.name AS project_name,
			t.package,
			t.name AS target_name,
			COALESCE(t.active, TRUE) AS active,
			rr.run_id AS last_run_id,
			rr.started AS last_started,
			COALESCE(rr.duration_sec, 0.0) AS duration_sec,
			COALESCE(rr.execs, 0) AS execs,
			COALESCE(rr.execs_per_sec, 0.0) AS execs_per_sec,
			COALESCE(rr.new_interesting, 0) AS new_interesting,
			COALESCE(rr.corpus_count, 0) AS corpus_count,
			COALESCE(rr.crashes, 0) AS crashes,
			COALESCE(rr.coverage_pct, 0.0) AS coverage_pct,
			COALESCE(rr.status, 'pending') AS status,
			COALESCE(rr.worker, '') AS worker,
			COALESCE(rr.git_sha, COALESCE(p.git_sha, '')) AS git_sha,
			COALESCE(rr.dirty, COALESCE(p.dirty, FALSE)) AS dirty,
			COALESCE(rr.go_version, '') AS go_version,
			COALESCE(oc.open_crashes, 0) AS open_crashes,
			COALESCE(rc.total_runs, 0) AS total_runs,
			COALESCE(tr.coverage_trend_json, '[]') AS coverage_trend_json
		FROM targets t
		JOIN projects p ON p.id = t.project_id
		LEFT JOIN ranked_runs rr ON rr.target_id = t.id AND rr.rn = 1
		LEFT JOIN open_crash_counts oc ON oc.target_id = t.id
		LEFT JOIN run_counts rc ON rc.target_id = t.id
		LEFT JOIN trend_per_target tr ON tr.target_id = t.id
		WHERE COALESCE(t.active, TRUE) = TRUE;
	`
	if _, err := s.db.ExecContext(ctx, viewStmt); err != nil {
		return fmt.Errorf("create view latest_target_status: %w", err)
	}
	return nil
}

// UpsertProject creates or updates a project by name (preserving compatibility with simple callers).
func (s *Store) UpsertProject(ctx context.Context, name, repo, branch string, syncedAt *time.Time) (int64, error) {
	return s.SyncProject(ctx, ProjectSyncInput{
		Name:     name,
		Repo:     repo,
		Branch:   branch,
		SyncedAt: syncedAt,
	})
}

// SyncProject records project checkout state, revision (git_sha + dirty), and any sync error.
// If in.SyncedAt is nil (e.g. a failed git pull), last_synced is NOT advanced, and sync_error is recorded.
func (s *Store) SyncProject(ctx context.Context, in ProjectSyncInput) (int64, error) {
	if s.readonly != nil {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE name = ?`, in.Name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var syncArg any
		if in.SyncedAt != nil {
			syncArg = in.SyncedAt.UTC()
		}
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO projects (name, repo, branch, last_synced, git_sha, dirty, sync_error)
			 VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			in.Name, in.Repo, in.Branch, syncArg, in.GitSHA, in.Dirty, in.SyncError,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("insert project %q: %w", in.Name, err)
		}
		return id, s.recordRevisionLocked(ctx, id, in)
	}
	if err != nil {
		return 0, fmt.Errorf("query project %q: %w", in.Name, err)
	}

	if in.SyncedAt != nil {
		_, err = s.db.ExecContext(ctx,
			`UPDATE projects
			 SET repo = ?, branch = ?, last_synced = ?, git_sha = ?, dirty = ?, sync_error = ?
			 WHERE id = ?`,
			in.Repo, in.Branch, in.SyncedAt.UTC(), in.GitSHA, in.Dirty, in.SyncError, id,
		)
	} else {
		// Preserve existing last_synced and git_sha if not provided on a failed pull
		_, err = s.db.ExecContext(ctx,
			`UPDATE projects
			 SET repo = ?, branch = ?,
			     git_sha = CASE WHEN ? != '' THEN ? ELSE git_sha END,
			     dirty = CASE WHEN ? != '' THEN ? ELSE dirty END,
			     sync_error = ?
			 WHERE id = ?`,
			in.Repo, in.Branch, in.GitSHA, in.GitSHA, in.GitSHA, in.Dirty, in.SyncError, id,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("update project %q: %w", in.Name, err)
	}
	return id, s.recordRevisionLocked(ctx, id, in)
}

// UpsertTarget ensures a target (project_id, package, name) exists and is marked active, returning its ID.
func (s *Store) UpsertTarget(ctx context.Context, projectID int64, pkg, name string) (int64, error) {
	if s.readonly != nil {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertTargetLocked(ctx, projectID, pkg, name)
}

func (s *Store) upsertTargetLocked(ctx context.Context, projectID int64, pkg, name string) (int64, error) {
	var id int64
	var active sql.NullBool
	err := s.db.QueryRowContext(ctx,
		`SELECT id, active FROM targets WHERE project_id = ? AND package = ? AND name = ?`,
		projectID, pkg, name,
	).Scan(&id, &active)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO targets (project_id, package, name, active) VALUES (?, ?, ?, TRUE) RETURNING id`,
			projectID, pkg, name,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("insert target %s.%s: %w", pkg, name, err)
		}
		return id, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query target %s.%s: %w", pkg, name, err)
	}
	if !active.Valid || !active.Bool {
		if _, err := s.db.ExecContext(ctx, `UPDATE targets SET active = TRUE WHERE id = ?`, id); err != nil {
			return 0, fmt.Errorf("reactivate target %d: %w", id, err)
		}
	}
	return id, nil
}

// ReconcileProjectTargets upserts all discovered targets for a project as active=TRUE and
// marks any previously stored targets for that project that were not discovered as active=FALSE.
func (s *Store) ReconcileProjectTargets(ctx context.Context, projectID int64, discovered []TargetKey) (map[TargetKey]int64, error) {
	if s.readonly != nil {
		return nil, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make(map[TargetKey]int64, len(discovered))
	activeIDs := make(map[int64]bool, len(discovered))

	for _, tk := range discovered {
		id, err := s.upsertTargetLocked(ctx, projectID, tk.Package, tk.Name)
		if err != nil {
			return nil, err
		}
		ids[tk] = id
		activeIDs[id] = true
	}

	// Find any existing active targets for this project that are no longer present
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM targets WHERE project_id = ? AND COALESCE(active, TRUE) = TRUE`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query project targets for reconciliation: %w", err)
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil && !activeIDs[id] {
			staleIDs = append(staleIDs, id)
		}
	}
	_ = rows.Close()

	for _, staleID := range staleIDs {
		if _, err := s.db.ExecContext(ctx, `UPDATE targets SET active = FALSE WHERE id = ?`, staleID); err != nil {
			return nil, fmt.Errorf("deactivate stale target %d: %w", staleID, err)
		}
	}
	return ids, nil
}

// InsertRun records a completed fuzz run (including revision and Go version) and returns its new run ID.
func (s *Store) InsertRun(ctx context.Context, r Run) (int64, error) {
	if s.readonly != nil {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	started := r.Started
	if started.IsZero() {
		started = time.Now().UTC()
	} else {
		started = started.UTC()
	}
	if r.Status == "" {
		r.Status = "ok"
	}
	if r.Worker == "" {
		r.Worker = "local-0"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO runs (
			target_id, started, duration_sec, execs, execs_per_sec,
			new_interesting, corpus_count, crashes, coverage_pct, status, worker,
			git_sha, dirty, go_version, diagnostic
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		r.TargetID, started, r.DurationSec, r.Execs, r.ExecsPerSec,
		r.NewInteresting, r.CorpusCount, r.Crashes, r.CoveragePct, r.Status, r.Worker,
		r.GitSHA, r.Dirty, r.GoVersion, r.Diagnostic,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert run for target %d: %w", r.TargetID, err)
	}
	if r.GitSHA != "" && !r.Dirty {
		_, err = tx.ExecContext(ctx, `INSERT INTO revision_targets SELECT project_id, ?, id, ?, 1, ?, ?, ? FROM targets WHERE id=?
 ON CONFLICT(project_id,git_sha,target_id) DO UPDATE SET
 first_run=LEAST(revision_targets.first_run,excluded.first_run),runs=revision_targets.runs+1,
 ok_runs=revision_targets.ok_runs+excluded.ok_runs,crash_runs=revision_targets.crash_runs+excluded.crash_runs,error_runs=revision_targets.error_runs+excluded.error_runs`,
			r.GitSHA, started, boolCount(r.Status == "ok"), boolCount(r.Status == "crash"), boolCount(r.Status != "ok" && r.Status != "crash"), r.TargetID)
		if err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// RecordCrash inserts a new crash or updates last_seen if the same (target_id, input_hash)
// or (target_id, stack_fingerprint) already exists.
func (s *Store) RecordCrash(ctx context.Context, c Crash) (int64, error) {
	if s.readonly != nil {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	firstSeen := c.FirstSeen
	if firstSeen.IsZero() {
		firstSeen = now
	} else {
		firstSeen = firstSeen.UTC()
	}
	lastSeen := c.LastSeen
	if lastSeen.IsZero() {
		lastSeen = firstSeen
	} else {
		lastSeen = lastSeen.UTC()
	}

	var existingID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM crashes
		 WHERE target_id = ? AND (input_hash = ? OR (? != '' AND stack_fingerprint = ?))
		 ORDER BY id ASC LIMIT 1`,
		c.TargetID, c.InputHash, c.StackFingerprint, c.StackFingerprint,
	).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		var id int64
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO crashes (
				target_id, first_seen, last_seen, fixed_at, stack, stack_fingerprint,
				input_hash, minimized_input, issue_url, git_sha, dirty, go_version
			) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			c.TargetID, firstSeen, lastSeen, c.Stack, c.StackFingerprint,
			c.InputHash, c.MinimizedInput, c.IssueURL, c.GitSHA, c.Dirty, c.GoVersion,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("insert crash: %w", err)
		}
		return id, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query existing crash: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE crashes
		 SET last_seen = ?,
		     fixed_at = NULL,
		     stack = CASE WHEN ? != '' THEN ? ELSE stack END,
		     stack_fingerprint = CASE WHEN ? != '' THEN ? ELSE stack_fingerprint END,
		     minimized_input = CASE WHEN ? != '' THEN ? ELSE minimized_input END,
		     git_sha = CASE WHEN ? != '' THEN ? ELSE git_sha END,
		     dirty = ?,
		     go_version = CASE WHEN ? != '' THEN ? ELSE go_version END
		 WHERE id = ?`,
		lastSeen,
		c.Stack, c.Stack,
		c.StackFingerprint, c.StackFingerprint,
		c.MinimizedInput, c.MinimizedInput,
		c.GitSHA, c.GitSHA,
		c.Dirty,
		c.GoVersion, c.GoVersion,
		existingID,
	)
	if err != nil {
		return 0, fmt.Errorf("update crash %d: %w", existingID, err)
	}
	return existingID, nil
}

// MarkCrashFixed marks a crash as fixed at fixedAt and optionally sets issueURL.
func (s *Store) MarkCrashFixed(ctx context.Context, crashID int64, fixedAt time.Time, issueURL string) error {
	if s.readonly != nil {
		return ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if fixedAt.IsZero() {
		fixedAt = time.Now().UTC()
	} else {
		fixedAt = fixedAt.UTC()
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE crashes
		 SET fixed_at = ?,
		     issue_url = CASE WHEN ? != '' THEN ? ELSE issue_url END
		 WHERE id = ?`,
		fixedAt, issueURL, issueURL, crashID,
	)
	if err != nil {
		return fmt.Errorf("mark crash %d fixed: %w", crashID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("crash %d: %w", crashID, ErrNotFound)
	}
	return nil
}

// PruneOldRuns deletes raw runs older than retention while preserving at least the most recent
// keepPerTarget runs per target so latest status and 10-point trends remain populated.
func (s *Store) PruneOldRuns(ctx context.Context, retention time.Duration, keepPerTarget int) (int64, error) {
	if s.readonly != nil {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if retention <= 0 {
		retention = 14 * 24 * time.Hour
	}
	if keepPerTarget <= 0 {
		keepPerTarget = 10
	}
	cutoff := time.Now().UTC().Add(-retention)

	res, err := s.db.ExecContext(ctx, `
		DELETE FROM runs
		WHERE started < ?
		  AND id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY target_id ORDER BY started DESC, id DESC) AS rn
				FROM runs
			) ranked
			WHERE rn <= ?
		  )
	`, cutoff, keepPerTarget)
	if err != nil {
		return 0, fmt.Errorf("prune old runs: %w", err)
	}
	deleted, _ := res.RowsAffected()
	return deleted, nil
}

// ListProjects returns all projects with their latest aggregated status and revision summary.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		SELECT
			p.id,
			p.name,
			p.repo,
			COALESCE(p.branch, ''),
			COALESCE(p.git_sha, ''),
			COALESCE(p.dirty, FALSE),
			COALESCE(p.sync_error, ''),
			p.last_synced,
			COUNT(v.target_id) AS target_count,
			COALESCE(SUM(v.total_runs), 0) AS total_runs,
			COALESCE(SUM(v.open_crashes), 0) AS open_crashes,
			MAX(v.last_started) AS last_run_at,
			COALESCE(AVG(CASE WHEN v.execs_per_sec > 0 THEN v.execs_per_sec END), 0.0) AS avg_execs_per_sec,
			COALESCE(AVG(CASE WHEN v.coverage_pct > 0 THEN v.coverage_pct END), 0.0) AS avg_coverage_pct,
			COALESCE(SUM(CASE WHEN v.status NOT IN ('ok', 'pending', 'canceled') THEN 1 ELSE 0 END), 0) AS err_targets,
			COALESCE(SUM(CASE WHEN v.status = 'ok' THEN 1 ELSE 0 END), 0) AS ok_targets
		FROM projects p
		LEFT JOIN latest_target_status v ON v.project_id = p.id
		GROUP BY p.id, p.name, p.repo, p.branch, p.git_sha, p.dirty, p.sync_error, p.last_synced
		ORDER BY p.name ASC;
	`

	rows, err := s.query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []Project
	for rows.Next() {
		var p Project
		var lastSynced sql.NullTime
		var lastRunAt sql.NullTime
		var errTargets, okTargets int
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Repo, &p.Branch, &p.GitSHA, &p.Dirty, &p.SyncError, &lastSynced,
			&p.TargetCount, &p.TotalRuns, &p.OpenCrashes, &lastRunAt,
			&p.AvgExecsPerSec, &p.AvgCoveragePct, &errTargets, &okTargets,
		); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		if lastSynced.Valid {
			t := lastSynced.Time
			p.LastSynced = &t
		}
		if lastRunAt.Valid {
			t := lastRunAt.Time
			p.LastRunAt = &t
		}
		switch {
		case p.OpenCrashes > 0:
			p.LatestStatus = "crash"
		case p.SyncError != "" || errTargets > 0:
			p.LatestStatus = "error"
		case okTargets > 0:
			p.LatestStatus = "ok"
		default:
			p.LatestStatus = "pending"
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []Project{}
	}
	return result, nil
}

// ListTargets returns active targets with their latest status and recent coverage trend in a single query.
func (s *Store) ListTargets(ctx context.Context, projectFilter string) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	baseQuery := `
		SELECT
			target_id, project_id, project_name, package, target_name, active,
			last_run_id, last_started, duration_sec, execs, execs_per_sec,
			new_interesting, corpus_count, crashes, coverage_pct, status, worker,
			git_sha, dirty, go_version, open_crashes, total_runs, coverage_trend_json
		FROM latest_target_status
	`
	var args []any
	if strings.TrimSpace(projectFilter) != "" {
		baseQuery += ` WHERE project_name = ? OR CAST(project_id AS VARCHAR) = ?`
		args = append(args, projectFilter, projectFilter)
	}
	baseQuery += ` ORDER BY project_name ASC, package ASC, target_name ASC`

	rows, err := s.query(ctx, baseQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var targets []Target
	for rows.Next() {
		var t Target
		var lastRunID sql.NullInt64
		var lastStarted sql.NullTime
		var trendJSON string
		if err := rows.Scan(
			&t.ID, &t.ProjectID, &t.ProjectName, &t.Package, &t.Name, &t.Active,
			&lastRunID, &lastStarted, &t.DurationSec, &t.Execs, &t.ExecsPerSec,
			&t.NewInteresting, &t.CorpusCount, &t.Crashes, &t.CoveragePct, &t.Status, &t.Worker,
			&t.GitSHA, &t.Dirty, &t.GoVersion, &t.OpenCrashes, &t.TotalRuns, &trendJSON,
		); err != nil {
			return nil, fmt.Errorf("scan target: %w", err)
		}
		if lastRunID.Valid {
			v := lastRunID.Int64
			t.LastRunID = &v
		}
		if lastStarted.Valid {
			tm := lastStarted.Time
			t.LastStarted = &tm
		}
		t.CoverageTrend = []float64{}
		if trendJSON != "" && trendJSON != "[]" {
			_ = json.Unmarshal([]byte(trendJSON), &t.CoverageTrend)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if targets == nil {
		targets = []Target{}
	}
	return targets, nil
}

// ListRuns returns recent runs, optionally filtered by target (ID or name) and capped at limit.
func (s *Store) ListRuns(ctx context.Context, targetFilter string, limit int) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	query := `
		SELECT
			r.id, r.target_id, p.name AS project_name, t.package, t.name AS target_name,
			r.started, r.duration_sec, r.execs, r.execs_per_sec,
			r.new_interesting, COALESCE(r.corpus_count, 0), r.crashes, r.coverage_pct, r.status, r.worker,
			COALESCE(r.git_sha, ''), COALESCE(r.dirty, FALSE), COALESCE(r.go_version, ''), COALESCE(r.diagnostic, '')
		FROM runs r
		JOIN targets t ON t.id = r.target_id
		JOIN projects p ON p.id = t.project_id
	`
	var args []any
	if strings.TrimSpace(targetFilter) != "" {
		query += ` WHERE CAST(r.target_id AS VARCHAR) = ? OR t.name = ? OR (p.name || '/' || t.name) = ?`
		args = append(args, targetFilter, targetFilter, targetFilter)
	}
	query += fmt.Sprintf(` ORDER BY r.started DESC, r.id DESC LIMIT %d`, limit)

	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var runs []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(
			&r.ID, &r.TargetID, &r.ProjectName, &r.Package, &r.TargetName,
			&r.Started, &r.DurationSec, &r.Execs, &r.ExecsPerSec,
			&r.NewInteresting, &r.CorpusCount, &r.Crashes, &r.CoveragePct, &r.Status, &r.Worker,
			&r.GitSHA, &r.Dirty, &r.GoVersion, &r.Diagnostic,
		); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if runs == nil {
		runs = []Run{}
	}
	return runs, nil
}

// ListCrashes returns crashes, optionally filtering to open crashes (fixed_at IS NULL) and/or a specific target.
func (s *Store) ListCrashes(ctx context.Context, openOnly bool, targetFilter string) ([]Crash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		SELECT
			c.id, c.target_id, p.name AS project_name, t.package, t.name AS target_name,
			c.first_seen, c.last_seen, c.fixed_at, c.stack, COALESCE(c.stack_fingerprint, ''),
			c.input_hash, c.minimized_input, COALESCE(c.issue_url, ''),
			COALESCE(c.git_sha, ''), COALESCE(c.dirty, FALSE), COALESCE(c.go_version, '')
		FROM crashes c
		JOIN targets t ON t.id = c.target_id
		JOIN projects p ON p.id = t.project_id
	`
	var clauses []string
	var args []any
	if openOnly {
		clauses = append(clauses, `c.fixed_at IS NULL`)
	}
	if strings.TrimSpace(targetFilter) != "" {
		clauses = append(clauses, `(CAST(c.target_id AS VARCHAR) = ? OR t.name = ? OR (p.name || '/' || t.name) = ?)`)
		args = append(args, targetFilter, targetFilter, targetFilter)
	}
	if len(clauses) > 0 {
		query += ` WHERE ` + strings.Join(clauses, ` AND `)
	}
	query += ` ORDER BY c.last_seen DESC, c.id DESC`

	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list crashes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var crashes []Crash
	for rows.Next() {
		var c Crash
		var fixedAt sql.NullTime
		if err := rows.Scan(
			&c.ID, &c.TargetID, &c.ProjectName, &c.Package, &c.TargetName,
			&c.FirstSeen, &c.LastSeen, &fixedAt, &c.Stack, &c.StackFingerprint,
			&c.InputHash, &c.MinimizedInput, &c.IssueURL,
			&c.GitSHA, &c.Dirty, &c.GoVersion,
		); err != nil {
			return nil, fmt.Errorf("scan crash: %w", err)
		}
		if fixedAt.Valid {
			tm := fixedAt.Time
			c.FixedAt = &tm
		}
		crashes = append(crashes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if crashes == nil {
		crashes = []Crash{}
	}
	return crashes, nil
}

// GetCoverageHistory returns chronological coverage and corpus points for a target.
func (s *Store) GetCoverageHistory(ctx context.Context, targetFilter string, limit int) ([]CoveragePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > 500 {
		limit = 50
	}

	query := `
		SELECT run_id, target_id, started, coverage_pct, execs_per_sec, new_interesting, corpus_count, git_sha
		FROM (
			SELECT
				r.id AS run_id,
				r.target_id,
				r.started,
				r.coverage_pct,
				r.execs_per_sec,
				r.new_interesting,
				COALESCE(r.corpus_count, 0) AS corpus_count,
				COALESCE(r.git_sha, '') AS git_sha
			FROM runs r
			JOIN targets t ON t.id = r.target_id
			JOIN projects p ON p.id = t.project_id
			WHERE CAST(r.target_id AS VARCHAR) = ? OR t.name = ? OR (p.name || '/' || t.name) = ?
			ORDER BY r.started DESC, r.id DESC
			LIMIT ?
		) sub
		ORDER BY started ASC, run_id ASC
	`

	rows, err := s.query(ctx, query, targetFilter, targetFilter, targetFilter, limit)
	if err != nil {
		return nil, fmt.Errorf("get coverage history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var points []CoveragePoint
	for rows.Next() {
		var pt CoveragePoint
		if err := rows.Scan(&pt.RunID, &pt.TargetID, &pt.Started, &pt.CoveragePct, &pt.ExecsPerSec, &pt.NewInteresting, &pt.CorpusCount, &pt.GitSHA); err != nil {
			return nil, fmt.Errorf("scan coverage point: %w", err)
		}
		points = append(points, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if points == nil {
		points = []CoveragePoint{}
	}
	return points, nil
}

// GetFleetStats returns summary counts for projects, active targets, open crashes, and runs in the last 24h.
func (s *Store) GetFleetStats(ctx context.Context) (FleetStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var stats FleetStats
	since := time.Now().UTC().Add(-24 * time.Hour)

	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM projects`).Scan(&stats.TotalProjects); err != nil {
		return stats, err
	}
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM targets WHERE COALESCE(active, TRUE) = TRUE`).Scan(&stats.TotalTargets); err != nil {
		return stats, err
	}
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM crashes WHERE fixed_at IS NULL`).Scan(&stats.OpenCrashes); err != nil {
		return stats, err
	}
	if err := s.queryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(execs), 0), COALESCE(AVG(CASE WHEN execs_per_sec > 0 THEN execs_per_sec END), 0.0) FROM runs WHERE started >= ?`,
		since,
	).Scan(&stats.RunsLast24h, &stats.ExecsLast24h, &stats.AvgExecsSec); err != nil {
		return stats, err
	}
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM latest_target_status WHERE status NOT IN ('ok', 'pending', 'canceled')`).Scan(&stats.FailedTargets); err != nil {
		return stats, err
	}
	stats.SchemaVersion, _ = s.schemaVersionLocked(ctx)
	return stats, nil
}

// ParseStatsQuery validates and normalizes a canned stats query identifier.
func ParseStatsQuery(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", StatsQueryThroughput:
		return StatsQueryThroughput, nil
	case StatsQueryCoverage:
		return StatsQueryCoverage, nil
	case StatsQueryOutcomes:
		return StatsQueryOutcomes, nil
	case StatsQueryRevisions:
		return StatsQueryRevisions, nil
	default:
		return "", fmt.Errorf("%w: unknown query %q (expected throughput, coverage, outcomes, or revisions)", ErrInvalidStatsParam, raw)
	}
}

// ParseStatsWindow validates and normalizes a time window identifier, returning the canonical
// window string, bucket size in seconds, lookback SQL interval literal, and bucket SQL interval literal.
func ParseStatsWindow(raw string) (window string, bucketSeconds int, lookbackSQL, bucketSQL string, err error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case StatsWindow1h:
		return StatsWindow1h, 300, "INTERVAL '1 hour'", "INTERVAL '5 minutes'", nil
	case StatsWindow6h:
		return StatsWindow6h, 900, "INTERVAL '6 hours'", "INTERVAL '15 minutes'", nil
	case "", StatsWindow24h:
		return StatsWindow24h, 3600, "INTERVAL '24 hours'", "INTERVAL '1 hour'", nil
	case StatsWindow7d:
		return StatsWindow7d, 21600, "INTERVAL '7 days'", "INTERVAL '6 hours'", nil
	case StatsWindow14d:
		return StatsWindow14d, 43200, "INTERVAL '14 days'", "INTERVAL '12 hours'", nil
	default:
		return "", 0, "", "", fmt.Errorf("%w: unknown window %q (expected 1h, 6h, 24h, 7d, or 14d)", ErrInvalidStatsParam, raw)
	}
}

// GetStats executes a canned time-series aggregation query ("throughput", "coverage", or "outcomes")
// over fuzz runs within the requested window ("1h", "6h", "24h", "7d", "14d"), optionally filtered by
// project (name or ID) and/or target (ID, name, or project/name).
func (s *Store) GetStats(ctx context.Context, queryName, windowName, projectFilter, targetFilter string) (StatsSeries, error) {
	qName, err := ParseStatsQuery(queryName)
	if err != nil {
		return StatsSeries{}, err
	}
	if qName == StatsQueryRevisions {
		if targetFilter != "" {
			return StatsSeries{}, fmt.Errorf("%w: revisions use project filtering", ErrInvalidStatsParam)
		}
		return s.GetRevisionStats(ctx, windowName, projectFilter)
	}
	win, bucketSec, lookbackSQL, bucketSQL, err := ParseStatsWindow(windowName)
	if err != nil {
		return StatsSeries{}, err
	}

	proj := strings.TrimSpace(projectFilter)
	tgt := strings.TrimSpace(targetFilter)

	s.mu.Lock()
	defer s.mu.Unlock()

	sqlQuery := fmt.Sprintf(`
		SELECT
			time_bucket(%s, r.started) AS bucket_start,
			CAST(COUNT(*) AS BIGINT) AS runs,
			CAST(COALESCE(SUM(r.execs), 0) AS BIGINT) AS execs,
			COALESCE(SUM(r.duration_sec), 0.0) AS duration_sec,
			COALESCE(AVG(CASE WHEN r.execs_per_sec > 0 THEN r.execs_per_sec END), 0.0) AS avg_execs_per_sec,
			COALESCE(MAX(r.execs_per_sec), 0.0) AS peak_execs_per_sec,
			COALESCE(AVG(CASE WHEN r.coverage_pct > 0 THEN r.coverage_pct END), 0.0) AS avg_coverage_pct,
			COALESCE(MAX(r.coverage_pct), 0.0) AS max_coverage_pct,
			CAST(COALESCE(SUM(r.new_interesting), 0) AS BIGINT) AS new_interesting,
			CAST(COALESCE(MAX(COALESCE(r.corpus_count, 0)), 0) AS BIGINT) AS corpus_count,
			CAST(COALESCE(SUM(CASE WHEN r.status = 'ok' THEN 1 ELSE 0 END), 0) AS BIGINT) AS ok_runs,
			CAST(COALESCE(SUM(CASE WHEN r.status = 'crash' THEN 1 ELSE 0 END), 0) AS BIGINT) AS crash_runs,
			CAST(COALESCE(SUM(CASE WHEN r.status IN ('error', 'missing_target') THEN 1 ELSE 0 END), 0) AS BIGINT) AS error_runs,
			CAST(COALESCE(SUM(CASE WHEN r.status = 'incomplete' THEN 1 ELSE 0 END), 0) AS BIGINT) AS incomplete_runs,
			CAST(COALESCE(SUM(CASE WHEN r.status = 'canceled' THEN 1 ELSE 0 END), 0) AS BIGINT) AS canceled_runs
		FROM runs r
		JOIN targets t ON t.id = r.target_id
		JOIN projects p ON p.id = t.project_id
		WHERE r.started >= (SELECT COALESCE(MAX(started), CURRENT_TIMESTAMP) FROM runs) - %s
	`, bucketSQL, lookbackSQL)

	var args []any
	if proj != "" {
		sqlQuery += ` AND (p.name = ? OR CAST(p.id AS VARCHAR) = ?)`
		args = append(args, proj, proj)
	}
	if tgt != "" {
		sqlQuery += ` AND (CAST(r.target_id AS VARCHAR) = ? OR t.name = ? OR (p.name || '/' || t.name) = ?)`
		args = append(args, tgt, tgt, tgt)
	}
	sqlQuery += ` GROUP BY bucket_start ORDER BY bucket_start ASC`

	rows, err := s.query(ctx, sqlQuery, args...)
	if err != nil {
		return StatsSeries{}, fmt.Errorf("get stats (%s/%s): %w", qName, win, err)
	}
	defer func() { _ = rows.Close() }()

	out := StatsSeries{
		Query:         qName,
		Window:        win,
		BucketSeconds: bucketSec,
		Project:       proj,
		Target:        tgt,
		Buckets:       []StatsBucket{},
	}

	var (
		execRateWeightedSum float64
		execRateWeight      int
		covWeightedSum      float64
		covWeight           int
	)

	for rows.Next() {
		var b StatsBucket
		if err := rows.Scan(
			&b.BucketStart,
			&b.Runs,
			&b.Execs,
			&b.DurationSec,
			&b.AvgExecsPerSec,
			&b.PeakExecsSec,
			&b.AvgCoveragePct,
			&b.MaxCoveragePct,
			&b.NewInteresting,
			&b.CorpusCount,
			&b.OKRuns,
			&b.CrashRuns,
			&b.ErrorRuns,
			&b.IncompleteRuns,
			&b.CanceledRuns,
		); err != nil {
			return StatsSeries{}, fmt.Errorf("scan stats bucket: %w", err)
		}
		out.Buckets = append(out.Buckets, b)
		out.TotalRuns += b.Runs
		out.TotalExecs += b.Execs
		if b.PeakExecsSec > out.PeakExecsSec {
			out.PeakExecsSec = b.PeakExecsSec
		}
		if b.MaxCoveragePct > out.MaxCoveragePct {
			out.MaxCoveragePct = b.MaxCoveragePct
		}
		out.NewInteresting += b.NewInteresting
		if b.CorpusCount > out.CorpusCount {
			out.CorpusCount = b.CorpusCount
		}
		out.OKRuns += b.OKRuns
		out.CrashRuns += b.CrashRuns
		out.ErrorRuns += b.ErrorRuns
		out.IncompleteRuns += b.IncompleteRuns
		out.CanceledRuns += b.CanceledRuns

		if b.AvgExecsPerSec > 0 && b.Runs > 0 {
			execRateWeightedSum += b.AvgExecsPerSec * float64(b.Runs)
			execRateWeight += b.Runs
		}
		if b.AvgCoveragePct > 0 && b.Runs > 0 {
			covWeightedSum += b.AvgCoveragePct * float64(b.Runs)
			covWeight += b.Runs
		}
	}
	if err := rows.Err(); err != nil {
		return StatsSeries{}, err
	}

	if execRateWeight > 0 {
		out.AvgExecsPerSec = execRateWeightedSum / float64(execRateWeight)
	}
	if covWeight > 0 {
		out.AvgCoveragePct = covWeightedSum / float64(covWeight)
	}
	return out, nil
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}
