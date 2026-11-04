package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NimbleTerminal/ntfuzzy/internal/orchestrator"
	"github.com/NimbleTerminal/ntfuzzy/internal/store"
)

// WorkerStatusProvider reports current orchestrator worker activity.
type WorkerStatusProvider interface {
	ActiveJobs() []orchestrator.ActiveJob
	WorkerCount() int
}

// Server exposes the JSON HTTP API under /api/*.
type Server struct {
	store   *store.Store
	workers WorkerStatusProvider
	mux     *http.ServeMux
}

// NewServer creates a new API server backed by the given DuckDB store and optional worker status provider.
func NewServer(st *store.Store, workers WorkerStatusProvider) *Server {
	s := &Server{
		store:   st,
		workers: workers,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/projects", s.handleProjects)
	s.mux.HandleFunc("GET /api/targets", s.handleTargets)
	s.mux.HandleFunc("GET /api/runs", s.handleRuns)
	s.mux.HandleFunc("GET /api/crashes", s.handleCrashes)
	s.mux.HandleFunc("POST /api/crashes/{id}/fix", s.handleFixCrash)
	s.mux.HandleFunc("GET /api/coverage/history", s.handleCoverageHistory)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]Project, 0, len(rows))
	for _, p := range rows {
		out = append(out, Project{
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
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	rows, err := s.store.ListTargets(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]Target, 0, len(rows))
	for _, t := range rows {
		out = append(out, Target{
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
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	limit := 100
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	rows, err := s.store.ListRuns(r.Context(), target, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]Run, 0, len(rows))
	for _, run := range rows {
		out = append(out, Run{
			ID:             run.ID,
			TargetID:       run.TargetID,
			ProjectName:    run.ProjectName,
			Package:        run.Package,
			TargetName:     run.TargetName,
			Started:        run.Started,
			DurationSec:    run.DurationSec,
			Execs:          run.Execs,
			ExecsPerSec:    run.ExecsPerSec,
			NewInteresting: run.NewInteresting,
			CorpusCount:    run.CorpusCount,
			Crashes:        run.Crashes,
			CoveragePct:    run.CoveragePct,
			Status:         run.Status,
			Worker:         run.Worker,
			GitSHA:         run.GitSHA,
			Dirty:          run.Dirty,
			GoVersion:      run.GoVersion,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCrashes(w http.ResponseWriter, r *http.Request) {
	openParam := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("open")))
	openOnly := openParam == "" || openParam == "true" || openParam == "1"
	if openParam == "false" || openParam == "0" || openParam == "all" {
		openOnly = false
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	rows, err := s.store.ListCrashes(r.Context(), openOnly, target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]Crash, 0, len(rows))
	for _, c := range rows {
		out = append(out, Crash{
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
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleFixCrash(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimSpace(r.PathValue("id"))
	crashID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || crashID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid crash id"))
		return
	}

	var req FixCrashRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	}

	if err := s.store.MarkCrashFixed(r.Context(), crashID, time.Now().UTC(), strings.TrimSpace(req.IssueURL)); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "fixed", "id": crashID})
}

func (s *Server) handleCoverageHistory(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		writeJSON(w, http.StatusOK, []CoveragePoint{})
		return
	}
	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	rows, err := s.store.GetCoverageHistory(r.Context(), target, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]CoveragePoint, 0, len(rows))
	for _, pt := range rows {
		out = append(out, CoveragePoint{
			RunID:          pt.RunID,
			TargetID:       pt.TargetID,
			Started:        pt.Started,
			CoveragePct:    pt.CoveragePct,
			ExecsPerSec:    pt.ExecsPerSec,
			NewInteresting: pt.NewInteresting,
			CorpusCount:    pt.CorpusCount,
			GitSHA:         pt.GitSHA,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetFleetStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var activeJobs []ActiveJob
	maxWorkers := 0
	if s.workers != nil {
		for _, j := range s.workers.ActiveJobs() {
			activeJobs = append(activeJobs, ActiveJob{
				WorkerID:  j.WorkerID,
				Project:   j.Project,
				Package:   j.Package,
				Target:    j.Target,
				FuzzTime:  j.FuzzTime,
				GitSHA:    j.GitSHA,
				Dirty:     j.Dirty,
				StartedAt: j.StartedAt,
				Status:    j.Status,
			})
		}
		maxWorkers = s.workers.WorkerCount()
	}
	if activeJobs == nil {
		activeJobs = []ActiveJob{}
	}

	health := "healthy"
	switch {
	case stats.OpenCrashes > 0:
		health = "crashes_detected"
	case stats.TotalTargets == 0 && len(activeJobs) == 0:
		health = "idle"
	case len(activeJobs) > 0:
		health = "fuzzing"
	}

	resp := StatusResponse{
		FleetHealth:   health,
		OpenCrashes:   stats.OpenCrashes,
		RunsLast24h:   stats.RunsLast24h,
		ExecsLast24h:  stats.ExecsLast24h,
		AvgExecsSec:   stats.AvgExecsSec,
		TotalProjects: stats.TotalProjects,
		TotalTargets:  stats.TotalTargets,
		ActiveWorkers: len(activeJobs),
		MaxWorkers:    maxWorkers,
		SchemaVersion: stats.SchemaVersion,
		ActiveJobs:    activeJobs,
		UpdatedAt:     time.Now().UTC(),
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	_ = enc.Encode(payload)
}

func writeError(w http.ResponseWriter, code int, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
