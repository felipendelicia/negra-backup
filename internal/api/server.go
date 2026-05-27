// internal/api/server.go
package api

import (
	"encoding/json"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jmoiron/sqlx"
)

// jobDispatcher is implemented by ws.Hub. Defined here to avoid circular import.
type jobDispatcher interface {
	DispatchJob(agentID, runID string, job models.BackupJob)
}

type Server struct {
	router    chi.Router
	db        *sqlx.DB
	cfg       config.Config
	hub       jobDispatcher
	wsHandler http.Handler
}

// NewServer creates the HTTP server. hub and wsHandler are wired in Plan 3.
// For now they can be nil — the WS endpoint will be added in Plan 3.
func NewServer(db *sqlx.DB, cfg config.Config) http.Handler {
	s := &Server{db: db, cfg: cfg}
	s.router = s.buildRouter()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Post("/api/auth/login", s.handleLogin)

	// Agent-authenticated routes (API key in Authorization: Bearer header)
	r.Group(func(r chi.Router) {
		r.Use(s.agentAuthMiddleware)
		r.Post("/api/upload/{run_id}", s.handleUpload)
	})

	// JWT-authenticated routes (UI)
	r.Group(func(r chi.Router) {
		r.Use(s.jwtAuthMiddleware)

		r.Get("/api/agents", s.handleListAgents)
		r.Delete("/api/agents/{id}", s.handleDeleteAgent)

		r.Get("/api/storage-destinations", s.handleListStorage)
		r.Post("/api/storage-destinations", s.handleCreateStorage)
		r.Put("/api/storage-destinations/{id}", s.handleUpdateStorage)
		r.Delete("/api/storage-destinations/{id}", s.handleDeleteStorage)

		r.Get("/api/jobs", s.handleListJobs)
		r.Post("/api/jobs", s.handleCreateJob)
		r.Put("/api/jobs/{id}", s.handleUpdateJob)
		r.Delete("/api/jobs/{id}", s.handleDeleteJob)
		r.Post("/api/jobs/{id}/run", s.handleTriggerJob)

		r.Get("/api/runs", s.handleListRuns)
		r.Get("/api/runs/{id}/logs", s.handleRunLogs)

		r.Get("/api/settings/notifications", s.handleGetNotificationSettings)
		r.Put("/api/settings/notifications", s.handleUpdateNotificationSettings)
	})

	return r
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func respondError(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
