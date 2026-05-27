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

// AgentConn holds the state for a single agent WebSocket connection.
type AgentConn struct {
	conn    *websocket.Conn
	agentID string
	send    chan []byte
}

type Hub struct {
	mu      sync.RWMutex
	agents  map[string]*AgentConn

	logMu   sync.RWMutex
	logSubs map[string][]chan string

	register   chan *AgentConn
	unregister chan *AgentConn
	db         *sqlx.DB
}

func NewHub(db *sqlx.DB) *Hub {
	return &Hub{
		agents:     make(map[string]*AgentConn),
		logSubs:    make(map[string][]chan string),
		register:   make(chan *AgentConn, 16),
		unregister: make(chan *AgentConn, 16),
		db:         db,
	}
}

func (h *Hub) Run() {
	for {
		select {
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

func (h *Hub) IsConnected(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.agents[agentID]
	return ok
}

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
