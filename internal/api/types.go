package api

import (
	"time"

	"nimbleterminal.dev/ntfuzz/internal/orchestrator"
	"nimbleterminal.dev/ntfuzz/internal/store"
)

// Project is the JSON wire representation of a fuzzing project.
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

// RedactSyncError redacts URL credentials and labeled secrets from a sync error string.
func RedactSyncError(s string) string {
	return orchestrator.SanitizeDiagnostic(s)
}

// RedactRepoURL strips embedded credentials from a repository URL as defense in depth.
func RedactRepoURL(repo string) string {
	return orchestrator.RedactRepoURL(repo)
}

// ProjectFromStore converts a store.Project into an API Project DTO.
func ProjectFromStore(p store.Project) Project {
	return Project{
		ID:             p.ID,
		Name:           p.Name,
		Repo:           orchestrator.RedactRepoURL(p.Repo),
		Branch:         p.Branch,
		GitSHA:         p.GitSHA,
		Dirty:          p.Dirty,
		PublicInputs:   p.PublicInputs,
		SyncError:      orchestrator.SanitizeDiagnostic(p.SyncError),
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

// RunFromStore converts a store.Run into an API Run DTO.
func RunFromStore(r store.Run) Run {
	return Run{
		Diagnostic:     r.Diagnostic,
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
	PublicInputs     bool       `json:"public_inputs"`
	FirstSeen        time.Time  `json:"first_seen"`
	LastSeen         time.Time  `json:"last_seen"`
	FixedAt          *time.Time `json:"fixed_at,omitempty"`
	Stack            string     `json:"stack"`
	StackFingerprint string     `json:"stack_fingerprint,omitempty"`
	InputHash        string     `json:"input_hash"`
	MinimizedInput   string     `json:"minimized_input"`
	InputTruncated   bool       `json:"input_truncated"`
	IssueURL         string     `json:"issue_url"`
	GitSHA           string     `json:"git_sha,omitempty"`
	Dirty            bool       `json:"dirty"`
	GoVersion        string     `json:"go_version,omitempty"`
	BlamedModule     string     `json:"blamed_module,omitempty"`
	BlamedVersion    string     `json:"blamed_version,omitempty"`
	BlamedFile       string     `json:"blamed_file,omitempty"`
	BlamedLine       int        `json:"blamed_line,omitempty"`
	BlamedURL        string     `json:"blamed_url,omitempty"`
	ReplayStatus     string     `json:"replay_status,omitempty"`
	ReplayGitSHA     string     `json:"replay_git_sha,omitempty"`
	ReplayCheckedAt  *time.Time `json:"replay_checked_at,omitempty"`
	ReplayDiagnostic string     `json:"replay_diagnostic,omitempty"`
}

// CrashFromStore converts a store.Crash into an API Crash DTO.
func CrashFromStore(c store.Crash) Crash {
	modRoot := orchestrator.DeriveModuleRoot(c.ProjectName, c.Package)
	scrubbedStack := orchestrator.ScrubStack(c.Stack, "", modRoot, "")
	blamedMod := c.BlamedModule
	blamedVer := c.BlamedVersion
	blamedFile := c.BlamedFile
	blamedLine := c.BlamedLine
	if blamedMod == "" && scrubbedStack != "" {
		if attr := orchestrator.AttributeStack(scrubbedStack, "", modRoot); attr.Module != "" {
			blamedMod = attr.Module
			blamedVer = attr.Version
			blamedFile = attr.File
			blamedLine = attr.Line
		}
	}
	return Crash{
		ID:               c.ID,
		TargetID:         c.TargetID,
		ProjectName:      c.ProjectName,
		Package:          c.Package,
		TargetName:       c.TargetName,
		PublicInputs:     c.PublicInputs,
		FirstSeen:        c.FirstSeen,
		LastSeen:         c.LastSeen,
		FixedAt:          c.FixedAt,
		Stack:            scrubbedStack,
		StackFingerprint: c.StackFingerprint,
		InputHash:        c.InputHash,
		MinimizedInput:   c.MinimizedInput,
		InputTruncated:   c.InputTruncated,
		IssueURL:         c.IssueURL,
		GitSHA:           c.GitSHA,
		Dirty:            c.Dirty,
		GoVersion:        c.GoVersion,
		BlamedModule:     blamedMod,
		BlamedVersion:    blamedVer,
		BlamedFile:       blamedFile,
		BlamedLine:       blamedLine,
		BlamedURL:        orchestrator.DependencySourceURL(blamedMod, blamedVer, blamedFile, blamedLine),
		ReplayStatus:     c.ReplayStatus,
		ReplayGitSHA:     c.ReplayGitSHA,
		ReplayCheckedAt:  c.ReplayCheckedAt,
		ReplayDiagnostic: c.ReplayDiagnostic,
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
	Version       string      `json:"version"`
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

// IssueCrashRequest is the JSON request body for POST /api/crashes/{id}/issue.
type IssueCrashRequest struct {
	IssueURL string `json:"issue_url"`
}

// Canned stats query identifiers exposed by GET /api/stats and GET /api/stats/{query}.
const (
	StatsQueryThroughput = store.StatsQueryThroughput
	StatsQueryCoverage   = store.StatsQueryCoverage
	StatsQueryOutcomes   = store.StatsQueryOutcomes
	StatsQueryRevisions  = store.StatsQueryRevisions
	StatsQueryBurndown   = store.StatsQueryBurndown
)

// Supported time window identifiers for GET /api/stats and GET /api/stats/{query}.
const (
	StatsWindow1h  = store.StatsWindow1h
	StatsWindow6h  = store.StatsWindow6h
	StatsWindow24h = store.StatsWindow24h
	StatsWindow7d  = store.StatsWindow7d
	StatsWindow14d = store.StatsWindow14d
)

// StatsBucket is the JSON wire representation of a single time-bucketed stats point.
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

// StatsBucketFromStore converts a store.StatsBucket into an API StatsBucket DTO.
func StatsBucketFromStore(b store.StatsBucket) StatsBucket {
	return StatsBucket{OpenCrashes: b.OpenCrashes, NewCrashes: b.NewCrashes, ReopenedCrashes: b.ReopenedCrashes, ResolvedCrashes: b.ResolvedCrashes, DetectedRevisions: b.DetectedRevisions, TestedRevisions: b.TestedRevisions,
		BucketStart:    b.BucketStart,
		Runs:           b.Runs,
		Execs:          b.Execs,
		DurationSec:    b.DurationSec,
		AvgExecsPerSec: b.AvgExecsPerSec,
		PeakExecsSec:   b.PeakExecsSec,
		AvgCoveragePct: b.AvgCoveragePct,
		MaxCoveragePct: b.MaxCoveragePct,
		NewInteresting: b.NewInteresting,
		CorpusCount:    b.CorpusCount,
		OKRuns:         b.OKRuns,
		CrashRuns:      b.CrashRuns,
		ErrorRuns:      b.ErrorRuns,
		IncompleteRuns: b.IncompleteRuns,
		CanceledRuns:   b.CanceledRuns,
	}
}

// StatsResponse is returned by GET /api/stats and GET /api/stats/{query}.
type StatsResponse struct {
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

// StatsResponseFromStore converts a store.StatsSeries into an API StatsResponse DTO.
func StatsResponseFromStore(s store.StatsSeries) StatsResponse {
	buckets := make([]StatsBucket, 0, len(s.Buckets))
	for _, b := range s.Buckets {
		buckets = append(buckets, StatsBucketFromStore(b))
	}
	var revisions []Revision
	if len(s.Revisions) > 0 {
		revisions = make([]Revision, len(s.Revisions))
		for i, r := range s.Revisions {
			r.SyncError = orchestrator.SanitizeDiagnostic(r.SyncError)
			revisions[i] = r
		}
	}
	return StatsResponse{HistoryEstimatedBefore: s.HistoryEstimatedBefore, OpenCrashes: s.OpenCrashes, NewCrashes: s.NewCrashes, ReopenedCrashes: s.ReopenedCrashes, ResolvedCrashes: s.ResolvedCrashes, Revisions: revisions,
		Query:          s.Query,
		Window:         s.Window,
		BucketSeconds:  s.BucketSeconds,
		Project:        s.Project,
		Target:         s.Target,
		TotalRuns:      s.TotalRuns,
		TotalExecs:     s.TotalExecs,
		AvgExecsPerSec: s.AvgExecsPerSec,
		PeakExecsSec:   s.PeakExecsSec,
		AvgCoveragePct: s.AvgCoveragePct,
		MaxCoveragePct: s.MaxCoveragePct,
		NewInteresting: s.NewInteresting,
		CorpusCount:    s.CorpusCount,
		OKRuns:         s.OKRuns,
		CrashRuns:      s.CrashRuns,
		ErrorRuns:      s.ErrorRuns,
		IncompleteRuns: s.IncompleteRuns,
		CanceledRuns:   s.CanceledRuns,
		Buckets:        buckets,
	}
}

// Revision describes an observed repository revision and its verification progress.
type Revision = store.Revision
