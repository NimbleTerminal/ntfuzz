package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"nimbleterminal.dev/ntduckbox"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

// CurrentSchemaVersion tracks the DuckDB schema version.
const CurrentSchemaVersion = 6

// MaxCrashInputBytes caps minimized input bytes stored directly in DuckDB (64 KiB).
const MaxCrashInputBytes = 64 * 1024

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
	Name         string
	Repo         string
	Branch       string
	GitSHA       string
	Dirty        bool
	SyncedAt     *time.Time
	SyncError    string
	PublicInputs bool
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
	PublicInputs   bool       `json:"public_inputs"`
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
	PublicInputs     bool       `json:"public_inputs"`
	FirstSeen        time.Time  `json:"first_seen"`
	LastSeen         time.Time  `json:"last_seen"`
	FixedAt          *time.Time `json:"fixed_at,omitempty"`
	Stack            string     `json:"stack"`
	StackFingerprint string     `json:"stack_fingerprint,omitempty"`
	InputHash        string     `json:"input_hash"`
	MinimizedInput   string     `json:"minimized_input"`
	InputBytes       []byte     `json:"-"`
	InputTruncated   bool       `json:"input_truncated"`
	IssueURL         string     `json:"issue_url"`
	GitSHA           string     `json:"git_sha,omitempty"`
	Dirty            bool       `json:"dirty"`
	GoVersion        string     `json:"go_version,omitempty"`
	BlamedModule     string     `json:"blamed_module,omitempty"`
	BlamedVersion    string     `json:"blamed_version,omitempty"`
	BlamedFile       string     `json:"blamed_file,omitempty"`
	BlamedLine       int        `json:"blamed_line,omitempty"`
	ReplayStatus     string     `json:"replay_status,omitempty"`
	ReplayGitSHA     string     `json:"replay_git_sha,omitempty"`
	ReplayCheckedAt  *time.Time `json:"replay_checked_at,omitempty"`
	ReplayDiagnostic string     `json:"replay_diagnostic,omitempty"`
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
	StatsQueryBurndown   = "burndown"
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
	OpenCrashes       int       `json:"open_crashes"`
	NewCrashes        int       `json:"new_crashes"`
	ReopenedCrashes   int       `json:"reopened_crashes"`
	ResolvedCrashes   int       `json:"resolved_crashes"`
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
	HistoryEstimatedBefore *time.Time    `json:"history_estimated_before,omitempty"`
	OpenCrashes            int           `json:"open_crashes"`
	NewCrashes             int           `json:"new_crashes"`
	ReopenedCrashes        int           `json:"reopened_crashes"`
	ResolvedCrashes        int           `json:"resolved_crashes"`
	Revisions              []Revision    `json:"revisions,omitempty"`
	Query                  string        `json:"query"`
	Window                 string        `json:"window"`
	BucketSeconds          int           `json:"bucket_seconds"`
	Project                string        `json:"project,omitempty"`
	Target                 string        `json:"target,omitempty"`
	TotalRuns              int           `json:"total_runs"`
	TotalExecs             int64         `json:"total_execs"`
	AvgExecsPerSec         float64       `json:"avg_execs_per_sec"`
	PeakExecsSec           float64       `json:"peak_execs_per_sec"`
	AvgCoveragePct         float64       `json:"avg_coverage_pct"`
	MaxCoveragePct         float64       `json:"max_coverage_pct"`
	NewInteresting         int           `json:"new_interesting"`
	CorpusCount            int           `json:"corpus_count"`
	OKRuns                 int           `json:"ok_runs"`
	CrashRuns              int           `json:"crash_runs"`
	ErrorRuns              int           `json:"error_runs"`
	IncompleteRuns         int           `json:"incomplete_runs"`
	CanceledRuns           int           `json:"canceled_runs"`
	Buckets                []StatsBucket `json:"buckets"`
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

	if _, err := s.schemaVersionLocked(ctx); err != nil {
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

	migrations = append(migrations, migration{version: 5, stmts: []string{crashEventSchema}})
	migrations = append(migrations, migration{
		version: 6,
		stmts: []string{
			`ALTER TABLE projects ADD COLUMN IF NOT EXISTS public_inputs BOOLEAN DEFAULT FALSE;`,
			`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS input_bytes BLOB;`,
			`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS input_truncated BOOLEAN DEFAULT FALSE;`,
			`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS blamed_module VARCHAR DEFAULT '';`,
			`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS blamed_version VARCHAR DEFAULT '';`,
			`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS blamed_file VARCHAR DEFAULT '';`,
			`ALTER TABLE crashes ADD COLUMN IF NOT EXISTS blamed_line INTEGER DEFAULT 0;`,
		},
	})

	applied := map[int]bool{}
	if vRows, qErr := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`); qErr == nil {
		for vRows.Next() {
			var v int
			if vRows.Scan(&v) == nil {
				applied[v] = true
			}
		}
		_ = vRows.Close()
	}

	for _, m := range migrations {
		if applied[m.version] {
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

	if err := s.backfillCrashes(ctx); err != nil {
		return fmt.Errorf("backfill crashes: %w", err)
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

var (
	redactSecretRe  = regexp.MustCompile(`(?i)(token|password|secret|authorization|api[_-]?key)([=: ]+(?:(?:token|bearer|basic)\s+)?)[^\s'"]+`)
	redactHTTPURLRe = regexp.MustCompile(`(https?://|git://)[^/\s@'"]+@`)
	redactSSHURLRe  = regexp.MustCompile(`(ssh://)[^/\s@'"]*:[^/\s@'"]+@`)
)

// HasEmbeddedRepoCredentials reports whether a repository URL contains embedded
// userinfo credentials (e.g., https://user:token@host/..., https://token@host/...,
// ssh://user:pass@host/..., or user:pass@host:path). Standard SSH syntax without
// a password (git@host:path or ssh://git@host/path) returns false.
func HasEmbeddedRepoCredentials(repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if strings.Contains(repo, "://") {
		if u, err := url.Parse(repo); err == nil && u.User != nil {
			if strings.EqualFold(u.Scheme, "ssh") {
				_, hasPass := u.User.Password()
				return hasPass
			}
			return true
		}
		schemeIdx := strings.Index(repo, "://")
		scheme := strings.ToLower(repo[:schemeIdx])
		rest := repo[schemeIdx+3:]
		if slashIdx := strings.IndexByte(rest, '/'); slashIdx >= 0 {
			rest = rest[:slashIdx]
		}
		if atIdx := strings.LastIndexByte(rest, '@'); atIdx >= 0 {
			userinfo := rest[:atIdx]
			if scheme == "ssh" {
				return strings.Contains(userinfo, ":")
			}
			return true
		}
		return false
	}
	if atIdx := strings.IndexByte(repo, '@'); atIdx > 0 {
		beforeAt := repo[:atIdx]
		if !strings.ContainsAny(beforeAt, `/\`) && strings.Contains(beforeAt, ":") {
			return true
		}
	}
	return false
}

// RedactRepoURL strips embedded credentials from a repository URL as defense in depth.
func RedactRepoURL(repo string) string {
	if !HasEmbeddedRepoCredentials(repo) {
		return repo
	}
	if strings.Contains(repo, "://") {
		schemeIdx := strings.Index(repo, "://")
		prefix := repo[:schemeIdx+3]
		rest := repo[schemeIdx+3:]
		slashIdx := strings.IndexByte(rest, '/')
		hostPart := rest
		pathPart := ""
		if slashIdx >= 0 {
			hostPart = rest[:slashIdx]
			pathPart = rest[slashIdx:]
		}
		if atIdx := strings.LastIndexByte(hostPart, '@'); atIdx >= 0 {
			return prefix + "<redacted>@" + hostPart[atIdx+1:] + pathPart
		}
	}
	if atIdx := strings.IndexByte(repo, '@'); atIdx > 0 {
		return "<redacted>@" + repo[atIdx+1:]
	}
	return redactHTTPURLRe.ReplaceAllString(repo, "${1}<redacted>@")
}

// RedactSyncError redacts URL credentials and labeled secrets from a sync error or diagnostic string.
func RedactSyncError(s string) string {
	if s == "" {
		return ""
	}
	s = redactHTTPURLRe.ReplaceAllString(s, "${1}<redacted>@")
	s = redactSSHURLRe.ReplaceAllString(s, "${1}<redacted>@")
	s = redactSecretRe.ReplaceAllString(s, "${1}${2}<redacted>")
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' || r == 127 {
			return -1
		}
		return r
	}, s)
	return sanitizeUTF8(s)
}

// SyncProject records project checkout state, revision (git_sha + dirty), and any sync error.
// If in.SyncedAt is nil (e.g. a failed git pull), last_synced is NOT advanced, and sync_error is recorded.
func (s *Store) SyncProject(ctx context.Context, in ProjectSyncInput) (int64, error) {
	if s.readonly != nil {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	in.Repo = RedactRepoURL(in.Repo)
	in.SyncError = RedactSyncError(in.SyncError)

	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE name = ?`, in.Name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var syncArg any
		if in.SyncedAt != nil {
			syncArg = in.SyncedAt.UTC()
		}
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO projects (name, repo, branch, last_synced, git_sha, dirty, sync_error, public_inputs)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			in.Name, in.Repo, in.Branch, syncArg, in.GitSHA, in.Dirty, in.SyncError, in.PublicInputs,
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
			 SET repo = ?, branch = ?, last_synced = ?, git_sha = ?, dirty = ?, sync_error = ?, public_inputs = ?
			 WHERE id = ?`,
			in.Repo, in.Branch, in.SyncedAt.UTC(), in.GitSHA, in.Dirty, in.SyncError, in.PublicInputs, id,
		)
	} else {
		// Preserve existing last_synced and git_sha if not provided on a failed pull
		_, err = s.db.ExecContext(ctx,
			`UPDATE projects
			 SET repo = ?, branch = ?,
			     git_sha = CASE WHEN ? != '' THEN ? ELSE git_sha END,
			     dirty = CASE WHEN ? != '' THEN ? ELSE dirty END,
			     sync_error = ?,
			     public_inputs = ?
			 WHERE id = ?`,
			in.Repo, in.Branch, in.GitSHA, in.GitSHA, in.GitSHA, in.Dirty, in.SyncError, in.PublicInputs, id,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("update project %q: %w", in.Name, err)
	}
	return id, s.recordRevisionLocked(ctx, id, in)
}

// SetProjectPublicInputs updates the public_inputs flag for an existing project by name.
func (s *Store) SetProjectPublicInputs(ctx context.Context, name string, publicInputs bool) error {
	if s.readonly != nil {
		return ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET public_inputs = ? WHERE name = ?`, publicInputs, name)
	return err
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
	defer func() { _ = tx.Rollback() }()
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

var (
	reFPFileLine       = regexp.MustCompile(`(?m)([A-Za-z0-9_./\\@!~+-]+\.go):\d+(\s*\+0x[0-9a-fA-F]+)?`)
	reFPLegacyFileLine = regexp.MustCompile(`(?m)([A-Za-z0-9_./\\-]+\.go):\d+(\s*\+0x[0-9a-fA-F]+)?`)
	reFPHexAddr        = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	reFPGoroutine      = regexp.MustCompile(`goroutine \d+`)
	reFPFailDuration   = regexp.MustCompile(`\([0-9.]+s\)`)
	reFPPkgFailDur     = regexp.MustCompile(`(FAIL\t\S+\t)[0-9.]+s`)
)

// FingerprintStack normalizes a crash stack by stripping directory paths, line numbers,
// test durations, hex pointers, and goroutine IDs so line shifts and path scrubbing
// produce identical fingerprints.
func FingerprintStack(stack string) string {
	return fingerprintStackWithFileLine(stack, reFPFileLine)
}

// LegacyFingerprintStack computes the pre-v6 stack fingerprint using the older path character class
// so historical crash rows that have not been recomputed still match on deduplication.
func LegacyFingerprintStack(stack string) string {
	return fingerprintStackWithFileLine(stack, reFPLegacyFileLine)
}

func fingerprintStackWithFileLine(stack string, fileLineRe *regexp.Regexp) string {
	trimmed := strings.TrimSpace(stack)
	if trimmed == "" {
		return ""
	}
	normalized := reFPFailDuration.ReplaceAllString(trimmed, "(0s)")
	normalized = reFPPkgFailDur.ReplaceAllString(normalized, "${1}0s")
	normalized = reFPGoroutine.ReplaceAllString(normalized, "goroutine N")
	normalized = fileLineRe.ReplaceAllStringFunc(normalized, func(m string) string {
		colon := strings.IndexByte(m, ':')
		if colon < 0 {
			return m
		}
		return filepath.Base(m[:colon])
	})
	normalized = reFPHexAddr.ReplaceAllString(normalized, "0xADDR")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:12])
}

func (s *Store) backfillCrashes(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			c.id,
			COALESCE(c.stack, ''),
			COALESCE(c.stack_fingerprint, ''),
			COALESCE(c.blamed_module, ''),
			COALESCE(c.minimized_input, ''),
			COALESCE(OCTET_LENGTH(c.input_bytes), 0),
			COALESCE(p.name, ''),
			COALESCE(t.package, '')
		FROM crashes c
		LEFT JOIN targets t ON t.id = c.target_id
		LEFT JOIN projects p ON p.id = t.project_id
		WHERE COALESCE(c.stack, '') <> '' OR COALESCE(OCTET_LENGTH(c.input_bytes), 0) = 0
	`)
	if err != nil {
		return err
	}
	type item struct {
		id             int64
		stack          string
		stackFP        string
		blamedModule   string
		minimizedInput string
		inputLen       int
		projectName    string
		pkgImport      string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(
			&it.id,
			&it.stack,
			&it.stackFP,
			&it.blamedModule,
			&it.minimizedInput,
			&it.inputLen,
			&it.projectName,
			&it.pkgImport,
		); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for _, it := range items {
		modRoot := DeriveModuleRoot(it.projectName, it.pkgImport)
		scrubbed := ScrubStack(it.stack, "", modRoot, "")
		fp := FingerprintStack(scrubbed)
		if fp == "" {
			fp = it.stackFP
		}
		var attr StackAttribution
		if it.blamedModule == "" && scrubbed != "" {
			attr = AttributeStack(scrubbed, "", modRoot)
		}
		var backfilledInput []byte
		var backfilledTrunc bool
		if it.inputLen == 0 && isSafeBackfillInputPath(it.minimizedInput) {
			if raw, readErr := os.ReadFile(it.minimizedInput); readErr == nil && len(raw) > 0 {
				if len(raw) > MaxCrashInputBytes {
					backfilledInput = append([]byte(nil), raw[:MaxCrashInputBytes]...)
					backfilledTrunc = true
				} else {
					backfilledInput = raw
				}
			}
		}

		stackChanged := scrubbed != it.stack || fp != it.stackFP
		if !stackChanged && attr.Module == "" && len(backfilledInput) == 0 {
			continue
		}

		if stackChanged || attr.Module != "" {
			if attr.Module != "" {
				if _, err := s.db.ExecContext(ctx,
					`UPDATE crashes SET stack = ?, stack_fingerprint = ?, blamed_module = ?, blamed_version = ?, blamed_file = ?, blamed_line = ? WHERE id = ?`,
					scrubbed, fp, attr.Module, attr.Version, attr.File, attr.Line, it.id,
				); err != nil {
					return err
				}
			} else {
				if _, err := s.db.ExecContext(ctx,
					`UPDATE crashes SET stack = ?, stack_fingerprint = ? WHERE id = ?`,
					scrubbed, fp, it.id,
				); err != nil {
					return err
				}
			}
		}
		if len(backfilledInput) > 0 {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE crashes SET input_bytes = ?, input_truncated = ? WHERE id = ?`,
				backfilledInput, backfilledTrunc, it.id,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func isSafeBackfillInputPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || p == "inline-failure" {
		return false
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	slashPath := strings.ReplaceAll(p, "\\", "/")
	if !strings.Contains(slashPath, "/crashes/") && !strings.HasPrefix(slashPath, "crashes/") && !strings.Contains(slashPath, "/testdata/fuzz/") {
		return false
	}
	info, err := os.Lstat(p)
	return err == nil && info.Mode().IsRegular()
}

func sanitizeUTF8(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "\uFFFD")
	}
	return s
}

// RecordCrash inserts a new crash or updates last_seen if the same (target_id, input_hash)
// or (target_id, stack_fingerprint) already exists.
func (s *Store) RecordCrash(ctx context.Context, c Crash) (int64, error) {
	if s.readonly != nil {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	c.Stack = sanitizeUTF8(c.Stack)
	c.StackFingerprint = sanitizeUTF8(c.StackFingerprint)
	c.InputHash = sanitizeUTF8(c.InputHash)
	c.MinimizedInput = sanitizeUTF8(c.MinimizedInput)
	c.IssueURL = sanitizeUTF8(c.IssueURL)
	c.GitSHA = sanitizeUTF8(c.GitSHA)
	c.GoVersion = sanitizeUTF8(c.GoVersion)
	c.BlamedModule = sanitizeUTF8(c.BlamedModule)
	c.BlamedVersion = sanitizeUTF8(c.BlamedVersion)
	c.BlamedFile = sanitizeUTF8(c.BlamedFile)

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

	inputBytes := c.InputBytes
	inputTruncated := c.InputTruncated
	if len(inputBytes) > MaxCrashInputBytes {
		inputBytes = inputBytes[:MaxCrashInputBytes]
		inputTruncated = true
	}
	inputHex := hex.EncodeToString(inputBytes)

	legacyFP := ""
	if c.Stack != "" {
		legacyFP = LegacyFingerprintStack(c.Stack)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingID int64
	var fixed sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT id,fixed_at FROM crashes
		 WHERE target_id = ? AND (
		     input_hash = ?
		     OR (? != '' AND stack_fingerprint = ?)
		     OR (? != '' AND stack_fingerprint = ?)
		 )
		 ORDER BY id ASC LIMIT 1`,
		c.TargetID, c.InputHash,
		c.StackFingerprint, c.StackFingerprint,
		legacyFP, legacyFP,
	).Scan(&existingID, &fixed)
	if errors.Is(err, sql.ErrNoRows) {
		var id int64
		err = tx.QueryRowContext(ctx,
			`INSERT INTO crashes (
				target_id, first_seen, last_seen, fixed_at, stack, stack_fingerprint,
				input_hash, minimized_input, input_bytes, input_truncated,
				issue_url, git_sha, dirty, go_version,
				blamed_module, blamed_version, blamed_file, blamed_line
			) VALUES (
				?, ?, ?, NULL, ?, ?,
				?, ?, CASE WHEN ? != '' THEN unhex(?) ELSE NULL END, ?,
				?, ?, ?, ?,
				?, ?, ?, ?
			) RETURNING id`,
			c.TargetID, firstSeen, lastSeen, c.Stack, c.StackFingerprint,
			c.InputHash, c.MinimizedInput, inputHex, inputHex, inputTruncated,
			c.IssueURL, c.GitSHA, c.Dirty, c.GoVersion,
			c.BlamedModule, c.BlamedVersion, c.BlamedFile, c.BlamedLine,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("insert crash: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO crash_events(crash_id,event_at,kind,delta) VALUES(?,?,'new',1)`, id, firstSeen)
		if err != nil {
			return 0, err
		}
		return id, tx.Commit()
	}
	if err != nil {
		return 0, fmt.Errorf("query existing crash: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE crashes
		 SET last_seen = ?,
		     fixed_at = NULL,
		     stack = CASE WHEN ? != '' THEN ? ELSE stack END,
		     stack_fingerprint = CASE WHEN ? != '' THEN ? ELSE stack_fingerprint END,
		     minimized_input = CASE WHEN ? != '' THEN ? ELSE minimized_input END,
		     input_bytes = CASE WHEN ? != '' THEN unhex(?) ELSE input_bytes END,
		     input_truncated = CASE WHEN ? != '' OR ? THEN ? ELSE COALESCE(input_truncated, FALSE) END,
		     git_sha = CASE WHEN ? != '' THEN ? ELSE git_sha END,
		     dirty = ?,
		     go_version = CASE WHEN ? != '' THEN ? ELSE go_version END,
		     blamed_module = CASE WHEN ? != '' THEN ? ELSE COALESCE(blamed_module, '') END,
		     blamed_version = CASE WHEN ? != '' THEN ? ELSE COALESCE(blamed_version, '') END,
		     blamed_file = CASE WHEN ? != '' THEN ? ELSE COALESCE(blamed_file, '') END,
		     blamed_line = CASE WHEN ? > 0 THEN ? ELSE COALESCE(blamed_line, 0) END
		 WHERE id = ?`,
		lastSeen,
		c.Stack, c.Stack,
		c.StackFingerprint, c.StackFingerprint,
		c.MinimizedInput, c.MinimizedInput,
		inputHex, inputHex,
		inputHex, inputTruncated, inputTruncated,
		c.GitSHA, c.GitSHA,
		c.Dirty,
		c.GoVersion, c.GoVersion,
		c.BlamedModule, c.BlamedModule,
		c.BlamedVersion, c.BlamedVersion,
		c.BlamedFile, c.BlamedFile,
		c.BlamedLine, c.BlamedLine,
		existingID,
	)
	if err != nil {
		return 0, fmt.Errorf("update crash %d: %w", existingID, err)
	}
	if fixed.Valid {
		_, err = tx.ExecContext(ctx, `INSERT INTO crash_events(crash_id,event_at,kind,delta) VALUES(?,?,'reopened',1)`, existingID, lastSeen)
		if err != nil {
			return 0, err
		}
	}
	return existingID, tx.Commit()
}

// SetCrashIssue sets issue_url on a crash without marking it fixed.
func (s *Store) SetCrashIssue(ctx context.Context, crashID int64, issueURL string) error {
	if s.readonly != nil {
		return ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`UPDATE crashes SET issue_url = ? WHERE id = ?`,
		issueURL, crashID,
	)
	if err != nil {
		return fmt.Errorf("set crash %d issue: %w", crashID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("crash %d: %w", crashID, ErrNotFound)
	}
	return nil
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO crash_events(crash_id,event_at,kind,delta) SELECT id,CAST(? AS TIMESTAMP),'resolved',-1 FROM crashes WHERE id=? AND fixed_at IS NULL`, fixedAt.UTC().Format("2006-01-02 15:04:05.999999"), crashID)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE crashes
		 SET fixed_at = COALESCE(fixed_at, ?),
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
	return tx.Commit()
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

	ver, _ := s.schemaVersionLocked(ctx)
	publicInputsCol := "COALESCE(p.public_inputs, FALSE)"
	groupPublicInputs := ", p.public_inputs"
	if ver > 0 && ver < 6 {
		publicInputsCol = "FALSE"
		groupPublicInputs = ""
	}

	query := fmt.Sprintf(`
		SELECT
			p.id,
			p.name,
			p.repo,
			COALESCE(p.branch, ''),
			COALESCE(p.git_sha, ''),
			COALESCE(p.dirty, FALSE),
			%s,
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
		GROUP BY p.id, p.name, p.repo, p.branch, p.git_sha, p.dirty%s, p.sync_error, p.last_synced
		ORDER BY p.name ASC;
	`, publicInputsCol, groupPublicInputs)

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
			&p.ID, &p.Name, &p.Repo, &p.Branch, &p.GitSHA, &p.Dirty, &p.PublicInputs, &p.SyncError, &lastSynced,
			&p.TargetCount, &p.TotalRuns, &p.OpenCrashes, &lastRunAt,
			&p.AvgExecsPerSec, &p.AvgCoveragePct, &errTargets, &okTargets,
		); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		p.Repo = RedactRepoURL(p.Repo)
		p.SyncError = RedactSyncError(p.SyncError)
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

func (s *Store) crashSelectSQL(ctx context.Context) string {
	ver, _ := s.schemaVersionLocked(ctx)
	if ver > 0 && ver < 6 {
		return `
			SELECT
				c.id, c.target_id, p.name AS project_name, t.package, t.name AS target_name,
				FALSE AS public_inputs,
				c.first_seen, c.last_seen, c.fixed_at, c.stack, COALESCE(c.stack_fingerprint, ''),
				c.input_hash, c.minimized_input,
				'' AS input_hex, FALSE AS input_truncated,
				COALESCE(c.issue_url, ''),
				COALESCE(c.git_sha, ''), COALESCE(c.dirty, FALSE), COALESCE(c.go_version, ''),
				'' AS blamed_module, '' AS blamed_version, '' AS blamed_file, 0 AS blamed_line,
				COALESCE((SELECT r.outcome FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1), ''),
				COALESCE((SELECT r.git_sha FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1), ''),
				(SELECT r.checked_at FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1),
				COALESCE((SELECT r.diagnostic FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1), '')
			FROM crashes c
			JOIN targets t ON t.id = c.target_id
			JOIN projects p ON p.id = t.project_id
		`
	}
	return `
		SELECT
			c.id, c.target_id, p.name AS project_name, t.package, t.name AS target_name,
			COALESCE(p.public_inputs, FALSE),
			c.first_seen, c.last_seen, c.fixed_at, c.stack, COALESCE(c.stack_fingerprint, ''),
			c.input_hash, c.minimized_input,
			COALESCE(hex(c.input_bytes), ''), COALESCE(c.input_truncated, FALSE),
			COALESCE(c.issue_url, ''),
			COALESCE(c.git_sha, ''), COALESCE(c.dirty, FALSE), COALESCE(c.go_version, ''),
			COALESCE(c.blamed_module, ''), COALESCE(c.blamed_version, ''), COALESCE(c.blamed_file, ''), COALESCE(c.blamed_line, 0),
			COALESCE((SELECT r.outcome FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1), ''),
			COALESCE((SELECT r.git_sha FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1), ''),
			(SELECT r.checked_at FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1),
			COALESCE((SELECT r.diagnostic FROM crash_replays r WHERE r.crash_id = c.id ORDER BY r.checked_at DESC LIMIT 1), '')
		FROM crashes c
		JOIN targets t ON t.id = c.target_id
		JOIN projects p ON p.id = t.project_id
	`
}

func scanCrashRow(scanner interface{ Scan(...any) error }) (Crash, error) {
	var c Crash
	var fixedAt sql.NullTime
	var replayCheckedAt sql.NullTime
	var inputHex string
	if err := scanner.Scan(
		&c.ID, &c.TargetID, &c.ProjectName, &c.Package, &c.TargetName,
		&c.PublicInputs,
		&c.FirstSeen, &c.LastSeen, &fixedAt, &c.Stack, &c.StackFingerprint,
		&c.InputHash, &c.MinimizedInput,
		&inputHex, &c.InputTruncated,
		&c.IssueURL,
		&c.GitSHA, &c.Dirty, &c.GoVersion,
		&c.BlamedModule, &c.BlamedVersion, &c.BlamedFile, &c.BlamedLine,
		&c.ReplayStatus, &c.ReplayGitSHA, &replayCheckedAt, &c.ReplayDiagnostic,
	); err != nil {
		return Crash{}, err
	}
	if fixedAt.Valid {
		tm := fixedAt.Time
		c.FixedAt = &tm
	}
	if replayCheckedAt.Valid {
		tm := replayCheckedAt.Time
		c.ReplayCheckedAt = &tm
	}
	if inputHex != "" {
		if raw, err := hex.DecodeString(inputHex); err == nil {
			c.InputBytes = raw
		}
	}
	return c, nil
}

// GetCrash returns a single crash record by ID, or ErrNotFound if it does not exist.
func (s *Store) GetCrash(ctx context.Context, crashID int64) (Crash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := s.crashSelectSQL(ctx) + ` WHERE c.id = ?`
	row := s.queryRow(ctx, query, crashID)
	c, err := scanCrashRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Crash{}, fmt.Errorf("crash %d: %w", crashID, ErrNotFound)
	}
	if err != nil {
		return Crash{}, fmt.Errorf("get crash %d: %w", crashID, err)
	}
	return c, nil
}

// ListCrashes returns crashes, optionally filtering to open crashes (fixed_at IS NULL) and/or a specific target.
func (s *Store) ListCrashes(ctx context.Context, openOnly bool, targetFilter string) ([]Crash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := s.crashSelectSQL(ctx)
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
		c, err := scanCrashRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan crash: %w", err)
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
	case StatsQueryBurndown:
		return StatsQueryBurndown, nil
	default:
		return "", fmt.Errorf("%w: unknown query %q (expected throughput, coverage, outcomes, revisions, or burndown)", ErrInvalidStatsParam, raw)
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
	if qName == StatsQueryBurndown {
		return s.GetBurndown(ctx, windowName, projectFilter, targetFilter)
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
