package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NimbleTerminal/ntfuzz/internal/orchestrator"
	"github.com/NimbleTerminal/ntfuzz/internal/store"
)

// WorkerStatusProvider reports current orchestrator worker activity.
type WorkerStatusProvider interface {
	ActiveJobs() []orchestrator.ActiveJob
	WorkerCount() int
}

// Server exposes the JSON HTTP API under /api/*.
type Server struct {
	store    *store.Store
	workers  WorkerStatusProvider
	mux      *http.ServeMux
	snapshot *store.Snapshot
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

// NewSnapshotServer exposes only read operations; no writer or orchestrator is attached.
func NewSnapshotServer(st *store.Store, snapshot store.Snapshot) *Server {
	s := &Server{store: st, snapshot: &snapshot, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/projects", s.handleProjects)
	s.mux.HandleFunc("GET /api/targets", s.handleTargets)
	s.mux.HandleFunc("GET /api/runs", s.handleRuns)
	s.mux.HandleFunc("GET /api/crashes", s.handleCrashes)
	if s.snapshot == nil {
		s.mux.HandleFunc("POST /api/crashes/{id}/fix", s.handleFixCrash)
	}
	s.mux.HandleFunc("GET /api/coverage/history", s.handleCoverageHistory)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		out = append(out, ProjectFromStore(p))
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
		out = append(out, TargetFromStore(t))
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
		out = append(out, RunFromStore(run))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCrashes(w http.ResponseWriter, r *http.Request) {
	openOnly, err := parseOpenParam(r.URL.Query().Get("open"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	rows, err := s.store.ListCrashes(r.Context(), openOnly, target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]Crash, 0, len(rows))
	for _, c := range rows {
		out = append(out, CrashFromStore(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func parseOpenParam(raw string) (bool, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "true", "1":
		return true, nil
	case "false", "0", "all":
		return false, nil
	default:
		return false, fmt.Errorf("invalid open parameter %q: expected true, false, or all", raw)
	}
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
		dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON request body: %w", err))
			return
		}
	}

	if err := s.store.MarkCrashFixed(r.Context(), crashID, time.Now().UTC(), strings.TrimSpace(req.IssueURL)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
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
		out = append(out, CoveragePointFromStore(pt))
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
			activeJobs = append(activeJobs, ActiveJobFromOrchestrator(j))
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
	if s.snapshot != nil {
		resp.ReadOnly = true
		resp.SnapshotAt = &s.snapshot.PublishedAt
		resp.UpdatedAt = s.snapshot.PublishedAt
		resp.FleetHealth = "snapshot"
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
