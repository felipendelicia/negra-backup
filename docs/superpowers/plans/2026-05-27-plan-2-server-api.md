# Server API & Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the full REST API server: HTTP server entrypoint, JWT auth for UI, API-key auth for agents, and CRUD handlers for all resources.

**Architecture:** Chi router. JWT for dashboard login. API key (hashed with bcrypt) for agent auth. All secrets (storage configs, DB conn strings, passphrases) encrypted at rest with AES-256-GCM using `ENCRYPTION_KEY`. Plan 1 (Foundation) must be complete first.

**Tech Stack:** `go-chi/chi/v5`, `golang-jwt/jwt/v5`, `golang.org/x/crypto/bcrypt`, `google/uuid`, `sqlx`

---

## File Map

- `internal/api/server.go` — HTTP server constructor, router wiring
- `internal/api/auth.go` — POST /api/auth/login handler + JWT issuing
- `internal/api/middleware.go` — JWT middleware + agent API key middleware
- `internal/api/agents.go` — GET/DELETE /api/agents
- `internal/api/jobs.go` — CRUD /api/jobs + POST /api/jobs/:id/run
- `internal/api/runs.go` — GET /api/runs + GET /api/runs/:id/logs
- `internal/api/upload.go` — POST /api/upload/:run_id (chunked multipart)
- `internal/api/storage.go` — CRUD /api/storage-destinations
- `internal/api/settings.go` — GET/PUT /api/settings/notifications
- `internal/crypto/crypto.go` — AES-256-GCM encrypt/decrypt helpers
- `internal/crypto/crypto_test.go`
- `cmd/server/main.go` — full server entrypoint (replaces stub)

---

### Task 1: AES-256-GCM Crypto Helpers

Secrets stored in DB (storage configs, DB conn strings, passphrases) are encrypted. This package handles that.

**Files:**
- Create: `internal/crypto/crypto.go`
- Create: `internal/crypto/crypto_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/crypto/crypto_test.go
package crypto_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	plaintext := "super-secret-connection-string"

	ciphertext, err := crypto.Encrypt(key, plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)

	decrypted, err := crypto.Decrypt(key, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncrypt_DifferentEachTime(t *testing.T) {
	key := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	c1, err := crypto.Encrypt(key, "hello")
	require.NoError(t, err)
	c2, err := crypto.Encrypt(key, "hello")
	require.NoError(t, err)
	assert.NotEqual(t, c1, c2) // random nonce each time
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	key2 := "aabbccddeeff00112233445566778899aabbccddeeff001122334455667788aa"

	ciphertext, err := crypto.Encrypt(key1, "secret")
	require.NoError(t, err)

	_, err = crypto.Decrypt(key2, ciphertext)
	require.Error(t, err)
}

func TestDecrypt_InvalidBase64(t *testing.T) {
	key := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	_, err := crypto.Decrypt(key, "not-base64!!!")
	require.Error(t, err)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/crypto/... -v
```

Expected: FAIL "no Go files"

- [ ] **Step 3: Implement crypto.go**

```go
// internal/crypto/crypto.go
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Encrypt encrypts plaintext using AES-256-GCM.
// key must be a 64-char hex string (32 bytes).
// Returns base64(nonce + ciphertext).
func Encrypt(hexKey, plaintext string) (string, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("key must be 64-char hex (32 bytes)")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("rand nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value produced by Encrypt.
func Decrypt(hexKey, encoded string) (string, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("key must be 64-char hex (32 bytes)")
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/crypto/... -v
```

Expected: PASS all 4 tests

- [ ] **Step 5: Commit**

```bash
git add internal/crypto/
git commit -m "feat: add AES-256-GCM encrypt/decrypt helpers"
```

---

### Task 2: HTTP Server Entrypoint

**Files:**
- Modify: `cmd/server/main.go`
- Create: `internal/api/server.go`

- [ ] **Step 1: Write failing test for server constructor**

```go
// internal/api/server_test.go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_HealthEndpoint(t *testing.T) {
	cfg := config.Config{
		JWTSecret:     "test-secret",
		EncryptionKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	srv := api.NewServer(nil, cfg) // nil DB for unit test

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/api/... -v -short
```

Expected: FAIL "no Go files"

- [ ] **Step 3: Implement internal/api/server.go**

```go
// internal/api/server.go
package api

import (
	"encoding/json"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jmoiron/sqlx"
)

type Server struct {
	router chi.Router
	db     *sqlx.DB
	cfg    config.Config
}

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

	// Health check
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Auth
	r.Post("/api/auth/login", s.handleLogin)

	// Agent-authenticated routes (API key)
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

// respond writes JSON with the given status code.
func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// respondError writes a JSON error body.
func respondError(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
```

- [ ] **Step 4: Run test**

```bash
go test ./internal/api/... -run TestServer_HealthEndpoint -v
```

Expected: PASS (handler stubs will panic — add stub handlers first)

Note: Before the test passes, you need the stub handler functions referenced in `buildRouter()`. Create them as empty stubs:

```go
// internal/api/stubs.go  (temporary, will be replaced by real handlers)
package api

import "net/http"

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request)                      {}
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request)                     {}
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request)                 {}
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request)                {}
func (s *Server) handleListStorage(w http.ResponseWriter, r *http.Request)                {}
func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request)              {}
func (s *Server) handleUpdateStorage(w http.ResponseWriter, r *http.Request)              {}
func (s *Server) handleDeleteStorage(w http.ResponseWriter, r *http.Request)              {}
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request)                   {}
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request)                  {}
func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request)                  {}
func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request)                  {}
func (s *Server) handleTriggerJob(w http.ResponseWriter, r *http.Request)                 {}
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request)                   {}
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request)                    {}
func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request)    {}
func (s *Server) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {}
func (s *Server) jwtAuthMiddleware(next http.Handler) http.Handler                        { return next }
func (s *Server) agentAuthMiddleware(next http.Handler) http.Handler                      { return next }
```

- [ ] **Step 5: Run test again**

```bash
go test ./internal/api/... -run TestServer_HealthEndpoint -v
```

Expected: PASS

- [ ] **Step 6: Update cmd/server/main.go with full entrypoint**

```go
// cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/felipendelicia/nat-backup/internal/db"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := db.RunMigrations(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	pool, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	// Create default admin user if not exists
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("bcrypt: %v", err)
	}
	_, _ = pool.Exec(
		`INSERT INTO admin_users (username, password_hash) VALUES ('admin', $1) ON CONFLICT DO NOTHING`,
		string(hash),
	)

	handler := api.NewServer(pool, cfg)

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // uploads can be slow
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		fmt.Printf("nat-backup-server listening on %s\n", addr)
		if cfg.TLSEnabled {
			if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != http.ErrServerClosed {
				log.Fatalf("tls server: %v", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				log.Fatalf("server: %v", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	fmt.Println("server stopped")
}
```

- [ ] **Step 7: Build server**

```bash
go build ./cmd/server
```

Expected: no errors

- [ ] **Step 8: Commit**

```bash
git add cmd/server/main.go internal/api/
git commit -m "feat: add HTTP server entrypoint and router skeleton"
```

---

### Task 3: Auth — JWT Login + Middleware

**Files:**
- Create: `internal/api/auth.go` (replaces stub handleLogin)
- Create: `internal/api/middleware.go` (replaces stub middlewares)
- Create: `internal/api/auth_test.go`
- Delete: `internal/api/stubs.go` stub for handleLogin, jwtAuthMiddleware, agentAuthMiddleware

- [ ] **Step 1: Write failing auth tests**

```go
// internal/api/auth_test.go
package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestServer(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Config{
		JWTSecret:     "test-secret-32-chars-min-xxxxxxxx",
		EncryptionKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	return api.NewTestServer(nil, cfg)
}

func TestLogin_Success(t *testing.T) {
	srv := makeTestServer(t)

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotEmpty(t, resp["token"])
}

func TestLogin_WrongPassword(t *testing.T) {
	srv := makeTestServer(t)

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTMiddleware_NoToken(t *testing.T) {
	srv := makeTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTMiddleware_ValidToken(t *testing.T) {
	srv := makeTestServer(t)

	// Login first
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var loginResp map[string]string
	json.NewDecoder(w.Body).Decode(&loginResp)
	token := loginResp["token"]

	// Use token
	req2 := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	// 200 or 500 (no DB) but NOT 401
	assert.NotEqual(t, http.StatusUnauthorized, w2.Code)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/api/... -run TestLogin -v
```

Expected: FAIL (NewTestServer not found)

- [ ] **Step 3: Implement auth.go**

```go
// internal/api/auth.go
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin authenticates admin user and returns a JWT.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Lookup user
	var hash string
	err := s.db.QueryRow(
		`SELECT password_hash FROM admin_users WHERE username = $1`, req.Username,
	).Scan(&hash)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		respondError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": req.Username,
		"exp": time.Now().Add(24 * time.Hour).Unix(),
		"iat": time.Now().Unix(),
	})

	signed, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "token signing failed")
		return
	}

	respond(w, http.StatusOK, map[string]string{"token": signed})
}
```

- [ ] **Step 4: Implement middleware.go**

```go
// internal/api/middleware.go
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const contextKeyUsername contextKey = "username"

// jwtAuthMiddleware validates Bearer JWT from Authorization header.
func (s *Server) jwtAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			respondError(w, http.StatusUnauthorized, "missing token")
			return
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(s.cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			respondError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			respondError(w, http.StatusUnauthorized, "invalid claims")
			return
		}

		ctx := context.WithValue(r.Context(), contextKeyUsername, claims["sub"])
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// agentAuthMiddleware validates agent API key from Authorization: Bearer header.
// Compares against bcrypt hashes stored in the agents table.
func (s *Server) agentAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			respondError(w, http.StatusUnauthorized, "missing api key")
			return
		}
		apiKey := strings.TrimPrefix(auth, "Bearer ")

		// Fetch all agents and check bcrypt (agents count is low, so this is fine)
		rows, err := s.db.Query(`SELECT id, api_key FROM agents`)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		defer rows.Close()

		var agentID string
		found := false
		for rows.Next() {
			var id, hash string
			if err := rows.Scan(&id, &hash); err != nil {
				continue
			}
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte(apiKey)) == nil {
				agentID = id
				found = true
				break
			}
		}

		if !found {
			respondError(w, http.StatusUnauthorized, "invalid api key")
			return
		}

		ctx := context.WithValue(r.Context(), contextKey("agent_id"), agentID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

- [ ] **Step 5: Add NewTestServer for unit tests (test-only helper)**

Add to `internal/api/server.go` (or a new `internal/api/testing.go`):

```go
// internal/api/testing.go
package api

import (
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

// NewTestServer creates a server with a pre-seeded admin user in a mock DB.
// For unit tests only — uses an in-memory mock that satisfies login queries.
func NewTestServer(db *sqlx.DB, cfg config.Config) http.Handler {
	s := &Server{db: db, cfg: cfg}
	// Override login handler to use hard-coded test credentials when DB is nil
	s.router = s.buildTestRouter()
	return s
}

func (s *Server) buildTestRouter() chi.Router {
	// Same as buildRouter but login uses test credentials
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	r.Post("/api/auth/login", s.handleLoginTest)

	r.Group(func(r chi.Router) {
		r.Use(s.jwtAuthMiddleware)
		r.Get("/api/agents", func(w http.ResponseWriter, r *http.Request) {
			if s.db == nil {
				respond(w, http.StatusServiceUnavailable, nil)
				return
			}
			s.handleListAgents(w, r)
		})
		// Add remaining routes as needed for tests
	})

	return r
}

func (s *Server) handleLoginTest(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	// Test credentials
	hash, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.MinCost)
	if req.Username != "admin" || bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) != nil {
		respondError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": req.Username,
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	})
	signed, _ := token.SignedString([]byte(s.cfg.JWTSecret))
	respond(w, http.StatusOK, map[string]string{"token": signed})
}
```

Add missing imports to `testing.go`:
```go
import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)
```

- [ ] **Step 6: Run auth tests**

```bash
go test ./internal/api/... -run "TestLogin|TestJWT" -v
```

Expected: PASS all 4 tests

- [ ] **Step 7: Commit**

```bash
git add internal/api/auth.go internal/api/middleware.go internal/api/testing.go
git commit -m "feat: add JWT auth and agent API key middleware"
```

---

### Task 4: Agents Handlers

**Files:**
- Create: `internal/api/agents.go` (replace stub)
- Create: `internal/api/agents_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/api/agents_test.go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
)

// Integration tests: require postgres. Unit tests use nil DB → 503.
func TestListAgents_NoAuth(t *testing.T) {
	cfg := config.Config{
		JWTSecret:     "test-secret-32-chars-min-xxxxxxxx",
		EncryptionKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	srv := api.NewServer(nil, cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/api/... -run TestListAgents -v
```

Expected: FAIL "handleListAgents not defined"

- [ ] **Step 3: Implement agents.go**

```go
// internal/api/agents.go
package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	var agents []models.Agent
	if err := s.db.Select(&agents, `SELECT id, name, os, arch, version, last_seen, status, created_at FROM agents ORDER BY created_at DESC`); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, agents)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	result, err := s.db.Exec(`DELETE FROM agents WHERE id = $1`, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "agent not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generateAPIKey creates a random 32-byte hex API key and its bcrypt hash.
func generateAPIKey() (plaintext, hash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	plaintext = hex.EncodeToString(raw)
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return
	}
	hash = string(h)
	return
}
```

- [ ] **Step 4: Remove stub for handleListAgents and handleDeleteAgent from stubs.go**

Edit `internal/api/stubs.go` — remove `handleListAgents` and `handleDeleteAgent` lines.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/api/... -run TestListAgents -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/agents.go internal/api/agents_test.go internal/api/stubs.go
git commit -m "feat: add agents list and delete handlers"
```

---

### Task 5: Storage Destinations Handlers

**Files:**
- Create: `internal/api/storage.go` (replace stubs)
- Create: `internal/api/storage_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/api/storage_test.go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestListStorage_NoAuth(t *testing.T) {
	cfg := config.Config{
		JWTSecret:     "test-secret-32-chars-min-xxxxxxxx",
		EncryptionKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	srv := api.NewServer(nil, cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/storage-destinations", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/api/... -run TestListStorage -v
```

Expected: FAIL

- [ ] **Step 3: Implement storage.go**

Storage configs contain secrets (S3 keys, SFTP passwords). Encrypt before storing, decrypt on read.

```go
// internal/api/storage.go
package api

import (
	"encoding/json"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type createStorageRequest struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (s *Server) handleListStorage(w http.ResponseWriter, r *http.Request) {
	var dests []models.StorageDestination
	if err := s.db.Select(&dests, `SELECT id, name, type, created_at FROM storage_destinations ORDER BY created_at DESC`); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Config is intentionally omitted from list (contains secrets)
	respond(w, http.StatusOK, dests)
}

func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	var req createStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" || req.Type == "" {
		respondError(w, http.StatusBadRequest, "name and type required")
		return
	}

	encConfig, err := crypto.Encrypt(s.cfg.EncryptionKey, string(req.Config))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	var dest models.StorageDestination
	err = s.db.QueryRowx(
		`INSERT INTO storage_destinations (name, type, config) VALUES ($1, $2, $3) RETURNING id, name, type, created_at`,
		req.Name, req.Type, encConfig,
	).StructScan(&dest)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respond(w, http.StatusCreated, dest)
}

func (s *Server) handleUpdateStorage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req createStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	encConfig, err := crypto.Encrypt(s.cfg.EncryptionKey, string(req.Config))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	result, err := s.db.Exec(
		`UPDATE storage_destinations SET name=$1, type=$2, config=$3 WHERE id=$4`,
		req.Name, req.Type, encConfig, id,
	)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	result, err := s.db.Exec(`DELETE FROM storage_destinations WHERE id=$1`, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Remove stubs for storage handlers from stubs.go**

- [ ] **Step 5: Run test**

```bash
go test ./internal/api/... -run TestListStorage -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/storage.go internal/api/storage_test.go internal/api/stubs.go
git commit -m "feat: add storage destinations CRUD handlers"
```

---

### Task 6: Jobs Handlers

**Files:**
- Create: `internal/api/jobs.go`
- Create: `internal/api/jobs_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/api/jobs_test.go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestListJobs_NoAuth(t *testing.T) {
	cfg := config.Config{
		JWTSecret:     "test-secret-32-chars-min-xxxxxxxx",
		EncryptionKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	srv := api.NewServer(nil, cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/api/... -run TestListJobs -v
```

Expected: FAIL

- [ ] **Step 3: Implement jobs.go**

```go
// internal/api/jobs.go
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type createJobRequest struct {
	AgentID              uuid.UUID       `json:"agent_id"`
	Name                 string          `json:"name"`
	Enabled              bool            `json:"enabled"`
	Type                 string          `json:"type"`
	Source               json.RawMessage `json:"source"`
	StorageDestinationID uuid.UUID       `json:"storage_destination_id"`
	ScheduleCron         string          `json:"schedule_cron"`
	RetentionDays        int             `json:"retention_days"`
	Compression          string          `json:"compression"`
	Encrypt              bool            `json:"encrypt"`
	EncryptPassphrase    string          `json:"encrypt_passphrase,omitempty"`
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	var jobs []models.BackupJob
	if err := s.db.Select(&jobs, `
		SELECT id, agent_id, name, enabled, type, source, storage_destination_id,
		       schedule_cron, retention_days, compression, encrypt, created_at, updated_at
		FROM backup_jobs ORDER BY created_at DESC`); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, jobs)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" || req.Type == "" || req.ScheduleCron == "" {
		respondError(w, http.StatusBadRequest, "name, type, schedule_cron required")
		return
	}
	if req.Compression == "" {
		req.Compression = models.CompressionZstd
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = 30
	}

	// Encrypt passphrase if set
	var encPassphrase *string
	if req.Encrypt && req.EncryptPassphrase != "" {
		enc, err := crypto.Encrypt(s.cfg.EncryptionKey, req.EncryptPassphrase)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encPassphrase = &enc
	}

	var job models.BackupJob
	err := s.db.QueryRowx(`
		INSERT INTO backup_jobs
		  (agent_id, name, enabled, type, source, storage_destination_id, schedule_cron, retention_days, compression, encrypt, encrypt_passphrase)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, agent_id, name, enabled, type, source, storage_destination_id,
		          schedule_cron, retention_days, compression, encrypt, created_at, updated_at`,
		req.AgentID, req.Name, req.Enabled, req.Type, req.Source,
		req.StorageDestinationID, req.ScheduleCron, req.RetentionDays,
		req.Compression, req.Encrypt, encPassphrase,
	).StructScan(&job)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respond(w, http.StatusCreated, job)
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	var encPassphrase *string
	if req.Encrypt && req.EncryptPassphrase != "" {
		enc, err := crypto.Encrypt(s.cfg.EncryptionKey, req.EncryptPassphrase)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encPassphrase = &enc
	}

	result, err := s.db.Exec(`
		UPDATE backup_jobs SET
		  agent_id=$1, name=$2, enabled=$3, type=$4, source=$5,
		  storage_destination_id=$6, schedule_cron=$7, retention_days=$8,
		  compression=$9, encrypt=$10, encrypt_passphrase=$11, updated_at=$12
		WHERE id=$13`,
		req.AgentID, req.Name, req.Enabled, req.Type, req.Source,
		req.StorageDestinationID, req.ScheduleCron, req.RetentionDays,
		req.Compression, req.Encrypt, encPassphrase, time.Now(), id,
	)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	result, err := s.db.Exec(`DELETE FROM backup_jobs WHERE id=$1`, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTriggerJob manually triggers a job run.
// The actual job dispatch happens via the scheduler package.
// This handler creates a backup_run record and signals the WS hub.
// The hub reference is injected after the server is created (set via SetHub).
func (s *Server) handleTriggerJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var job models.BackupJob
	if err := s.db.Get(&job, `SELECT * FROM backup_jobs WHERE id=$1`, id); err != nil {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}

	var run models.BackupRun
	if err := s.db.QueryRowx(
		`INSERT INTO backup_runs (job_id, status) VALUES ($1, 'running') RETURNING id, job_id, started_at, status`,
		id,
	).StructScan(&run); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Signal WS hub if available (injected via SetHub after server creation)
	if s.hub != nil {
		s.hub.DispatchJob(job.AgentID.String(), run.ID.String(), job)
	}

	respond(w, http.StatusCreated, run)
}
```

Note: `s.hub` field and `SetHub` method are added in Plan 3 (WebSocket). For now, declare the field in server.go:

```go
// Add to Server struct in server.go:
// hub    *ws.Hub  — added in Plan 3
```

For now, in `server.go` add:
```go
// Placeholder hub interface — replaced by ws.Hub in Plan 3
type jobDispatcher interface {
	DispatchJob(agentID, runID string, job models.BackupJob)
}

// Add to Server struct:
hub jobDispatcher
```

- [ ] **Step 4: Remove job stubs from stubs.go**

- [ ] **Step 5: Run tests**

```bash
go test ./internal/api/... -v -short
```

Expected: PASS all existing tests

- [ ] **Step 6: Commit**

```bash
git add internal/api/jobs.go internal/api/jobs_test.go internal/api/server.go internal/api/stubs.go
git commit -m "feat: add backup jobs CRUD and manual trigger handler"
```

---

### Task 7: Runs, Upload, and Settings Handlers

**Files:**
- Create: `internal/api/runs.go`
- Create: `internal/api/upload.go`
- Create: `internal/api/settings.go`

- [ ] **Step 1: Implement runs.go**

```go
// internal/api/runs.go
package api

import (
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	query := `SELECT id, job_id, started_at, finished_at, status, size_bytes, file_count, error_message, storage_path
	           FROM backup_runs WHERE 1=1`
	args := []any{}
	i := 1

	if jobID := q.Get("job_id"); jobID != "" {
		if id, err := uuid.Parse(jobID); err == nil {
			query += " AND job_id=$" + string(rune('0'+i))
			args = append(args, id)
			i++
		}
	}
	if status := q.Get("status"); status != "" {
		query += " AND status=$1"
		args = append(args, status)
	}

	query += " ORDER BY started_at DESC LIMIT 100"

	var runs []models.BackupRun
	if err := s.db.Select(&runs, query, args...); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, runs)
}

// handleRunLogs streams run logs via WebSocket (real-time during run).
// After run completes, returns stored error_message as final log entry.
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var run models.BackupRun
	if err := s.db.Get(&run, `SELECT * FROM backup_runs WHERE id=$1`, id); err != nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}

	// For completed runs, return error message if any
	if run.Status != models.RunStatusRunning {
		msg := ""
		if run.ErrorMessage != nil {
			msg = *run.ErrorMessage
		}
		respond(w, http.StatusOK, map[string]any{
			"run_id":  id,
			"status":  run.Status,
			"message": msg,
		})
		return
	}

	// For running: upgrade to WebSocket and subscribe to live logs from hub
	if s.hub != nil {
		s.hub.ServeRunLogs(w, r, id.String())
		return
	}

	respond(w, http.StatusOK, map[string]any{"status": "running", "message": "no log stream available"})
}
```

- [ ] **Step 2: Implement upload.go**

Agents upload backup files via chunked multipart POST to `/api/upload/:run_id`.

```go
// internal/api/upload.go
package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxUploadMemory = 32 << 20 // 32 MB buffer

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid run_id")
		return
	}

	// Verify run exists and is running
	var status string
	if err := s.db.QueryRow(`SELECT status FROM backup_runs WHERE id=$1`, runID).Scan(&status); err != nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	if status != "running" {
		respondError(w, http.StatusConflict, "run is not in running state")
		return
	}

	if err := r.ParseMultipartForm(maxUploadMemory); err != nil {
		respondError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		respondError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	// Store in local data dir (Plan 3 will route this to the correct storage backend)
	destDir := filepath.Join("data", "uploads", runID.String())
	if err := os.MkdirAll(destDir, 0700); err != nil {
		respondError(w, http.StatusInternalServerError, "create dir: "+err.Error())
		return
	}

	destPath := filepath.Join(destDir, header.Filename)
	dst, err := os.Create(destPath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "create file: "+err.Error())
		return
	}
	defer dst.Close()

	size, err := io.Copy(dst, file)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "write file: "+err.Error())
		return
	}

	// Update run with size and path
	_, err = s.db.Exec(
		`UPDATE backup_runs SET size_bytes=COALESCE(size_bytes,0)+$1, storage_path=$2 WHERE id=$3`,
		size, fmt.Sprintf("local://%s", destPath), runID,
	)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respond(w, http.StatusOK, map[string]any{"bytes_received": size, "path": destPath})
}
```

- [ ] **Step 3: Implement settings.go**

```go
// internal/api/settings.go
package api

import (
	"encoding/json"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/models"
)

func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var settings models.NotificationSettings
	err := s.db.Get(&settings, `SELECT id, type, config FROM notification_settings LIMIT 1`)
	if err != nil {
		// No settings yet — return empty
		respond(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	respond(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	_, err := s.db.Exec(`
		INSERT INTO notification_settings (type, config) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET type=$1, config=$2`,
		body.Type, body.Config,
	)
	if err != nil {
		// Try upsert by delete+insert if no unique constraint
		s.db.Exec(`DELETE FROM notification_settings`)
		_, err = s.db.Exec(`INSERT INTO notification_settings (type, config) VALUES ($1, $2)`, body.Type, body.Config)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Remove remaining stubs from stubs.go**

After implementing all handlers, `internal/api/stubs.go` should be empty. Delete it:

```bash
rm internal/api/stubs.go
```

- [ ] **Step 5: Verify build**

```bash
go build ./...
```

Expected: no errors

- [ ] **Step 6: Run all tests**

```bash
go test ./... -short -v
```

Expected: PASS all tests

- [ ] **Step 7: Commit**

```bash
git add internal/api/runs.go internal/api/upload.go internal/api/settings.go
git rm internal/api/stubs.go
git commit -m "feat: add runs, upload, and notification settings handlers"
```
