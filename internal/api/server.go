// internal/api/server.go
package api

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	apiStatic "github.com/felipendelicia/nat-backup/internal/api/static"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jmoiron/sqlx"
)

type Server struct {
	router     chi.Router
	db         *sqlx.DB
	cfg        config.Config
	hub        *ws.Hub
	wsHandler  *ws.AgentHandler
	consoleHub *ConsoleHub
	sched      interface {
		AddJob(job models.BackupJob) error
		RemoveJob(jobID string)
	}
}

// ConsoleHub returns the server's log broadcast hub so callers can wire log.SetOutput.
func (s *Server) GetConsoleHub() *ConsoleHub { return s.consoleHub }

func (s *Server) SetScheduler(sched interface {
	AddJob(job models.BackupJob) error
	RemoveJob(jobID string)
}) {
	s.sched = sched
}

// NewServer creates the HTTP handler and returns the hub and agent handler for use by the caller.
func NewServer(db *sqlx.DB, cfg config.Config) (*Server, *ws.Hub, *ws.AgentHandler) {
	hub := ws.NewHub(db)
	go hub.Run()

	agentHandler := ws.NewAgentHandler(hub)
	s := &Server{
		db:         db,
		cfg:        cfg,
		hub:        hub,
		wsHandler:  agentHandler,
		consoleHub: NewConsoleHub(),
	}
	s.router = s.buildRouter()
	return s, hub, agentHandler
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// NewServerWithStatic creates a server that also serves the embedded React UI.
func NewServerWithStatic(db *sqlx.DB, cfg config.Config) (*Server, *ws.Hub, *ws.AgentHandler) {
	hub := ws.NewHub(db)
	go hub.Run()
	agentHandler := ws.NewAgentHandler(hub)
	s := &Server{
		db:         db,
		cfg:        cfg,
		hub:        hub,
		wsHandler:  agentHandler,
		consoleHub: NewConsoleHub(),
	}
	agentHandler.SetConsoleWriter(s.consoleHub)
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

// redactQueryToken strips the value of a ?token= parameter from a URI so the JWT
// used to authenticate the console and run-log WebSockets never reaches the logs.
func redactQueryToken(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	q := u.Query()
	if !q.Has("token") {
		return uri
	}
	q.Set("token", "REDACTED")
	u.RawQuery = q.Encode()
	return u.String()
}

// requestLogger logs each request through the standard library logger. chi's
// middleware.Logger builds its own log.New(os.Stdout, ...), which log.SetOutput
// cannot redirect — so request lines never reached the console hub and the page
// showed no traffic at all. Routing through log.Default() fixes that, and the URI
// is redacted so query-string tokens are not persisted.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		defer func() {
			log.Printf("%s %s %s %d %dB %s",
				r.Method, redactQueryToken(r.RequestURI), r.Proto,
				ww.Status(), ww.BytesWritten(), time.Since(start))
		}()
		next.ServeHTTP(ww, r)
	})
}

// maxJSONBody caps request bodies on the JSON API. Without it any endpoint accepts
// an unbounded body and a single request can exhaust server memory.
const maxJSONBody = 1 << 20 // 1 MiB

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The upload endpoint streams multi-gigabyte archives and sets its own limit.
		if r.Body != nil && !strings.HasPrefix(r.URL.Path, "/api/upload/") {
			r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(limitBody)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// WebSocket for agents (auth via hello message)
	r.Get("/ws/agent", s.wsHandler.ServeHTTP)

	// WebSocket for server console logs (auth via ?token= query param)
	consoleLimiter := newRateLimiter(30, time.Minute)
	r.With(consoleLimiter.middleware).Get("/ws/console", s.handleConsoleWS)

	// Login and the console socket are the two unauthenticated entry points that
	// take a credential, so both are throttled per IP.
	loginLimiter := newRateLimiter(10, time.Minute)
	r.With(loginLimiter.middleware).Post("/api/auth/login", s.handleLogin)

	// Agent-authenticated routes (API key in Authorization: Bearer header)
	r.Group(func(r chi.Router) {
		r.Use(s.agentAuthMiddleware)
		r.Post("/api/upload/{run_id}", s.handleUpload)
	})

	// JWT-authenticated routes (UI)
	r.Group(func(r chi.Router) {
		r.Use(s.jwtAuthMiddleware)

		r.Get("/api/agents", s.handleListAgents)
		r.Post("/api/agents", s.handleCreateAgent)
		r.Delete("/api/agents/{id}", s.handleDeleteAgent)
		r.Post("/api/agents/{id}/update", s.handleUpdateAgent)
		r.Get("/api/agents/{id}/browse", s.handleBrowseAgent)

		r.Get("/api/storage-destinations", s.handleListStorage)
		r.Post("/api/storage-destinations", s.handleCreateStorage)
		r.Put("/api/storage-destinations/{id}", s.handleUpdateStorage)
		r.Delete("/api/storage-destinations/{id}", s.handleDeleteStorage)

		r.Get("/api/jobs", s.handleListJobs)
		r.Post("/api/jobs", s.handleCreateJob)
		r.Put("/api/jobs/{id}", s.handleUpdateJob)
		r.Patch("/api/jobs/{id}/toggle", s.handleToggleJob)
		r.Delete("/api/jobs/{id}", s.handleDeleteJob)
		r.Post("/api/jobs/{id}/run", s.handleTriggerJob)

		r.Get("/api/runs", s.handleListRuns)
		r.Get("/api/runs/stats", s.handleRunStats)
		r.Post("/api/runs/{id}/cancel", s.handleCancelRun)
		r.Get("/api/runs/{id}/logs", s.handleRunLogs)
		r.Get("/api/runs/{id}/logs/ws", s.handleRunLogsWS)

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
