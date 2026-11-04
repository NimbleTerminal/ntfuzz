package api

import "time"

// Project is the JSON wire representation of a fuzzing project.
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

// Target is the JSON wire representation of an active Go fuzz target.
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

// Run is the JSON wire representation of a completed fuzz run.
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
	CorpusCount    int       `json:"corpus_count"`
	Crashes        int       `json:"crashes"`
	CoveragePct    float64   `json:"coverage_pct"`
	Status         string    `json:"status"`
	Worker         string    `json:"worker"`
	GitSHA         string    `json:"git_sha,omitempty"`
	Dirty          bool      `json:"dirty"`
	GoVersion      string    `json:"go_version,omitempty"`
}

// Crash is the JSON wire representation of a fuzzing crash.
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

// CoveragePoint is the JSON wire representation of a point in a target's coverage & corpus history.
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

// ActiveJob describes an in-flight fuzzing worker job in GET /api/status.
type ActiveJob struct {
	WorkerID  string    `json:"worker_id"`
	Project   string    `json:"project"`
	Package   string    `json:"package"`
	Target    string    `json:"target"`
	FuzzTime  string    `json:"fuzztime"`
	GitSHA    string    `json:"git_sha,omitempty"`
	Dirty     bool      `json:"dirty"`
	StartedAt time.Time `json:"started_at"`
	Status    string    `json:"status"`
}

// StatusResponse is returned by GET /api/status.
type StatusResponse struct {
	FleetHealth   string      `json:"fleet_health"`
	OpenCrashes   int         `json:"open_crashes"`
	RunsLast24h   int         `json:"runs_last_24h"`
	ExecsLast24h  int64       `json:"execs_last_24h"`
	AvgExecsSec   float64     `json:"avg_execs_per_sec"`
	TotalProjects int         `json:"total_projects"`
	TotalTargets  int         `json:"total_targets"`
	ActiveWorkers int         `json:"active_workers"`
	MaxWorkers    int         `json:"max_workers"`
	SchemaVersion int         `json:"schema_version"`
	ActiveJobs    []ActiveJob `json:"active_jobs"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// FixCrashRequest is the JSON request body for POST /api/crashes/{id}/fix.
type FixCrashRequest struct {
	IssueURL string `json:"issue_url,omitempty"`
}
