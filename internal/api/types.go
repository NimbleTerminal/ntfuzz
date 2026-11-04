package api

import (
	"time"

	"github.com/NimbleTerminal/ntfuzz/internal/orchestrator"
	"github.com/NimbleTerminal/ntfuzz/internal/store"
)

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

// ProjectFromStore converts a store.Project into an API Project DTO.
func ProjectFromStore(p store.Project) Project {
	return Project{
		ID:             p.ID,
		Name:           p.Name,
		Repo:           p.Repo,
		Branch:         p.Branch,
		GitSHA:         p.GitSHA,
		Dirty:          p.Dirty,
		SyncError:      p.SyncError,
		LastSynced:     p.LastSynced,
		TargetCount:    p.TargetCount,
		TotalRuns:      p.TotalRuns,
		OpenCrashes:    p.OpenCrashes,
		LatestStatus:   p.LatestStatus,
		LastRunAt:      p.LastRunAt,
		AvgExecsPerSec: p.AvgExecsPerSec,
		AvgCoveragePct: p.AvgCoveragePct,
	}
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

// TargetFromStore converts a store.Target into an API Target DTO.
func TargetFromStore(t store.Target) Target {
	return Target{
		ID:             t.ID,
		ProjectID:      t.ProjectID,
		ProjectName:    t.ProjectName,
		Package:        t.Package,
		Name:           t.Name,
		LastRunID:      t.LastRunID,
		LastStarted:    t.LastStarted,
		DurationSec:    t.DurationSec,
		Execs:          t.Execs,
		ExecsPerSec:    t.ExecsPerSec,
		NewInteresting: t.NewInteresting,
		CorpusCount:    t.CorpusCount,
		Crashes:        t.Crashes,
		CoveragePct:    t.CoveragePct,
		Status:         t.Status,
		Worker:         t.Worker,
		GitSHA:         t.GitSHA,
		Dirty:          t.Dirty,
		GoVersion:      t.GoVersion,
		OpenCrashes:    t.OpenCrashes,
		TotalRuns:      t.TotalRuns,
		CoverageTrend:  t.CoverageTrend,
	}
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

// RunFromStore converts a store.Run into an API Run DTO.
func RunFromStore(r store.Run) Run {
	return Run{
		ID:             r.ID,
		TargetID:       r.TargetID,
		ProjectName:    r.ProjectName,
		Package:        r.Package,
		TargetName:     r.TargetName,
		Started:        r.Started,
		DurationSec:    r.DurationSec,
		Execs:          r.Execs,
		ExecsPerSec:    r.ExecsPerSec,
		NewInteresting: r.NewInteresting,
		CorpusCount:    r.CorpusCount,
		Crashes:        r.Crashes,
		CoveragePct:    r.CoveragePct,
		Status:         r.Status,
		Worker:         r.Worker,
		GitSHA:         r.GitSHA,
		Dirty:          r.Dirty,
		GoVersion:      r.GoVersion,
	}
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

// CrashFromStore converts a store.Crash into an API Crash DTO.
func CrashFromStore(c store.Crash) Crash {
	return Crash{
		ID:               c.ID,
		TargetID:         c.TargetID,
		ProjectName:      c.ProjectName,
		Package:          c.Package,
		TargetName:       c.TargetName,
		FirstSeen:        c.FirstSeen,
		LastSeen:         c.LastSeen,
		FixedAt:          c.FixedAt,
		Stack:            c.Stack,
		StackFingerprint: c.StackFingerprint,
		InputHash:        c.InputHash,
		MinimizedInput:   c.MinimizedInput,
		IssueURL:         c.IssueURL,
		GitSHA:           c.GitSHA,
		Dirty:            c.Dirty,
		GoVersion:        c.GoVersion,
	}
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

// CoveragePointFromStore converts a store.CoveragePoint into an API CoveragePoint DTO.
func CoveragePointFromStore(pt store.CoveragePoint) CoveragePoint {
	return CoveragePoint{
		RunID:          pt.RunID,
		TargetID:       pt.TargetID,
		Started:        pt.Started,
		CoveragePct:    pt.CoveragePct,
		ExecsPerSec:    pt.ExecsPerSec,
		NewInteresting: pt.NewInteresting,
		CorpusCount:    pt.CorpusCount,
		GitSHA:         pt.GitSHA,
	}
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

// ActiveJobFromOrchestrator converts an orchestrator.ActiveJob into an API ActiveJob DTO.
func ActiveJobFromOrchestrator(j orchestrator.ActiveJob) ActiveJob {
	return ActiveJob{
		WorkerID:  j.WorkerID,
		Project:   j.Project,
		Package:   j.Package,
		Target:    j.Target,
		FuzzTime:  j.FuzzTime,
		GitSHA:    j.GitSHA,
		Dirty:     j.Dirty,
		StartedAt: j.StartedAt,
		Status:    j.Status,
	}
}

// StatusResponse is returned by GET /api/status.
type StatusResponse struct {
	ReadOnly      bool        `json:"read_only"`
	SnapshotAt    *time.Time  `json:"snapshot_at,omitempty"`
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
