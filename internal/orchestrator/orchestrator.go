package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/store"
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
	Logger        *slog.Logger
}

type scheduledTarget struct {
	project   ProjectConfig
	projectID int64
	checkout  CheckoutInfo
	target    DiscoveredTarget
	targetID  int64
	priority  int // lower runs first (0 = never run or recently added corpus, >0 = flat streak)
}

func (s scheduledTarget) effectiveModuleDir() string {
	if s.target.ModuleDir != "" {
		return s.target.ModuleDir
	}
	return s.checkout.RepoDir
}

// Orchestrator coordinates continuous checkout, target discovery, fuzzing execution,
// and DuckDB persistence across a fleet of Go repositories.
type Orchestrator struct {
	cfg           Config
	store         *store.Store
	runner        WorkerRunner
	workers       int
	roundInterval time.Duration
	logger        *slog.Logger

	mu          sync.RWMutex
	activeJobs  map[string]ActiveJob
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
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	return &Orchestrator{
		cfg:           cfg,
		store:         st,
		runner:        runner,
		workers:       workers,
		roundInterval: interval,
		logger:        logger,
		activeJobs:    make(map[string]ActiveJob),
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
			o.logger.Warn("checkout/pull failed",
				"component", "orchestrator",
				"project", proj.Name,
				"err", err,
			)
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
			o.logger.Error("store sync project failed",
				"component", "orchestrator",
				"project", proj.Name,
				"err", err,
			)
			continue
		}

		targets, err := DiscoverTargets(ctx, checkout.RepoDir)
		if err != nil {
			o.logger.Warn("target discovery failed",
				"component", "orchestrator",
				"project", proj.Name,
				"dir", checkout.RepoDir,
				"err", err,
			)
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
			o.logger.Error("reconcile targets failed",
				"component", "orchestrator",
				"project", proj.Name,
				"err", err,
			)
			continue
		}

		// Removed/renamed targets cannot verify old inputs; keep their crashes open.
		oldCrashes, listErr := o.store.ListCrashes(ctx, false, "")
		if listErr != nil {
			o.logger.Warn("list missing-target replays failed", "err", listErr)
		}
		for _, c := range oldCrashes {
			if c.ProjectName != proj.Name || c.GitSHA == checkout.GitSHA {
				continue
			}
			if _, present := reconciledIDs[store.TargetKey{Package: c.Package, Name: c.TargetName}]; present {
				continue
			}
			needed, e := o.store.NeedsReplay(ctx, c, checkout.GitSHA)
			if e != nil || !needed {
				continue
			}
			if e = o.store.RecordReplay(ctx, c, store.CrashReplay{GitSHA: checkout.GitSHA, CheckedAt: time.Now().UTC(), Outcome: "blocked", Diagnostic: "Target is no longer discovered on this revision"}); e != nil {
				o.logger.Warn("record missing-target replay failed", "err", e)
			}
		}

		o.logger.Info("synced project",
			"component", "orchestrator",
			"project", proj.Name,
			"sha", checkout.GitSHA,
			"dirty", checkout.Dirty,
			"targets", len(targets),
			"dir", checkout.RepoDir,
		)

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

// moduleQueue coordinates non-starving dispatch of targets across workers so that:
//  1. Two workers never execute inside the same Go module directory at the same time.
//  2. A worker skips currently locked modules and picks work from any unlocked module instead of blocking.
type moduleQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	pending  []scheduledTarget
	inFlight map[string]bool
	closed   bool
}

func newModuleQueue(items []scheduledTarget) *moduleQueue {
	q := &moduleQueue{
		pending:  append([]scheduledTarget(nil), items...),
		inFlight: make(map[string]bool),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *moduleQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *moduleQueue) acquire(ctx context.Context) (scheduledTarget, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if q.closed || ctx.Err() != nil || len(q.pending) == 0 {
			return scheduledTarget{}, false
		}
		for i, item := range q.pending {
			modDir := item.effectiveModuleDir()
			if !q.inFlight[modDir] {
				q.inFlight[modDir] = true
				q.pending = append(q.pending[:i], q.pending[i+1:]...)
				return item, true
			}
		}
		// All remaining targets belong to modules currently being fuzzed by other workers; wait for release.
		q.cond.Wait()
	}
}

func (q *moduleQueue) release(modDir string) {
	q.mu.Lock()
	delete(q.inFlight, modDir)
	q.cond.Broadcast()
	q.mu.Unlock()
}

// RunOnce performs one synchronization, prioritizes targets that recently grew corpus,
// and dispatches jobs across workers without starving workers on locked modules.
func (o *Orchestrator) RunOnce(ctx context.Context) {
	scheduled := o.SyncAll(ctx)
	if len(scheduled) == 0 || ctx.Err() != nil {
		return
	}

	selected := o.selectDueTargets(scheduled)
	queue := newModuleQueue(selected)
	stopWake := context.AfterFunc(ctx, queue.close)
	defer stopWake()

	workerCount := min(o.workers, len(selected))
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		workerID := fmt.Sprintf("local-%d", i+1)
		go func(wID string) {
			defer wg.Done()
			for {
				item, ok := queue.acquire(ctx)
				if !ok {
					return
				}
				o.executeTarget(ctx, wID, item)
				queue.release(item.effectiveModuleDir())
			}
		}(workerID)
	}
	wg.Wait()

	if ctx.Err() == nil {
		_, _ = o.store.PruneOldRuns(ctx, 14*24*time.Hour, 10)
	}
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
	o.logger.Info("starting orchestrator loop",
		"component", "orchestrator",
		"projects", len(o.cfg.Projects),
		"workers", o.workers,
	)

	for {
		if ctx.Err() != nil {
			return nil
		}
		o.RunOnce(ctx)

		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator loop stopped", "component", "orchestrator")
			return nil
		case <-time.After(o.roundInterval):
		}
	}
}

func (o *Orchestrator) executeTarget(ctx context.Context, workerID string, item scheduledTarget) {
	moduleDir := item.effectiveModuleDir()
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

	o.logger.Info("fuzzing target",
		"component", "orchestrator",
		"worker", workerID,
		"project", job.Project,
		"target", job.TargetName,
		"sha", item.checkout.GitSHA,
		"fuzztime", fuzzTime.String(),
	)

	o.replayCrashes(ctx, item, job)
	res, err := o.runner.RunTarget(ctx, job)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		res.Status = "canceled"
		res.Crashes, res.CrashRecords = 0, nil
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	if err != nil {
		if res.Status == "" {
			res.Status = "error"
		}
		res.Output = sanitizeDiagnostic(res.Output+"\nrunner: "+err.Error(), job.RepoDir, job.CorpusDir)
	}
	if err != nil {
		o.logger.Warn("runner error",
			"component", "orchestrator",
			"worker", workerID,
			"project", job.Project,
			"target", job.TargetName,
			"err", err,
		)
	}

	o.recordTargetActivity(item.targetID, res.NewInteresting, res.Crashes)

	_, storeErr := o.store.InsertRun(ctx, store.Run{
		Diagnostic:     res.Output,
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
		o.logger.Error("insert run failed",
			"component", "orchestrator",
			"worker", workerID,
			"project", job.Project,
			"target", job.TargetName,
			"err", storeErr,
		)
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
			o.logger.Error("record crash failed",
				"component", "orchestrator",
				"worker", workerID,
				"project", job.Project,
				"target", job.TargetName,
				"err", crashErr,
			)
		} else {
			o.logger.Warn("crash recorded",
				"component", "orchestrator",
				"worker", workerID,
				"project", job.Project,
				"target", job.TargetName,
				"hash", crash.InputHash,
				"fp", crash.StackFingerprint,
			)
		}
	}

	o.logger.Info("completed target run",
		"component", "orchestrator",
		"worker", workerID,
		"project", job.Project,
		"target", job.TargetName,
		"sha", item.checkout.GitSHA,
		"status", res.Status,
		"execs", res.Execs,
		"rate", fmt.Sprintf("%.0f/s", res.ExecsPerSec),
		"new", res.NewInteresting,
		"corpus", res.CorpusCount,
		"cov", fmt.Sprintf("%.1f%%", res.CoveragePct),
		"crashes", res.Crashes,
	)
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

func isLocalPathRepo(repo string) bool {
	info, err := os.Stat(repo)
	return err == nil && info.IsDir()
}
