// internal/ws/hub.go
package ws

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
)

// AgentConn holds the state for a single agent WebSocket connection.
//
// send is never closed. Closing it from the hub while another goroutine was in the
// middle of a send panicked with "send on closed channel" and, because several of
// those senders run in goroutines without a recover (the cron scheduler among them),
// took the whole process down. Shutdown is signalled by closing done instead: senders
// select on it, and the channel itself is left for the GC.
type AgentConn struct {
	conn      *websocket.Conn
	agentID   string
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

// close signals every sender that this connection is gone. Safe to call repeatedly.
func (ac *AgentConn) close() {
	ac.closeOnce.Do(func() { close(ac.done) })
}

// BrowseResponse carries the result of a browse_path request.
type BrowseResponse struct {
	Entries []BrowseEntry
	Err     string
}

type Hub struct {
	mu     sync.RWMutex
	agents map[string]*AgentConn

	logMu   sync.RWMutex
	logSubs map[string][]chan string

	browseMu   sync.Mutex
	browseReqs map[string]chan BrowseResponse

	register   chan *AgentConn
	unregister chan *AgentConn
	db         *sqlx.DB
}

func NewHub(db *sqlx.DB) *Hub {
	return &Hub{
		agents:     make(map[string]*AgentConn),
		logSubs:    make(map[string][]chan string),
		browseReqs: make(map[string]chan BrowseResponse),
		register:   make(chan *AgentConn, 16),
		unregister: make(chan *AgentConn, 16),
		db:         db,
	}
}

func (h *Hub) Run() {
	reconcile := time.NewTicker(60 * time.Second)
	defer reconcile.Stop()

	for {
		select {
		case <-reconcile.C:
			h.reconcileOffline()

		case ac := <-h.register:
			h.mu.Lock()
			h.agents[ac.agentID] = ac
			h.mu.Unlock()
			log.Printf("agent %s connected", ac.agentID)

		case ac := <-h.unregister:
			h.mu.Lock()
			if current, ok := h.agents[ac.agentID]; ok && current == ac {
				current.close()
				delete(h.agents, ac.agentID)
			}
			h.mu.Unlock()
			if h.db != nil {
				if _, err := h.db.Exec(`UPDATE agents SET status='offline' WHERE id=$1`, ac.agentID); err != nil {
					log.Printf("db update agent offline: %v", err)
				}
			}
			log.Printf("agent %s disconnected", ac.agentID)
		}
	}
}

// Register adds an agent connection and returns the AgentConn handle.
// Pass the returned handle to Unregister to avoid the reconnect race.
func (h *Hub) Register(agentID string, conn *websocket.Conn) *AgentConn {
	ac := &AgentConn{
		conn:    conn,
		agentID: agentID,
		send:    make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	h.register <- ac
	if conn != nil {
		go ac.writePump()
	}
	return ac
}

// Unregister removes the specific agent connection by pointer identity,
// preventing a stale unregister from closing a newer connection for the same agent.
func (h *Hub) Unregister(ac *AgentConn) {
	h.unregister <- ac
}

// reconcileOffline marks as offline any agent the DB thinks is online
// but that is not present in the hub's active connections.
func (h *Hub) reconcileOffline() {
	if h.db == nil {
		return
	}
	rows, err := h.db.Query(`SELECT id FROM agents WHERE status='online'`)
	if err != nil {
		log.Printf("reconcileOffline: %v", err)
		return
	}
	defer rows.Close()

	h.mu.RLock()
	connected := make(map[string]bool, len(h.agents))
	for id := range h.agents {
		connected[id] = true
	}
	h.mu.RUnlock()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if !connected[id] {
			if _, err := h.db.Exec(`UPDATE agents SET status='offline' WHERE id=$1`, id); err != nil {
				log.Printf("reconcileOffline: mark offline %s: %v", id, err)
			}
		}
	}
}

func (h *Hub) IsConnected(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.agents[agentID]
	return ok
}

// DispatchJob sends a run_job message to the agent. It returns an error when the
// job could not be handed off, so the caller can fail the run instead of leaving it
// stuck in 'running' forever — which is what happened when this returned nothing.
func (h *Hub) DispatchJob(agentID, runID string, job models.BackupJob, storageType string, storageConfig json.RawMessage, passphrase string) error {
	h.mu.RLock()
	ac, ok := h.agents[agentID]
	h.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %s is not connected", agentID)
	}

	msg := ServerMessage{
		Type:          MsgTypeRunJob,
		RunID:         runID,
		Job:           &job,
		StorageType:   storageType,
		StorageConfig: storageConfig,
		Passphrase:    passphrase,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("dispatch marshal: %w", err)
	}

	select {
	case ac.send <- data:
		return nil
	case <-ac.done:
		return fmt.Errorf("agent %s disconnected before dispatch", agentID)
	default:
		return fmt.Errorf("agent %s send buffer full", agentID)
	}
}

// BrowsePath sends a browse_path request to the agent and waits for the result.
// Returns an error if the agent is not connected or times out.
func (h *Hub) BrowsePath(agentID, requestID, path string) ([]BrowseEntry, error) {
	h.mu.RLock()
	ac, ok := h.agents[agentID]
	h.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agent not connected")
	}

	ch := make(chan BrowseResponse, 1)
	h.browseMu.Lock()
	h.browseReqs[requestID] = ch
	h.browseMu.Unlock()

	defer func() {
		h.browseMu.Lock()
		delete(h.browseReqs, requestID)
		h.browseMu.Unlock()
	}()

	data, _ := json.Marshal(ServerMessage{Type: MsgTypeBrowsePath, RequestID: requestID, Path: path})
	select {
	case ac.send <- data:
	case <-ac.done:
		return nil, fmt.Errorf("agent disconnected")
	default:
		return nil, fmt.Errorf("agent send buffer full")
	}

	select {
	case res := <-ch:
		if res.Err != "" {
			return nil, fmt.Errorf("%s", res.Err)
		}
		return res.Entries, nil
	case <-time.After(8 * time.Second):
		return nil, fmt.Errorf("timeout waiting for agent response")
	}
}

// DeliverBrowseResult routes a browse_result message from the agent to the waiting caller.
func (h *Hub) DeliverBrowseResult(requestID string, entries []BrowseEntry, errStr string) {
	h.browseMu.Lock()
	ch, ok := h.browseReqs[requestID]
	h.browseMu.Unlock()
	if ok {
		ch <- BrowseResponse{Entries: entries, Err: errStr}
	}
}

// UpdateAgent sends an update_agent message to the specified agent.
func (h *Hub) UpdateAgent(agentID string) bool {
	h.mu.RLock()
	ac, ok := h.agents[agentID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	data, _ := json.Marshal(ServerMessage{Type: MsgTypeUpdateAgent})
	select {
	case ac.send <- data:
		return true
	case <-ac.done:
		return false
	default:
		return false
	}
}

// CancelJob sends a cancel_job message to the agent running the given run.
// Returns false if the agent is not connected.
func (h *Hub) CancelJob(agentID, runID string) bool {
	h.mu.RLock()
	ac, ok := h.agents[agentID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	msg := ServerMessage{
		Type:  MsgTypeCancelJob,
		RunID: runID,
	}
	data, _ := json.Marshal(msg)
	select {
	case ac.send <- data:
		return true
	case <-ac.done:
		return false
	default:
		return false
	}
}

func (h *Hub) BroadcastRunLog(runID, line string) {
	// The fan-out runs under the read lock on purpose. Copying the subscribers and
	// releasing the lock first left a window in which UnsubscribeRunLogs could close
	// a channel that this loop was about to send on, panicking the process. The sends
	// are non-blocking, so holding the lock here costs nothing.
	h.logMu.RLock()
	defer h.logMu.RUnlock()

	for _, ch := range h.logSubs[runID] {
		select {
		case ch <- line:
		default:
		}
	}
}

func (h *Hub) SubscribeRunLogs(runID string) chan string {
	ch := make(chan string, 64)
	h.logMu.Lock()
	h.logSubs[runID] = append(h.logSubs[runID], ch)
	h.logMu.Unlock()
	return ch
}

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

func (ac *AgentConn) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer ac.conn.Close()
	for {
		select {
		case <-ac.done:
			ac.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			ac.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		case msg := <-ac.send:
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
