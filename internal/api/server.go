package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NimbleTerminal/ntfuzzy/internal/store"
)

// ActiveJob describes an in-flight fuzzing worker job managed by the orchestrator.
type ActiveJob struct {
	WorkerID    string    `json:"worker_id"`
	Project     string    `json:"project"`
	Package     string    `json:"package"`
	Target      string    `json:"target"`
	FuzzTime    string    `json:"fuzztime"`
	StartedAt   time.Time `json:"started_at"`
	Status      string    `json:"status"`
}

// WorkerStatusProvider reports current orchestrator worker activity.
type WorkerStatusProvider interface {
	ActiveJobs() []ActiveJob
	WorkerCount() int
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
	ActiveJobs    []ActiveJob `json:"active_jobs"`
	UpdatedAt     time.Time   `json:"updated_at"`
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
	s.mux.HandleFunc("GET /api/coverage/history", s.handleCoverageHistory)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	targets, err := s.store.ListTargets(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	limit := 100
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	runs, err := s.store.ListRuns(r.Context(), target, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleCrashes(w http.ResponseWriter, r *http.Request) {
	openParam := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("open")))
	openOnly := openParam == "" || openParam == "true" || openParam == "1"
	if openParam == "false" || openParam == "0" || openParam == "all" {
		openOnly = false
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	crashes, err := s.store.ListCrashes(r.Context(), openOnly, target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, crashes)
}

func (s *Server) handleCoverageHistory(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		writeJSON(w, http.StatusOK, []store.CoveragePoint{})
		return
	}
	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	history, err := s.store.GetCoverageHistory(r.Context(), target, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
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
		activeJobs = s.workers.ActiveJobs()
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
	if errors.IsContextCanceled(err) {
		return
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

type errorsHelper struct{}

var errors errorsHelper

func (errorsHelper) IsContextCanceled(err error) bool {
	return err != nil && err == context.Canceled
}
