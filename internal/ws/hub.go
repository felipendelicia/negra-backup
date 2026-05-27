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

type agentConn struct {
	conn    *websocket.Conn
	agentID string
	send    chan []byte
}

type Hub struct {
	mu      sync.RWMutex
	agents  map[string]*agentConn

	logMu   sync.RWMutex
	logSubs map[string][]chan string

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

func (h *Hub) Register(agentID string, conn *websocket.Conn) {
	ac := &agentConn{conn: conn, agentID: agentID, send: make(chan []byte, 64)}
	h.register <- ac
	if conn != nil {
		go ac.writePump()
	}
}

func (h *Hub) Unregister(agentID string) {
	h.unregister <- agentID
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
