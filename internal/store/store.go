package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

// Store manages persistence of fuzzing projects, targets, runs, and crashes in DuckDB.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Project represents a fuzzing project with aggregated status metadata.
type Project struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Repo           string     `json:"repo"`
	Branch         string     `json:"branch,omitempty"`
	LastSynced     *time.Time `json:"last_synced,omitempty"`
	TargetCount    int        `json:"target_count"`
	TotalRuns      int        `json:"total_runs"`
	OpenCrashes    int        `json:"open_crashes"`
	LatestStatus   string     `json:"latest_status"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	AvgExecsPerSec float64    `json:"avg_execs_per_sec"`
	AvgCoveragePct float64    `json:"avg_coverage_pct"`
}

// Target represents a discovered Go fuzz target and its latest run status.
type Target struct {
	ID             int64      `json:"id"`
	ProjectID      int64      `json:"project_id"`
	ProjectName    string     `json:"project_name"`
	Package        string     `json:"package"`
	Name           string     `json:"name"`
	LastRunID      *int64     `json:"last_run_id,omitempty"`
	LastStarted    *time.Time `json:"last_started,omitempty"`
	DurationSec    float64    `json:"duration_sec"`
	Execs          int64      `json:"execs"`
	ExecsPerSec    float64    `json:"execs_per_sec"`
	NewInteresting int        `json:"new_interesting"`
	Crashes        int        `json:"crashes"`
	CoveragePct    float64    `json:"coverage_pct"`
	Status         string     `json:"status"`
	Worker         string     `json:"worker"`
	OpenCrashes    int        `json:"open_crashes"`
	TotalRuns      int        `json:"total_runs"`
	CoverageTrend  []float64  `json:"coverage_trend"`
}

// Run represents a single execution of a fuzz target.
type Run struct {
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
	Crashes        int       `json:"crashes"`
	CoveragePct    float64   `json:"coverage_pct"`
	Status         string    `json:"status"`
	Worker         string    `json:"worker"`
}

// Crash represents a fuzzing crash record stored in DuckDB.
type Crash struct {
	ID             int64      `json:"id"`
	TargetID       int64      `json:"target_id"`
	ProjectName    string     `json:"project_name,omitempty"`
	Package        string     `json:"package,omitempty"`
	TargetName     string     `json:"target_name,omitempty"`
	FirstSeen      time.Time  `json:"first_seen"`
	LastSeen       time.Time  `json:"last_seen"`
	FixedAt        *time.Time `json:"fixed_at,omitempty"`
	Stack          string     `json:"stack"`
	InputHash      string     `json:"input_hash"`
	MinimizedInput string     `json:"minimized_input"`
	IssueURL       string     `json:"issue_url"`
}

// CoveragePoint represents a point in a target's coverage and throughput history.
type CoveragePoint struct {
	RunID          int64     `json:"run_id"`
	TargetID       int64     `json:"target_id"`
	Started        time.Time `json:"started"`
	CoveragePct    float64   `json:"coverage_pct"`
	ExecsPerSec    float64   `json:"execs_per_sec"`
	NewInteresting int       `json:"new_interesting"`
}

// FleetStats summarizes overall store statistics for /api/status.
type FleetStats struct {
	TotalProjects int     `json:"total_projects"`
	TotalTargets  int     `json:"total_targets"`
	OpenCrashes   int     `json:"open_crashes"`
	RunsLast24h   int     `json:"runs_last_24h"`
	ExecsLast24h  int64   `json:"execs_last_24h"`
	AvgExecsSec   float64 `json:"avg_execs_per_sec"`
}

// Open opens or creates a DuckDB database at path (use "" or ":memory:" for in-memory)
// and runs schema migrations.
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
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Migrate creates all required tables, sequences, indexes, and views if they do not exist.
func (s *Store) Migrate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stmts := []string{
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

		`CREATE OR REPLACE VIEW latest_target_status AS
		WITH ranked_runs AS (
			SELECT
				r.id AS run_id,
				r.target_id,
				r.started,
				r.duration_sec,
				r.execs,
				r.execs_per_sec,
				r.new_interesting,
				r.crashes,
				r.coverage_pct,
				r.status,
				r.worker,
				ROW_NUMBER() OVER (PARTITION BY r.target_id ORDER BY r.started DESC, r.id DESC) AS rn
			FROM runs r
		),
		run_counts AS (
			SELECT target_id, COUNT(*) AS total_runs
			FROM runs
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
			rr.run_id AS last_run_id,
			rr.started AS last_started,
			COALESCE(rr.duration_sec, 0.0) AS duration_sec,
			COALESCE(rr.execs, 0) AS execs,
			COALESCE(rr.execs_per_sec, 0.0) AS execs_per_sec,
			COALESCE(rr.new_interesting, 0) AS new_interesting,
			COALESCE(rr.crashes, 0) AS crashes,
			COALESCE(rr.coverage_pct, 0.0) AS coverage_pct,
			COALESCE(rr.status, 'pending') AS status,
			COALESCE(rr.worker, '') AS worker,
			COALESCE(oc.open_crashes, 0) AS open_crashes,
			COALESCE(rc.total_runs, 0) AS total_runs
		FROM targets t
		JOIN projects p ON p.id = t.project_id
		LEFT JOIN ranked_runs rr ON rr.target_id = t.id AND rr.rn = 1
		LEFT JOIN open_crash_counts oc ON oc.target_id = t.id
		LEFT JOIN run_counts rc ON rc.target_id = t.id;`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("exec migration %q: %w", stmt, err)
		}
	}
	return nil
}

// UpsertProject creates or updates a project by name and returns its ID.
func (s *Store) UpsertProject(ctx context.Context, name, repo, branch string, syncedAt *time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var syncArg any
		if syncedAt != nil {
			syncArg = syncedAt.UTC()
		}
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO projects (name, repo, branch, last_synced) VALUES (?, ?, ?, ?) RETURNING id`,
			name, repo, branch, syncArg,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("insert project %q: %w", name, err)
		}
		return id, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query project %q: %w", name, err)
	}

	if syncedAt != nil {
		_, err = s.db.ExecContext(ctx,
			`UPDATE projects SET repo = ?, branch = ?, last_synced = ? WHERE id = ?`,
			repo, branch, syncedAt.UTC(), id,
		)
	} else {
		_, err = s.db.ExecContext(ctx,
			`UPDATE projects SET repo = ?, branch = ? WHERE id = ?`,
			repo, branch, id,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("update project %q: %w", name, err)
	}
	return id, nil
}

// UpsertTarget ensures a target (project_id, package, name) exists and returns its ID.
func (s *Store) UpsertTarget(ctx context.Context, projectID int64, pkg, name string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM targets WHERE project_id = ? AND package = ? AND name = ?`,
		projectID, pkg, name,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO targets (project_id, package, name) VALUES (?, ?, ?) RETURNING id`,
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
	return id, nil
}

// InsertRun records a completed fuzz run and returns its new run ID.
func (s *Store) InsertRun(ctx context.Context, r Run) (int64, error) {
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

	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO runs (
			target_id, started, duration_sec, execs, execs_per_sec,
			new_interesting, crashes, coverage_pct, status, worker
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		r.TargetID, started, r.DurationSec, r.Execs, r.ExecsPerSec,
		r.NewInteresting, r.Crashes, r.CoveragePct, r.Status, r.Worker,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert run for target %d: %w", r.TargetID, err)
	}
	return id, nil
}

// RecordCrash inserts a new crash or updates last_seen if the same (target_id, input_hash) already exists.
func (s *Store) RecordCrash(ctx context.Context, c Crash) (int64, error) {
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
		`SELECT id FROM crashes WHERE target_id = ? AND input_hash = ?`,
		c.TargetID, c.InputHash,
	).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		var id int64
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO crashes (
				target_id, first_seen, last_seen, fixed_at, stack,
				input_hash, minimized_input, issue_url
			) VALUES (?, ?, ?, NULL, ?, ?, ?, ?) RETURNING id`,
			c.TargetID, firstSeen, lastSeen, c.Stack, c.InputHash, c.MinimizedInput, c.IssueURL,
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
		`UPDATE crashes SET last_seen = ?, fixed_at = NULL, stack = CASE WHEN ? != '' THEN ? ELSE stack END WHERE id = ?`,
		lastSeen, c.Stack, c.Stack, existingID,
	)
	if err != nil {
		return 0, fmt.Errorf("update crash %d: %w", existingID, err)
	}
	return existingID, nil
}

// ListProjects returns all projects with their latest aggregated status summary.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		SELECT
			p.id,
			p.name,
			p.repo,
			COALESCE(p.branch, ''),
			p.last_synced,
			COUNT(v.target_id) AS target_count,
			COALESCE(SUM(v.total_runs), 0) AS total_runs,
			COALESCE(SUM(v.open_crashes), 0) AS open_crashes,
			MAX(v.last_started) AS last_run_at,
			COALESCE(AVG(CASE WHEN v.execs_per_sec > 0 THEN v.execs_per_sec END), 0.0) AS avg_execs_per_sec,
			COALESCE(AVG(CASE WHEN v.coverage_pct > 0 THEN v.coverage_pct END), 0.0) AS avg_coverage_pct,
			COALESCE(SUM(CASE WHEN v.status = 'error' OR v.status = 'crash' THEN 1 ELSE 0 END), 0) AS err_targets,
			COALESCE(SUM(CASE WHEN v.status = 'ok' THEN 1 ELSE 0 END), 0) AS ok_targets
		FROM projects p
		LEFT JOIN latest_target_status v ON v.project_id = p.id
		GROUP BY p.id, p.name, p.repo, p.branch, p.last_synced
		ORDER BY p.name ASC;
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var result []Project
	for rows.Next() {
		var p Project
		var lastSynced sql.NullTime
		var lastRunAt sql.NullTime
		var errTargets, okTargets int
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Repo, &p.Branch, &lastSynced,
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
		case errTargets > 0:
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

// ListTargets returns targets with their latest status and recent coverage trend.
// projectFilter can be empty (all projects), a project name, or a numeric project ID.
func (s *Store) ListTargets(ctx context.Context, projectFilter string) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	baseQuery := `
		SELECT
			target_id, project_id, project_name, package, target_name,
			last_run_id, last_started, duration_sec, execs, execs_per_sec,
			new_interesting, crashes, coverage_pct, status, worker,
			open_crashes, total_runs
		FROM latest_target_status
	`
	var args []any
	if strings.TrimSpace(projectFilter) != "" {
		baseQuery += ` WHERE project_name = ? OR CAST(project_id AS VARCHAR) = ?`
		args = append(args, projectFilter, projectFilter)
	}
	baseQuery += ` ORDER BY project_name ASC, package ASC, target_name ASC`

	rows, err := s.db.QueryContext(ctx, baseQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer rows.Close()

	var targets []Target
	for rows.Next() {
		var t Target
		var lastRunID sql.NullInt64
		var lastStarted sql.NullTime
		if err := rows.Scan(
			&t.ID, &t.ProjectID, &t.ProjectName, &t.Package, &t.Name,
			&lastRunID, &lastStarted, &t.DurationSec, &t.Execs, &t.ExecsPerSec,
			&t.NewInteresting, &t.Crashes, &t.CoveragePct, &t.Status, &t.Worker,
			&t.OpenCrashes, &t.TotalRuns,
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
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Populate recent coverage trend (last 10 runs per target in chronological order)
	for i := range targets {
		trendRows, err := s.db.QueryContext(ctx, `
			SELECT coverage_pct FROM (
				SELECT coverage_pct, started, id
				FROM runs
				WHERE target_id = ?
				ORDER BY started DESC, id DESC
				LIMIT 10
			) sub
			ORDER BY started ASC, id ASC
		`, targets[i].ID)
		if err != nil {
			continue
		}
		var trend []float64
		for trendRows.Next() {
			var cov float64
			if err := trendRows.Scan(&cov); err == nil {
				trend = append(trend, cov)
			}
		}
		_ = trendRows.Close()
		if trend != nil {
			targets[i].CoverageTrend = trend
		}
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
			r.new_interesting, r.crashes, r.coverage_pct, r.status, r.worker
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

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(
			&r.ID, &r.TargetID, &r.ProjectName, &r.Package, &r.TargetName,
			&r.Started, &r.DurationSec, &r.Execs, &r.ExecsPerSec,
			&r.NewInteresting, &r.Crashes, &r.CoveragePct, &r.Status, &r.Worker,
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
			c.first_seen, c.last_seen, c.fixed_at, c.stack,
			c.input_hash, c.minimized_input, COALESCE(c.issue_url, '')
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

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list crashes: %w", err)
	}
	defer rows.Close()

	var crashes []Crash
	for rows.Next() {
		var c Crash
		var fixedAt sql.NullTime
		if err := rows.Scan(
			&c.ID, &c.TargetID, &c.ProjectName, &c.Package, &c.TargetName,
			&c.FirstSeen, &c.LastSeen, &fixedAt, &c.Stack,
			&c.InputHash, &c.MinimizedInput, &c.IssueURL,
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

// GetCoverageHistory returns chronological coverage and throughput points for a target.
func (s *Store) GetCoverageHistory(ctx context.Context, targetFilter string, limit int) ([]CoveragePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > 500 {
		limit = 50
	}

	query := `
		SELECT run_id, target_id, started, coverage_pct, execs_per_sec, new_interesting
		FROM (
			SELECT
				r.id AS run_id,
				r.target_id,
				r.started,
				r.coverage_pct,
				r.execs_per_sec,
				r.new_interesting
			FROM runs r
			JOIN targets t ON t.id = r.target_id
			JOIN projects p ON p.id = t.project_id
			WHERE CAST(r.target_id AS VARCHAR) = ? OR t.name = ? OR (p.name || '/' || t.name) = ?
			ORDER BY r.started DESC, r.id DESC
			LIMIT ?
		) sub
		ORDER BY started ASC, run_id ASC
	`

	rows, err := s.db.QueryContext(ctx, query, targetFilter, targetFilter, targetFilter, limit)
	if err != nil {
		return nil, fmt.Errorf("get coverage history: %w", err)
	}
	defer rows.Close()

	var points []CoveragePoint
	for rows.Next() {
		var pt CoveragePoint
		if err := rows.Scan(&pt.RunID, &pt.TargetID, &pt.Started, &pt.CoveragePct, &pt.ExecsPerSec, &pt.NewInteresting); err != nil {
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

// GetFleetStats returns summary counts for projects, targets, open crashes, and runs in the last 24h.
func (s *Store) GetFleetStats(ctx context.Context) (FleetStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var stats FleetStats
	since := time.Now().UTC().Add(-24 * time.Hour)

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&stats.TotalProjects); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM targets`).Scan(&stats.TotalTargets); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crashes WHERE fixed_at IS NULL`).Scan(&stats.OpenCrashes); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(execs), 0), COALESCE(AVG(CASE WHEN execs_per_sec > 0 THEN execs_per_sec END), 0.0) FROM runs WHERE started >= ?`,
		since,
	).Scan(&stats.RunsLast24h, &stats.ExecsLast24h, &stats.AvgExecsSec); err != nil {
		return stats, err
	}
	return stats, nil
}
