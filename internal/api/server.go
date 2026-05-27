// internal/api/server.go
package api

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/config"
	apiStatic "github.com/felipendelicia/nat-backup/internal/api/static"
	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jmoiron/sqlx"
)

type Server struct {
	router    chi.Router
	db        *sqlx.DB
	cfg       config.Config
	hub       *ws.Hub
	wsHandler *ws.AgentHandler
}

// NewServer creates the HTTP handler and returns the hub and agent handler for use by the caller.
func NewServer(db *sqlx.DB, cfg config.Config) (http.Handler, *ws.Hub, *ws.AgentHandler) {
	hub := ws.NewHub(db)
	go hub.Run()

	agentHandler := ws.NewAgentHandler(hub)
	s := &Server{
		db:        db,
		cfg:       cfg,
		hub:       hub,
		wsHandler: agentHandler,
	}
	s.router = s.buildRouter()
	return s, hub, agentHandler
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// NewServerWithStatic creates a server that also serves the embedded React UI.
func NewServerWithStatic(db *sqlx.DB, cfg config.Config) (http.Handler, *ws.Hub, *ws.AgentHandler) {
	hub := ws.NewHub(db)
	go hub.Run()
	agentHandler := ws.NewAgentHandler(hub)
	s := &Server{
		db:        db,
		cfg:       cfg,
		hub:       hub,
		wsHandler: agentHandler,
	}
	s.router = s.buildRouterWithStatic()
	return s, hub, agentHandler
}

func (s *Server) buildRouterWithStatic() chi.Router {
	r := s.buildRouter()

	distFS, err := fs.Sub(apiStatic.FS, "dist")
	if err != nil {
		return r // no embedded UI
	}

	fileServer := http.FileServer(http.FS(distFS))

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		// Try serving the asset; fall back to index.html for SPA routing
		p := r.URL.Path
		if len(p) > 0 && p[0] == '/' {
			p = p[1:]
		}
		if p == "" {
			r2 := *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, &r2)
			return
		}
		if _, err := distFS.Open(p); err != nil {
			r2 := *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, &r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	return r
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// WebSocket for agents (auth via hello message)
	r.Get("/ws/agent", s.wsHandler.ServeHTTP)

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
