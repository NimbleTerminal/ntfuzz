package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/NimbleTerminal/ntfuzzy/internal/api"
	"github.com/NimbleTerminal/ntfuzzy/internal/store"
)

// Options configures the Orchestrator runtime parameters.
type Options struct {
	Workers       int
	RoundInterval time.Duration
	Runner        WorkerRunner
	Logger        *log.Logger
}

type scheduledTarget struct {
	project   ProjectConfig
	projectID int64
	repoDir   string
	target    DiscoveredTarget
	targetID  int64
}

// Orchestrator coordinates continuous checkout, target discovery, fuzzing execution,
// and DuckDB persistence across a fleet of Go repositories.
type Orchestrator struct {
	cfg           Config
	store         *store.Store
	runner        WorkerRunner
	workers       int
	roundInterval time.Duration
	logger        *log.Logger

	mu         sync.RWMutex
	activeJobs map[string]api.ActiveJob
}

// New creates a new Orchestrator instance.
func New(cfg Config, st *store.Store, opts Options) *Orchestrator {
	workers := opts.Workers
	if workers <= 0 {
		workers = 2
	}
	interval := opts.RoundInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	runner := opts.Runner
	if runner == nil {
		runner = NewLocalRunner()
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}

	return &Orchestrator{
		cfg:           cfg,
		store:         st,
		runner:        runner,
		workers:       workers,
		roundInterval: interval,
		logger:        logger,
		activeJobs:    make(map[string]api.ActiveJob),
	}
}

// ActiveJobs implements api.WorkerStatusProvider.
func (o *Orchestrator) ActiveJobs() []api.ActiveJob {
	o.mu.RLock()
	defer o.mu.RUnlock()

	jobs := make([]api.ActiveJob, 0, len(o.activeJobs))
	for _, j := range o.activeJobs {
		jobs = append(jobs, j)
	}
	return jobs
}

// WorkerCount implements api.WorkerStatusProvider.
func (o *Orchestrator) WorkerCount() int {
	return o.workers
}

// SyncAll synchronizes all configured projects and upserts their discovered fuzz targets into DuckDB.
// Individual project failures are logged and skipped so one bad repo never stops the fleet.
func (o *Orchestrator) SyncAll(ctx context.Context) []scheduledTarget {
	var scheduled []scheduledTarget

	for _, proj := range o.cfg.Projects {
		if ctx.Err() != nil {
			break
		}
		repoDir, err := EnsureCheckout(ctx, proj, o.cfg.WorkDir)
		if err != nil {
			o.logf("WARN", "project=%s msg=%q err=%q", proj.Name, "checkout failed", err)
			_, _ = o.store.UpsertProject(ctx, proj.Name, proj.Repo, proj.Branch, nil)
			continue
		}

		now := time.Now().UTC()
		projID, err := o.store.UpsertProject(ctx, proj.Name, proj.Repo, proj.Branch, &now)
		if err != nil {
			o.logf("ERROR", "project=%s msg=%q err=%q", proj.Name, "store upsert project failed", err)
			continue
		}

		targets, err := DiscoverTargets(ctx, repoDir)
		if err != nil {
			o.logf("WARN", "project=%s dir=%s msg=%q err=%q", proj.Name, repoDir, "target discovery failed", err)
			continue
		}

		o.logf("INFO", "project=%s targets=%d dir=%s msg=%q", proj.Name, len(targets), repoDir, "synced project")

		for _, disc := range targets {
			tID, err := o.store.UpsertTarget(ctx, projID, disc.PackageImport, disc.FuncName)
			if err != nil {
				o.logf("ERROR", "project=%s target=%s msg=%q err=%q", proj.Name, disc.FuncName, "store upsert target failed", err)
				continue
			}
			scheduled = append(scheduled, scheduledTarget{
				project:   proj,
				projectID: projID,
				repoDir:   repoDir,
				target:    disc,
				targetID:  tID,
			})
		}
	}

	return scheduled
}

// RunOnce performs one complete synchronization and fuzzing pass over all discovered targets.
func (o *Orchestrator) RunOnce(ctx context.Context) {
	scheduled := o.SyncAll(ctx)
	if len(scheduled) == 0 || ctx.Err() != nil {
		return
	}

	jobsCh := make(chan scheduledTarget, len(scheduled))
	for _, item := range scheduled {
		jobsCh <- item
	}
	close(jobsCh)

	workerCount := min(o.workers, len(scheduled))
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		workerID := fmt.Sprintf("local-%d", i+1)
		go func(wID string) {
			defer wg.Done()
			for item := range jobsCh {
				if ctx.Err() != nil {
					return
				}
				o.executeTarget(ctx, wID, item)
			}
		}(workerID)
	}
	wg.Wait()
}

// Run starts the continuous orchestration loop until ctx is canceled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.logf("INFO", "projects=%d workers=%d msg=%q", len(o.cfg.Projects), o.workers, "starting orchestrator loop")

	for {
		if ctx.Err() != nil {
			return nil
		}
		o.RunOnce(ctx)

		select {
		case <-ctx.Done():
			o.logf("INFO", "msg=%q", "orchestrator loop stopped")
			return nil
		case <-time.After(o.roundInterval):
		}
	}
}

func (o *Orchestrator) executeTarget(ctx context.Context, workerID string, item scheduledTarget) {
	fuzzTime := item.project.EffectiveFuzzTime(o.cfg.DefaultFuzzTime)
	corpusDir := item.project.EffectiveCorpusDir(o.cfg.CorpusDir)

	job := JobSpec{
		WorkerID:   workerID,
		Project:    item.project.Name,
		RepoDir:    item.repoDir,
		PkgImport:  item.target.PackageImport,
		PkgRelDir:  item.target.PackageRelDir,
		TargetName: item.target.FuncName,
		FuzzTime:   fuzzTime,
		CorpusDir:  corpusDir,
	}

	o.setActiveJob(workerID, api.ActiveJob{
		WorkerID:  workerID,
		Project:   job.Project,
		Package:   job.PkgImport,
		Target:    job.TargetName,
		FuzzTime:  fuzzTime.String(),
		StartedAt: time.Now().UTC(),
		Status:    "fuzzing",
	})
	defer o.clearActiveJob(workerID)

	o.logf("INFO", "worker=%s project=%s target=%s fuzztime=%s msg=%q",
		workerID, job.Project, job.TargetName, fuzzTime, "fuzzing target")

	res, err := o.runner.RunTarget(ctx, job)
	if err != nil && ctx.Err() != nil {
		return
	}
	if err != nil {
		o.logf("WARN", "worker=%s project=%s target=%s msg=%q err=%q",
			workerID, job.Project, job.TargetName, "runner error", err)
	}

	_, storeErr := o.store.InsertRun(ctx, store.Run{
		TargetID:       item.targetID,
		Started:        res.Started,
		DurationSec:    res.DurationSec,
		Execs:          res.Execs,
		ExecsPerSec:    res.ExecsPerSec,
		NewInteresting: res.NewInteresting,
		Crashes:        res.Crashes,
		CoveragePct:    res.CoveragePct,
		Status:         res.Status,
		Worker:         workerID,
	})
	if storeErr != nil {
		o.logf("ERROR", "worker=%s project=%s target=%s msg=%q err=%q",
			workerID, job.Project, job.TargetName, "insert run failed", storeErr)
	}

	for _, crash := range res.CrashRecords {
		minInput := crash.MinimizedInput
		if minInput != "" && minInput != "inline-failure" && !filepath.IsAbs(minInput) {
			pkgDir := item.repoDir
			if item.target.PackageRelDir != "" && item.target.PackageRelDir != "." {
				pkgDir = filepath.Join(item.repoDir, item.target.PackageRelDir)
			}
			minInput = filepath.Join(pkgDir, minInput)
		}
		_, crashErr := o.store.RecordCrash(ctx, store.Crash{
			TargetID:       item.targetID,
			FirstSeen:      res.Started,
			LastSeen:       time.Now().UTC(),
			Stack:          crash.Stack,
			InputHash:      crash.InputHash,
			MinimizedInput: minInput,
		})
		if crashErr != nil {
			o.logf("ERROR", "worker=%s project=%s target=%s msg=%q err=%q",
				workerID, job.Project, job.TargetName, "record crash failed", crashErr)
		} else {
			o.logf("WARN", "worker=%s project=%s target=%s hash=%s msg=%q",
				workerID, job.Project, job.TargetName, crash.InputHash, "crash recorded")
		}
	}

	o.logf("INFO", "worker=%s project=%s target=%s status=%s execs=%d rate=%.0f/s new=%d cov=%.1f%% crashes=%d",
		workerID, job.Project, job.TargetName, res.Status, res.Execs, res.ExecsPerSec, res.NewInteresting, res.CoveragePct, res.Crashes)
}

func (o *Orchestrator) setActiveJob(workerID string, job api.ActiveJob) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.activeJobs[workerID] = job
}

func (o *Orchestrator) clearActiveJob(workerID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.activeJobs, workerID)
}

func (o *Orchestrator) logf(level, format string, args ...any) {
	if o.logger == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	o.logger.Printf("level=%s component=orchestrator %s", level, msg)
}
