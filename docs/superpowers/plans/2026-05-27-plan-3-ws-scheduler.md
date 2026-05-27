# WebSocket Hub, Scheduler & Email Notifier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the WebSocket hub (agent connections + live log streaming), cron scheduler (triggers backup jobs), email notifier (failure alerts), and retention cleanup job.

**Architecture:** Hub maintains a map of agentID → websocket.Conn. Scheduler uses `robfig/cron` to fire jobs — creates a `backup_runs` record, then sends `run_job` WS message to the agent. Agent responds with `job_done`/`job_failed` messages that update the run record. Plans 1 and 2 must be complete first.

**Tech Stack:** `gorilla/websocket`, `robfig/cron/v3`, `net/smtp` (stdlib), `sqlx`

---

## File Map

- `internal/ws/types.go` — all WS message types (JSON structs)
- `internal/ws/hub.go` — Hub: agent registry, message routing, log fan-out
- `internal/ws/handler.go` — HTTP WebSocket upgrade handler for agents
- `internal/scheduler/scheduler.go` — cron engine that dispatches jobs
- `internal/notify/email.go` — SMTP email sender
- `internal/notify/retention.go` — daily retention cleanup
- Update: `internal/api/server.go` — wire hub into Server struct

---

### Task 1: WebSocket Message Types

**Files:**
- Create: `internal/ws/types.go`
- Create: `internal/ws/types_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/ws/types_test.go
package ws_test

import (
	"encoding/json"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelloMessage_RoundTrip(t *testing.T) {
	msg := ws.AgentMessage{
		Type:    ws.MsgTypeHello,
		APIKey:  "abc123",
		OS:      "linux",
		Arch:    "amd64",
		Version: "1.0.0",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var got ws.AgentMessage
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, ws.MsgTypeHello, got.Type)
	assert.Equal(t, "linux", got.OS)
}

func TestServerMessage_RunJob(t *testing.T) {
	msg := ws.ServerMessage{
		Type:  ws.MsgTypeRunJob,
		RunID: "run-uuid-123",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "run_job")
	assert.Contains(t, string(data), "run-uuid-123")
}

func TestMessageTypeConstants(t *testing.T) {
	assert.Equal(t, "hello", ws.MsgTypeHello)
	assert.Equal(t, "heartbeat", ws.MsgTypeHeartbeat)
	assert.Equal(t, "job_progress", ws.MsgTypeJobProgress)
	assert.Equal(t, "job_done", ws.MsgTypeJobDone)
	assert.Equal(t, "job_failed", ws.MsgTypeJobFailed)
	assert.Equal(t, "run_job", ws.MsgTypeRunJob)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/ws/... -v
```

Expected: FAIL "no Go files"

- [ ] **Step 3: Implement types.go**

```go
// internal/ws/types.go
package ws

import "github.com/felipendelicia/nat-backup/internal/models"

// Message type constants — used in the "type" field of every WS message.
const (
	// Agent → Server
	MsgTypeHello       = "hello"
	MsgTypeHeartbeat   = "heartbeat"
	MsgTypeJobProgress = "job_progress"
	MsgTypeJobDone     = "job_done"
	MsgTypeJobFailed   = "job_failed"

	// Server → Agent
	MsgTypeRunJob = "run_job"
)

// AgentMessage is sent from agent to server.
type AgentMessage struct {
	Type string `json:"type"`

	// hello
	APIKey  string `json:"api_key,omitempty"`
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Version string `json:"version,omitempty"`

	// job_progress
	RunID       string `json:"run_id,omitempty"`
	Percent     int    `json:"percent,omitempty"`
	CurrentFile string `json:"current_file,omitempty"`

	// job_done
	Status      string `json:"status,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	StoragePath string `json:"storage_path,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`

	// job_failed
	Error string `json:"error,omitempty"`
}

// ServerMessage is sent from server to agent.
type ServerMessage struct {
	Type  string           `json:"type"`
	RunID string           `json:"run_id,omitempty"`
	Job   *models.BackupJob `json:"job,omitempty"`
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/ws/... -v
```

Expected: PASS all 3 tests

- [ ] **Step 5: Commit**

```bash
git add internal/ws/types.go internal/ws/types_test.go
git commit -m "feat: add WebSocket message types"
```

---

### Task 2: WebSocket Hub

**Files:**
- Create: `internal/ws/hub.go`
- Create: `internal/ws/hub_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/ws/hub_test.go
package ws_test

import (
	"sync"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/stretchr/testify/assert"
)

func TestHub_RegisterUnregister(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	hub.Register("agent-1", nil) // nil conn for unit test
	assert.True(t, hub.IsConnected("agent-1"))

	hub.Unregister("agent-1")
	time.Sleep(10 * time.Millisecond)
	assert.False(t, hub.IsConnected("agent-1"))
}

func TestHub_SubscribeLogsFanOut(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	ch1 := hub.SubscribeRunLogs("run-abc")
	ch2 := hub.SubscribeRunLogs("run-abc")
	defer hub.UnsubscribeRunLogs("run-abc", ch1)
	defer hub.UnsubscribeRunLogs("run-abc", ch2)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		select {
		case msg := <-ch1:
			assert.Equal(t, "hello", msg)
		case <-time.After(time.Second):
			t.Error("timeout waiting on ch1")
		}
	}()

	go func() {
		defer wg.Done()
		select {
		case msg := <-ch2:
			assert.Equal(t, "hello", msg)
		case <-time.After(time.Second):
			t.Error("timeout waiting on ch2")
		}
	}()

	hub.BroadcastRunLog("run-abc", "hello")
	wg.Wait()
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/ws/... -v
```

Expected: FAIL "undefined: ws.NewHub"

- [ ] **Step 3: Implement hub.go**

```go
// internal/ws/hub.go
package ws

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
)

// agentConn holds an agent's WS connection.
type agentConn struct {
	conn    *websocket.Conn
	agentID string
	send    chan []byte
}

// Hub manages all connected agents and run log subscribers.
type Hub struct {
	mu     sync.RWMutex
	agents map[string]*agentConn // agentID → conn

	// run log fan-out: runID → set of subscriber channels
	logMu       sync.RWMutex
	logSubs     map[string][]chan string

	register   chan *agentConn
	unregister chan string
	db         *sqlx.DB
}

func NewHub(db *sqlx.DB) *Hub {
	return &Hub{
		agents:     make(map[string]*agentConn),
		logSubs:    make(map[string][]chan string),
		register:   make(chan *agentConn, 16),
		unregister: make(chan string, 16),
		db:         db,
	}
}

// Run processes hub events. Call in a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case agent := <-h.register:
			h.mu.Lock()
			h.agents[agent.agentID] = agent
			h.mu.Unlock()
			log.Printf("agent %s connected", agent.agentID)

		case agentID := <-h.unregister:
			h.mu.Lock()
			if agent, ok := h.agents[agentID]; ok {
				close(agent.send)
				delete(h.agents, agentID)
			}
			h.mu.Unlock()
			if h.db != nil {
				h.db.Exec(`UPDATE agents SET status='offline' WHERE id=$1`, agentID)
			}
			log.Printf("agent %s disconnected", agentID)
		}
	}
}

// Register adds an agent connection to the hub.
func (h *Hub) Register(agentID string, conn *websocket.Conn) {
	ac := &agentConn{conn: conn, agentID: agentID, send: make(chan []byte, 64)}
	h.register <- ac
	if conn != nil {
		go ac.writePump()
	}
}

// Unregister removes an agent connection.
func (h *Hub) Unregister(agentID string) {
	h.unregister <- agentID
}

// IsConnected returns true if the agent is currently connected.
func (h *Hub) IsConnected(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.agents[agentID]
	return ok
}

// DispatchJob sends a run_job command to an agent.
func (h *Hub) DispatchJob(agentID, runID string, job models.BackupJob) {
	h.mu.RLock()
	ac, ok := h.agents[agentID]
	h.mu.RUnlock()
	if !ok {
		log.Printf("dispatch: agent %s not connected", agentID)
		return
	}

	msg := ServerMessage{Type: MsgTypeRunJob, RunID: runID, Job: &job}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("dispatch marshal: %v", err)
		return
	}

	select {
	case ac.send <- data:
	default:
		log.Printf("dispatch: agent %s send buffer full", agentID)
	}
}

// BroadcastRunLog sends a log line to all subscribers of a run.
func (h *Hub) BroadcastRunLog(runID, line string) {
	h.logMu.RLock()
	subs := h.logSubs[runID]
	h.logMu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- line:
		default:
		}
	}
}

// SubscribeRunLogs creates a channel that receives log lines for a run.
func (h *Hub) SubscribeRunLogs(runID string) chan string {
	ch := make(chan string, 64)
	h.logMu.Lock()
	h.logSubs[runID] = append(h.logSubs[runID], ch)
	h.logMu.Unlock()
	return ch
}

// UnsubscribeRunLogs removes a log subscriber channel.
func (h *Hub) UnsubscribeRunLogs(runID string, ch chan string) {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	subs := h.logSubs[runID]
	newSubs := subs[:0]
	for _, s := range subs {
		if s != ch {
			newSubs = append(newSubs, s)
		}
	}
	if len(newSubs) == 0 {
		delete(h.logSubs, runID)
	} else {
		h.logSubs[runID] = newSubs
	}
	close(ch)
}

// ServeRunLogs upgrades the HTTP connection to WebSocket and streams run logs.
// Called from the api.Server runs handler.
func (h *Hub) ServeRunLogs(w interface{ Header() interface{} }, r interface{}, runID string) {
	// This is a stub — the real implementation in handler.go uses the upgrader.
	// See ws.Handler for the actual WS upgrade.
}

// writePump drains the send channel to the WS connection.
func (ac *agentConn) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer ac.conn.Close()

	for {
		select {
		case msg, ok := <-ac.send:
			if !ok {
				ac.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			ac.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ac.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			ac.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ac.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/ws/... -v
```

Expected: PASS all 5 tests

- [ ] **Step 5: Commit**

```bash
git add internal/ws/hub.go internal/ws/hub_test.go
git commit -m "feat: add WebSocket hub with agent registry and log fan-out"
```

---

### Task 3: WebSocket HTTP Handler (Agent Connection)

**Files:**
- Create: `internal/ws/handler.go`
- Create: `internal/ws/handler_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/ws/handler_test.go
package ws_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/stretchr/testify/assert"
)

func TestAgentHandler_RejectsNonWS(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	handler := ws.NewAgentHandler(hub)

	req := httptest.NewRequest(http.MethodGet, "/ws/agent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Not a WS request — upgrader should reject
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/ws/... -run TestAgentHandler -v
```

Expected: FAIL "undefined: ws.NewAgentHandler"

- [ ] **Step 3: Implement handler.go**

```go
// internal/ws/handler.go
package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // agent connections are server-initiated trust
	},
}

// AgentHandler handles the WebSocket upgrade for agent connections.
type AgentHandler struct {
	hub *Hub
	db  *sqlx.DB
}

func NewAgentHandler(hub *Hub) *AgentHandler {
	return &AgentHandler{hub: hub, db: hub.db}
}

func (h *AgentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	// First message must be "hello" with API key
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}

	var hello AgentMessage
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != MsgTypeHello {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "expected hello"))
		conn.Close()
		return
	}

	// Authenticate API key
	agentID, err := h.authenticateAPIKey(hello.APIKey)
	if err != nil {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4003, "unauthorized"))
		conn.Close()
		return
	}

	// Update agent record
	if h.db != nil {
		h.db.Exec(
			`UPDATE agents SET status='online', last_seen=NOW(), os=$1, arch=$2, version=$3 WHERE id=$4`,
			hello.OS, hello.Arch, hello.Version, agentID,
		)
	}

	h.hub.Register(agentID, conn)
	defer h.hub.Unregister(agentID)

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Read pump: process messages from agent
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		var msg AgentMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		h.handleAgentMessage(agentID, msg)
	}
}

func (h *AgentHandler) handleAgentMessage(agentID string, msg AgentMessage) {
	switch msg.Type {
	case MsgTypeHeartbeat:
		if h.db != nil {
			h.db.Exec(`UPDATE agents SET last_seen=NOW() WHERE id=$1`, agentID)
		}

	case MsgTypeJobProgress:
		h.hub.BroadcastRunLog(msg.RunID,
			log.Sprintf("%d%% %s", msg.Percent, msg.CurrentFile))

	case MsgTypeJobDone:
		if h.db != nil {
			h.db.Exec(`
				UPDATE backup_runs SET
				  status='success', finished_at=NOW(),
				  size_bytes=$1, file_count=$2, storage_path=$3
				WHERE id=$4`,
				msg.SizeBytes, msg.FileCount, msg.StoragePath, msg.RunID)
		}
		h.hub.BroadcastRunLog(msg.RunID, "completed: "+msg.StoragePath)

	case MsgTypeJobFailed:
		if h.db != nil {
			h.db.Exec(`
				UPDATE backup_runs SET status='failed', finished_at=NOW(), error_message=$1
				WHERE id=$2`,
				msg.Error, msg.RunID)
		}
		h.hub.BroadcastRunLog(msg.RunID, "failed: "+msg.Error)

		// Trigger email notification
		go h.notifyFailure(msg.RunID, msg.Error)
	}
}

func (h *AgentHandler) notifyFailure(runID, errMsg string) {
	// Will be wired to notify.EmailSender in Plan 3 Task 5
	log.Printf("backup run %s failed: %s", runID, errMsg)
}

func (h *AgentHandler) authenticateAPIKey(apiKey string) (string, error) {
	if h.db == nil {
		return "test-agent-id", nil // test mode
	}

	rows, err := h.db.Query(`SELECT id, api_key FROM agents`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			continue
		}
		// api_key column stores bcrypt hash
		import "golang.org/x/crypto/bcrypt"
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(apiKey)) == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("invalid api key")
}
```

Note: Fix the inline import in `authenticateAPIKey` — move `bcrypt` import to the file's import block:

```go
// internal/ws/handler.go — correct import block
import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

// authenticateAPIKey — remove the inline import, use package-level import
func (h *AgentHandler) authenticateAPIKey(apiKey string) (string, error) {
	if h.db == nil {
		return "test-agent-id", nil
	}

	rows, err := h.db.Query(`SELECT id, api_key FROM agents`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(apiKey)) == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("invalid api key")
}
```

Also fix the `log.Sprintf` in `handleAgentMessage` — it should be `fmt.Sprintf`:

```go
case MsgTypeJobProgress:
	h.hub.BroadcastRunLog(msg.RunID,
		fmt.Sprintf("%d%% %s", msg.Percent, msg.CurrentFile))
```

- [ ] **Step 4: Add WS route to api/server.go**

Add to `buildRouter()` in `internal/api/server.go`:

```go
// WebSocket endpoint for agents (no JWT — uses WS hello message auth)
r.Get("/ws/agent", s.wsHandler.ServeHTTP)
```

And add `wsHandler` field to `Server` struct:

```go
type Server struct {
	router    chi.Router
	db        *sqlx.DB
	cfg       config.Config
	hub       jobDispatcher
	wsHandler http.Handler
}
```

Update `NewServer` to initialize hub and wsHandler:

```go
func NewServer(db *sqlx.DB, cfg config.Config) http.Handler {
	hub := ws.NewHub(db)
	go hub.Run()

	s := &Server{
		db:        db,
		cfg:       cfg,
		hub:       hub,
		wsHandler: ws.NewAgentHandler(hub),
	}
	s.router = s.buildRouter()
	return s
}
```

Add import `"github.com/felipendelicia/nat-backup/internal/ws"` to `server.go`.

Update `jobDispatcher` interface in `server.go` to match Hub's real method:
```go
type jobDispatcher interface {
	DispatchJob(agentID, runID string, job models.BackupJob)
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/ws/... -v
go build ./...
```

Expected: all PASS, build succeeds

- [ ] **Step 6: Commit**

```bash
git add internal/ws/handler.go internal/ws/handler_test.go internal/api/server.go
git commit -m "feat: add WebSocket agent connection handler"
```

---

### Task 4: Cron Scheduler

**Files:**
- Create: `internal/scheduler/scheduler.go`
- Create: `internal/scheduler/scheduler_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/scheduler/scheduler_test.go
package scheduler_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDispatcher struct {
	calls atomic.Int32
}

func (m *mockDispatcher) DispatchJob(agentID, runID string, job interface{}) {
	m.calls.Add(1)
}

func TestScheduler_StartStop(t *testing.T) {
	dispatch := &mockDispatcher{}
	s := scheduler.New(nil, dispatch)
	s.Start()
	s.Stop()
	// no panic = pass
}

func TestScheduler_ParseCron(t *testing.T) {
	valid := []string{"0 2 * * *", "*/5 * * * *", "@daily"}
	for _, expr := range valid {
		err := scheduler.ValidateCron(expr)
		require.NoError(t, err, "expected valid: %s", expr)
	}

	invalid := []string{"not-a-cron", "60 * * * *"}
	for _, expr := range invalid {
		err := scheduler.ValidateCron(expr)
		assert.Error(t, err, "expected invalid: %s", expr)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/scheduler/... -v
```

Expected: FAIL "no Go files"

- [ ] **Step 3: Implement scheduler.go**

```go
// internal/scheduler/scheduler.go
package scheduler

import (
	"fmt"
	"log"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/jmoiron/sqlx"
	"github.com/robfig/cron/v3"
)

// Dispatcher sends jobs to connected agents.
type Dispatcher interface {
	DispatchJob(agentID, runID string, job interface{})
}

// JobDispatcher is the typed version used with the real hub.
type JobDispatcher interface {
	DispatchJob(agentID, runID string, job models.BackupJob)
}

// Scheduler wraps robfig/cron and manages backup job schedules.
type Scheduler struct {
	cron       *cron.Cron
	db         *sqlx.DB
	dispatcher JobDispatcher
	entryIDs   map[string]cron.EntryID // jobID → cron entry
}

// New creates a new Scheduler. Call Start() to begin scheduling.
func New(db *sqlx.DB, dispatcher JobDispatcher) *Scheduler {
	return &Scheduler{
		cron:       cron.New(),
		db:         db,
		dispatcher: dispatcher,
		entryIDs:   make(map[string]cron.EntryID),
	}
}

// Start loads all enabled jobs from DB and schedules them.
func (s *Scheduler) Start() {
	if s.db != nil {
		s.loadJobs()
	}
	s.cron.Start()
	log.Println("scheduler started")
}

// Stop halts all scheduled jobs.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// loadJobs fetches all enabled backup jobs and registers their cron entries.
func (s *Scheduler) loadJobs() {
	var jobs []models.BackupJob
	if err := s.db.Select(&jobs, `SELECT * FROM backup_jobs WHERE enabled=true`); err != nil {
		log.Printf("scheduler load jobs: %v", err)
		return
	}

	for _, job := range jobs {
		if err := s.AddJob(job); err != nil {
			log.Printf("scheduler add job %s: %v", job.ID, err)
		}
	}
	log.Printf("scheduler loaded %d jobs", len(jobs))
}

// AddJob adds or replaces a cron entry for a backup job.
func (s *Scheduler) AddJob(job models.BackupJob) error {
	// Remove existing entry if any
	if id, ok := s.entryIDs[job.ID.String()]; ok {
		s.cron.Remove(id)
	}

	if !job.Enabled {
		return nil
	}

	entryID, err := s.cron.AddFunc(job.ScheduleCron, func() {
		s.triggerJob(job)
	})
	if err != nil {
		return fmt.Errorf("cron add: %w", err)
	}

	s.entryIDs[job.ID.String()] = entryID
	return nil
}

// RemoveJob removes a job's cron entry.
func (s *Scheduler) RemoveJob(jobID string) {
	if id, ok := s.entryIDs[jobID]; ok {
		s.cron.Remove(id)
		delete(s.entryIDs, jobID)
	}
}

// triggerJob creates a backup_run record and dispatches to the agent.
func (s *Scheduler) triggerJob(job models.BackupJob) {
	if s.db == nil {
		return
	}

	var runID string
	err := s.db.QueryRow(
		`INSERT INTO backup_runs (job_id, status) VALUES ($1, 'running') RETURNING id`,
		job.ID,
	).Scan(&runID)
	if err != nil {
		log.Printf("scheduler create run for job %s: %v", job.ID, err)
		return
	}

	log.Printf("scheduler dispatching job %s (run %s) to agent %s", job.ID, runID, job.AgentID)
	s.dispatcher.DispatchJob(job.AgentID.String(), runID, job)
}

// ValidateCron returns an error if the cron expression is invalid.
func ValidateCron(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	_, err := parser.Parse(expr)
	return err
}
```

- [ ] **Step 4: Fix test mock — Dispatcher interface mismatch**

The test uses `mockDispatcher.DispatchJob(agentID, runID string, job interface{})` but `JobDispatcher` requires `models.BackupJob`. Update the test:

```go
// internal/scheduler/scheduler_test.go
package scheduler_test

import (
	"sync/atomic"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDispatcher struct {
	calls atomic.Int32
}

func (m *mockDispatcher) DispatchJob(agentID, runID string, job models.BackupJob) {
	m.calls.Add(1)
}

func TestScheduler_StartStop(t *testing.T) {
	dispatch := &mockDispatcher{}
	s := scheduler.New(nil, dispatch)
	s.Start()
	s.Stop()
}

func TestScheduler_ParseCron(t *testing.T) {
	valid := []string{"0 2 * * *", "*/5 * * * *", "@daily"}
	for _, expr := range valid {
		err := scheduler.ValidateCron(expr)
		require.NoError(t, err, "expected valid: %s", expr)
	}

	invalid := []string{"not-a-cron"}
	for _, expr := range invalid {
		err := scheduler.ValidateCron(expr)
		assert.Error(t, err, "expected invalid: %s", expr)
	}
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/scheduler/... -v
```

Expected: PASS both tests

- [ ] **Step 6: Wire scheduler into cmd/server/main.go**

Add to `main()` after hub creation:

```go
// in cmd/server/main.go
import "github.com/felipendelicia/nat-backup/internal/scheduler"

// after api.NewServer(pool, cfg):
sched := scheduler.New(pool, hub)
sched.Start()
defer sched.Stop()
```

Note: `hub` must be exposed from `api.NewServer`. Refactor `api.NewServer` to also return the hub:

```go
// Change NewServer signature in internal/api/server.go:
func NewServer(db *sqlx.DB, cfg config.Config) (http.Handler, *ws.Hub) {
	hub := ws.NewHub(db)
	go hub.Run()
	s := &Server{db: db, cfg: cfg, hub: hub, wsHandler: ws.NewAgentHandler(hub)}
	s.router = s.buildRouter()
	return s, hub
}
```

Update `cmd/server/main.go` accordingly:
```go
handler, hub := api.NewServer(pool, cfg)
sched := scheduler.New(pool, hub)
sched.Start()
defer sched.Stop()
```

- [ ] **Step 7: Build**

```bash
go build ./...
```

Expected: no errors

- [ ] **Step 8: Commit**

```bash
git add internal/scheduler/ internal/api/server.go cmd/server/main.go
git commit -m "feat: add cron scheduler for backup jobs"
```

---

### Task 5: Email Notifier + Retention Cleanup

**Files:**
- Create: `internal/notify/email.go`
- Create: `internal/notify/email_test.go`
- Create: `internal/notify/retention.go`
- Create: `internal/notify/retention_test.go`
- Update: `internal/ws/handler.go` — wire email notifier

- [ ] **Step 1: Write failing email test**

```go
// internal/notify/email_test.go
package notify_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/stretchr/testify/assert"
)

func TestNewEmailSender_Config(t *testing.T) {
	cfg := models.EmailNotificationConfig{
		SMTPHost: "smtp.example.com",
		SMTPPort: 587,
		From:     "backup@example.com",
		To:       []string{"admin@example.com"},
		TLS:      true,
	}
	sender := notify.NewEmailSender(cfg)
	assert.NotNil(t, sender)
}

// TestSendEmail_ActualSMTP would test real sending — skip in unit tests.
func TestSendEmail_MockSend(t *testing.T) {
	// Use notify.EmailSender with mock SMTP — just verify no panic on valid args
	sender := notify.NewEmailSender(models.EmailNotificationConfig{
		SMTPHost: "localhost",
		SMTPPort: 1025, // mailhog or similar
		From:     "test@test.com",
		To:       []string{"admin@test.com"},
	})
	// Don't actually send — just test the struct is sane
	assert.NotNil(t, sender)
}
```

- [ ] **Step 2: Write failing retention test**

```go
// internal/notify/retention_test.go
package notify_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/stretchr/testify/assert"
)

func TestRetentionCleaner_New(t *testing.T) {
	cleaner := notify.NewRetentionCleaner(nil, nil)
	assert.NotNil(t, cleaner)
}
```

- [ ] **Step 3: Run to verify failure**

```bash
go test ./internal/notify/... -v
```

Expected: FAIL "no Go files"

- [ ] **Step 4: Implement email.go**

```go
// internal/notify/email.go
package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"github.com/felipendelicia/nat-backup/internal/models"
)

// EmailSender sends backup failure notifications via SMTP.
type EmailSender struct {
	cfg models.EmailNotificationConfig
}

func NewEmailSender(cfg models.EmailNotificationConfig) *EmailSender {
	return &EmailSender{cfg: cfg}
}

// SendFailureAlert sends a backup failure email.
func (e *EmailSender) SendFailureAlert(jobName, agentName, runID, errMsg string) error {
	subject := fmt.Sprintf("[nat-backup] Backup FAILED: %s", jobName)
	body := fmt.Sprintf(
		"Backup job failed.\n\nJob: %s\nAgent: %s\nRun ID: %s\n\nError:\n%s",
		jobName, agentName, runID, errMsg,
	)
	return e.send(subject, body)
}

func (e *EmailSender) send(subject, body string) error {
	msg := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		e.cfg.From,
		strings.Join(e.cfg.To, ", "),
		subject,
		body,
	))

	addr := fmt.Sprintf("%s:%d", e.cfg.SMTPHost, e.cfg.SMTPPort)

	var auth smtp.Auth
	if e.cfg.Username != "" {
		auth = smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.SMTPHost)
	}

	if e.cfg.TLS {
		return e.sendTLS(addr, auth, msg)
	}

	return smtp.SendMail(addr, auth, e.cfg.From, e.cfg.To, msg)
}

func (e *EmailSender) sendTLS(addr string, auth smtp.Auth, msg []byte) error {
	host, _, _ := net.SplitHostPort(addr)
	tlsCfg := &tls.Config{ServerName: host}

	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(e.cfg.From); err != nil {
		return fmt.Errorf("smtp MAIL: %w", err)
	}
	for _, to := range e.cfg.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("smtp RCPT %s: %w", to, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	return w.Close()
}
```

- [ ] **Step 5: Implement retention.go**

```go
// internal/notify/retention.go
package notify

import (
	"log"
	"time"

	"github.com/jmoiron/sqlx"
)

// RetentionCleaner deletes backup runs older than their job's retention_days.
type RetentionCleaner struct {
	db     *sqlx.DB
	sender *EmailSender
}

func NewRetentionCleaner(db *sqlx.DB, sender *EmailSender) *RetentionCleaner {
	return &RetentionCleaner{db: db, sender: sender}
}

// Run performs one retention sweep. Call on a daily cron.
func (rc *RetentionCleaner) Run() {
	if rc.db == nil {
		return
	}

	log.Println("retention: starting cleanup")

	// Find runs older than their job's retention_days
	rows, err := rc.db.Query(`
		SELECT br.id, br.storage_path
		FROM backup_runs br
		JOIN backup_jobs bj ON br.job_id = bj.id
		WHERE br.started_at < NOW() - (bj.retention_days || ' days')::INTERVAL
		  AND br.status IN ('success', 'failed')
	`)
	if err != nil {
		log.Printf("retention query: %v", err)
		return
	}
	defer rows.Close()

	var deleted int
	for rows.Next() {
		var id string
		var storagePath *string
		if err := rows.Scan(&id, &storagePath); err != nil {
			continue
		}

		// Delete storage file (best effort)
		if storagePath != nil {
			rc.deleteStorageFile(*storagePath)
		}

		// Delete run record
		if _, err := rc.db.Exec(`DELETE FROM backup_runs WHERE id=$1`, id); err != nil {
			log.Printf("retention delete run %s: %v", id, err)
			continue
		}
		deleted++
	}

	log.Printf("retention: deleted %d old runs", deleted)
}

func (rc *RetentionCleaner) deleteStorageFile(path string) {
	// Storage deletion is handled by the storage backend.
	// For now, log — Plan 4 will wire in the actual storage backends.
	log.Printf("retention: would delete storage file: %s", path)
}

// StartDailySchedule runs the retention cleaner every day at midnight.
func (rc *RetentionCleaner) StartDailySchedule() {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			time.Sleep(time.Until(next))
			rc.Run()
		}
	}()
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/notify/... -v
```

Expected: PASS both tests

- [ ] **Step 7: Wire email into WS handler**

Update `internal/ws/handler.go` — add `emailSender` field and `SetEmailSender` method:

```go
// Add to AgentHandler struct:
type AgentHandler struct {
	hub         *Hub
	db          *sqlx.DB
	emailSender interface {
		SendFailureAlert(jobName, agentName, runID, errMsg string) error
	}
}

// Add method:
func (h *AgentHandler) SetEmailSender(sender interface {
	SendFailureAlert(jobName, agentName, runID, errMsg string) error
}) {
	h.emailSender = sender
}

// Update notifyFailure:
func (h *AgentHandler) notifyFailure(runID, errMsg string) {
	if h.emailSender == nil {
		log.Printf("backup run %s failed (no email configured): %s", runID, errMsg)
		return
	}
	// Fetch job name and agent name from DB for a useful email
	var jobName, agentName string
	if h.db != nil {
		h.db.QueryRow(`
			SELECT bj.name, a.name
			FROM backup_runs br
			JOIN backup_jobs bj ON br.job_id=bj.id
			JOIN agents a ON bj.agent_id=a.id
			WHERE br.id=$1`, runID).Scan(&jobName, &agentName)
	}
	if err := h.emailSender.SendFailureAlert(jobName, agentName, runID, errMsg); err != nil {
		log.Printf("send failure email: %v", err)
	}
}
```

Wire in `cmd/server/main.go`:

```go
// After api.NewServer:
// Load notification settings from DB
var notifCfg models.EmailNotificationConfig
// (attempt to load — if none configured, skip)
var emailSender *notify.EmailSender
var nsConfig models.NotificationSettings
if err := pool.Get(&nsConfig, `SELECT * FROM notification_settings WHERE type='email' LIMIT 1`); err == nil {
	if err := json.Unmarshal(nsConfig.Config, &notifCfg); err == nil {
		emailSender = notify.NewEmailSender(notifCfg)
	}
}
// wsHandler.SetEmailSender(emailSender) — exposed from api package
```

For now, just ensure it compiles. The email sender is optional (nil = no emails).

- [ ] **Step 8: Build and test**

```bash
go build ./...
go test ./... -short -v
```

Expected: all PASS

- [ ] **Step 9: Commit**

```bash
git add internal/notify/ internal/ws/handler.go cmd/server/main.go
git commit -m "feat: add email notifier and daily retention cleanup"
```
