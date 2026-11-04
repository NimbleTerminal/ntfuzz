package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/NimbleTerminal/ntfuzzy/internal/store"
)

// ActiveJob describes an in-flight fuzzing worker job managed by the orchestrator.
// Owned by internal/orchestrator so internal/api depends on internal/orchestrator, not vice versa.
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
	checkout  CheckoutInfo
	target    DiscoveredTarget
	targetID  int64
	priority  int // lower runs first (0 = never run or recently added corpus, >0 = flat streak)
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

	mu          sync.RWMutex
	activeJobs  map[string]ActiveJob
	moduleLocks map[string]*sync.Mutex
	flatStreaks map[int64]int
	skipCounts  map[int64]int
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
		activeJobs:    make(map[string]ActiveJob),
		moduleLocks:   make(map[string]*sync.Mutex),
		flatStreaks:   make(map[int64]int),
		skipCounts:    make(map[int64]int),
	}
}

// ActiveJobs returns a snapshot of currently running fuzz jobs.
func (o *Orchestrator) ActiveJobs() []ActiveJob {
	o.mu.RLock()
	defer o.mu.RUnlock()

	jobs := make([]ActiveJob, 0, len(o.activeJobs))
	for _, j := range o.activeJobs {
		jobs = append(jobs, j)
	}
	return jobs
}

// WorkerCount returns the configured maximum number of concurrent workers.
func (o *Orchestrator) WorkerCount() int {
	return o.workers
}

// SyncAll synchronizes all configured projects, records git revision state (or visible sync errors),
// and reconciles discovered fuzz targets in DuckDB.
func (o *Orchestrator) SyncAll(ctx context.Context) []scheduledTarget {
	var scheduled []scheduledTarget

	for _, proj := range o.cfg.Projects {
		if ctx.Err() != nil {
			break
		}
		checkout, err := EnsureCheckout(ctx, proj, o.cfg.WorkDir)
		if err != nil {
			o.logf("WARN", "project=%s msg=%q err=%q", proj.Name, "checkout/pull failed", err)
			// Record the sync error visibly WITHOUT advancing last_synced
			_, _ = o.store.SyncProject(ctx, store.ProjectSyncInput{
				Name:      proj.Name,
				Repo:      proj.Repo,
				Branch:    proj.Branch,
				GitSHA:    checkout.GitSHA,
				Dirty:     checkout.Dirty,
				SyncedAt:  nil,
				SyncError: err.Error(),
			})
			continue
		}

		now := time.Now().UTC()
		projID, err := o.store.SyncProject(ctx, store.ProjectSyncInput{
			Name:      proj.Name,
			Repo:      proj.Repo,
			Branch:    proj.Branch,
			GitSHA:    checkout.GitSHA,
			Dirty:     checkout.Dirty,
			SyncedAt:  &now,
			SyncError: "",
		})
		if err != nil {
			o.logf("ERROR", "project=%s msg=%q err=%q", proj.Name, "store sync project failed", err)
			continue
		}

		targets, err := DiscoverTargets(ctx, checkout.RepoDir)
		if err != nil {
			o.logf("WARN", "project=%s dir=%s msg=%q err=%q", proj.Name, checkout.RepoDir, "target discovery failed", err)
			continue
		}

		keys := make([]store.TargetKey, 0, len(targets))
		for _, disc := range targets {
			keys = append(keys, store.TargetKey{
				Package: disc.PackageImport,
				Name:    disc.FuncName,
			})
		}

		reconciledIDs, err := o.store.ReconcileProjectTargets(ctx, projID, keys)
		if err != nil {
			o.logf("ERROR", "project=%s msg=%q err=%q", proj.Name, "reconcile targets failed", err)
			continue
		}

		o.logf("INFO", "project=%s sha=%s dirty=%t targets=%d dir=%s msg=%q",
			proj.Name, checkout.GitSHA, checkout.Dirty, len(targets), checkout.RepoDir, "synced project")

		for _, disc := range targets {
			tID := reconciledIDs[store.TargetKey{Package: disc.PackageImport, Name: disc.FuncName}]
			if tID == 0 {
				continue
			}
			scheduled = append(scheduled, scheduledTarget{
				project:   proj,
				projectID: projID,
				checkout:  checkout,
				target:    disc,
				targetID:  tID,
				priority:  o.targetPriority(tID),
			})
		}
	}

	return scheduled
}

// RunOnce performs one synchronization, prioritizes targets that recently grew corpus,
// and dispatches jobs across workers while serializing access per Go module directory.
func (o *Orchestrator) RunOnce(ctx context.Context) {
	scheduled := o.SyncAll(ctx)
	if len(scheduled) == 0 || ctx.Err() != nil {
		return
	}

	selected := o.selectDueTargets(scheduled)

	jobsCh := make(chan scheduledTarget, len(selected))
	for _, item := range selected {
		jobsCh <- item
	}
	close(jobsCh)

	workerCount := min(o.workers, len(selected))
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

	// Apply 14-day run retention policy while keeping at least the latest 10 runs per target
	_, _ = o.store.PruneOldRuns(ctx, 14*24*time.Hour, 10)
}

func (o *Orchestrator) selectDueTargets(all []scheduledTarget) []scheduledTarget {
	o.mu.Lock()
	defer o.mu.Unlock()

	sort.SliceStable(all, func(i, j int) bool {
		return all[i].priority < all[j].priority
	})

	var due []scheduledTarget
	for _, item := range all {
		streak := o.flatStreaks[item.targetID]
		// Back off targets with 3+ consecutive flat runs for up to 2 sweeps
		maxSkip := min(streak/3, 2)
		if maxSkip > 0 && o.skipCounts[item.targetID] < maxSkip {
			o.skipCounts[item.targetID]++
			continue
		}
		o.skipCounts[item.targetID] = 0
		due = append(due, item)
	}
	if len(due) == 0 {
		return all
	}
	return due
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
	moduleDir := item.target.ModuleDir
	if moduleDir == "" {
		moduleDir = item.checkout.RepoDir
	}

	// Ensure two workers never run `go test` in the same module directory simultaneously
	modLock := o.moduleLock(moduleDir)
	modLock.Lock()
	defer modLock.Unlock()

	fuzzTime := item.project.EffectiveFuzzTime(o.cfg.DefaultFuzzTime)
	corpusDir := item.project.EffectiveCorpusDir(o.cfg.CorpusDir)
	isRemoteClone := !isLocalPathRepo(item.project.Repo)

	job := JobSpec{
		WorkerID:        workerID,
		Project:         item.project.Name,
		RepoDir:         moduleDir,
		PkgImport:       item.target.PackageImport,
		PkgRelDir:       item.target.PackageRelDir,
		TargetName:      item.target.FuncName,
		FuzzTime:        fuzzTime,
		CorpusDir:       corpusDir,
		CleanCrashInGit: isRemoteClone,
	}

	o.setActiveJob(workerID, ActiveJob{
		WorkerID:  workerID,
		Project:   job.Project,
		Package:   job.PkgImport,
		Target:    job.TargetName,
		FuzzTime:  fuzzTime.String(),
		GitSHA:    item.checkout.GitSHA,
		Dirty:     item.checkout.Dirty,
		StartedAt: time.Now().UTC(),
		Status:    "fuzzing",
	})
	defer o.clearActiveJob(workerID)

	o.logf("INFO", "worker=%s project=%s target=%s sha=%s fuzztime=%s msg=%q",
		workerID, job.Project, job.TargetName, item.checkout.GitSHA, fuzzTime, "fuzzing target")

	res, err := o.runner.RunTarget(ctx, job)
	if err != nil && ctx.Err() != nil {
		return
	}
	if err != nil {
		o.logf("WARN", "worker=%s project=%s target=%s msg=%q err=%q",
			workerID, job.Project, job.TargetName, "runner error", err)
	}

	o.recordTargetActivity(item.targetID, res.NewInteresting, res.Crashes)

	_, storeErr := o.store.InsertRun(ctx, store.Run{
		TargetID:       item.targetID,
		Started:        res.Started,
		DurationSec:    res.DurationSec,
		Execs:          res.Execs,
		ExecsPerSec:    res.ExecsPerSec,
		NewInteresting: res.NewInteresting,
		CorpusCount:    res.CorpusCount,
		Crashes:        res.Crashes,
		CoveragePct:    res.CoveragePct,
		Status:         res.Status,
		Worker:         workerID,
		GitSHA:         item.checkout.GitSHA,
		Dirty:          item.checkout.Dirty,
		GoVersion:      item.checkout.GoVersion,
	})
	if storeErr != nil {
		o.logf("ERROR", "worker=%s project=%s target=%s msg=%q err=%q",
			workerID, job.Project, job.TargetName, "insert run failed", storeErr)
	}

	for _, crash := range res.CrashRecords {
		minInput := crash.MinimizedInput
		if minInput != "" && minInput != "inline-failure" && !filepath.IsAbs(minInput) {
			pkgDir := moduleDir
			if item.target.PackageRelDir != "" && item.target.PackageRelDir != "." {
				pkgDir = filepath.Join(moduleDir, item.target.PackageRelDir)
			}
			minInput = filepath.Join(pkgDir, minInput)
		}
		_, crashErr := o.store.RecordCrash(ctx, store.Crash{
			TargetID:         item.targetID,
			FirstSeen:        res.Started,
			LastSeen:         time.Now().UTC(),
			Stack:            crash.Stack,
			StackFingerprint: crash.StackFingerprint,
			InputHash:        crash.InputHash,
			MinimizedInput:   minInput,
			GitSHA:           item.checkout.GitSHA,
			Dirty:            item.checkout.Dirty,
			GoVersion:        item.checkout.GoVersion,
		})
		if crashErr != nil {
			o.logf("ERROR", "worker=%s project=%s target=%s msg=%q err=%q",
				workerID, job.Project, job.TargetName, "record crash failed", crashErr)
		} else {
			o.logf("WARN", "worker=%s project=%s target=%s hash=%s fp=%s msg=%q",
				workerID, job.Project, job.TargetName, crash.InputHash, crash.StackFingerprint, "crash recorded")
		}
	}

	o.logf("INFO", "worker=%s project=%s target=%s sha=%s status=%s execs=%d rate=%.0f/s new=%d corpus=%d cov=%.1f%% crashes=%d",
		workerID, job.Project, job.TargetName, item.checkout.GitSHA, res.Status,
		res.Execs, res.ExecsPerSec, res.NewInteresting, res.CorpusCount, res.CoveragePct, res.Crashes)
}

func (o *Orchestrator) moduleLock(moduleDir string) *sync.Mutex {
	o.mu.Lock()
	defer o.mu.Unlock()
	if m, ok := o.moduleLocks[moduleDir]; ok {
		return m
	}
	m := &sync.Mutex{}
	o.moduleLocks[moduleDir] = m
	return m
}

func (o *Orchestrator) targetPriority(targetID int64) int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.flatStreaks[targetID]
}

func (o *Orchestrator) recordTargetActivity(targetID int64, newInteresting, crashes int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if newInteresting > 0 || crashes > 0 {
		o.flatStreaks[targetID] = 0
		o.skipCounts[targetID] = 0
	} else {
		o.flatStreaks[targetID]++
	}
}

func (o *Orchestrator) setActiveJob(workerID string, job ActiveJob) {
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

func isLocalPathRepo(repo string) bool {
	info, err := os.Stat(repo)
	return err == nil && info.IsDir()
}
