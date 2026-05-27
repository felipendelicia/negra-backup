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
type AgentConn struct {
	conn    *websocket.Conn
	agentID string
	send    chan []byte
}

// BrowseResponse carries the result of a browse_path request.
type BrowseResponse struct {
	Entries []BrowseEntry
	Err     string
}

type Hub struct {
	mu      sync.RWMutex
	agents  map[string]*AgentConn

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
				close(current.send)
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
	ac := &AgentConn{conn: conn, agentID: agentID, send: make(chan []byte, 64)}
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

func (h *Hub) DispatchJob(agentID, runID string, job models.BackupJob, storageType string, storageConfig json.RawMessage, passphrase string) {
	h.mu.RLock()
	ac, ok := h.agents[agentID]
	h.mu.RUnlock()
	if !ok {
		log.Printf("dispatch: agent %s not connected", agentID)
		return
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
		log.Printf("dispatch marshal: %v", err)
		return
	}

	select {
	case ac.send <- data:
	default:
		log.Printf("dispatch: agent %s send buffer full", agentID)
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
	default:
		return false
	}
}

func (h *Hub) BroadcastRunLog(runID, line string) {
	h.logMu.RLock()
	subs := make([]chan string, len(h.logSubs[runID]))
	copy(subs, h.logSubs[runID])
	h.logMu.RUnlock()

	for _, ch := range subs {
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
