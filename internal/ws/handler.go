// internal/ws/handler.go
package ws

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type FailureNotifier interface {
	SendFailureAlert(jobName, agentName, runID, errMsg string) error
}

// ConsoleWriter is implemented by the API ConsoleHub to accept agent log lines.
type ConsoleWriter interface {
	Write(p []byte) (int, error)
}

type AgentHandler struct {
	hub             *Hub
	db              *sqlx.DB
	failureNotifier FailureNotifier
	consoleWriter   ConsoleWriter
}

func (h *AgentHandler) SetConsoleWriter(w ConsoleWriter) { h.consoleWriter = w }

func NewAgentHandler(hub *Hub) *AgentHandler {
	return &AgentHandler{hub: hub, db: hub.db}
}

func (h *AgentHandler) SetFailureNotifier(n FailureNotifier) {
	h.failureNotifier = n
}

func (h *AgentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

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

	agentID, err := h.authenticateAPIKey(hello.APIKey)
	if err != nil {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4003, "unauthorized"))
		conn.Close()
		return
	}

	if h.db != nil {
		if _, err := h.db.Exec(
			`UPDATE agents SET status='online', last_seen=NOW(), os=$1, arch=$2, version=$3 WHERE id=$4`,
			hello.OS, hello.Arch, hello.Version, agentID,
		); err != nil {
			log.Printf("db update agent online %s: %v", agentID, err)
		}
	}

	ac := h.hub.Register(agentID, conn)
	defer h.hub.Unregister(ac)

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

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
	case MsgTypeBrowseResult:
		h.hub.DeliverBrowseResult(msg.RequestID, msg.Entries, msg.BrowseError)

	case MsgTypeAgentLog:
		if h.consoleWriter != nil && msg.Log != "" {
			// Resolve agent name for the prefix
			agentName := agentID[:8]
			if h.db != nil {
				var name string
				if err := h.db.QueryRow(`SELECT name FROM agents WHERE id=$1`, agentID).Scan(&name); err == nil {
					agentName = name
				}
			}
			line := fmt.Sprintf("[agent:%s] %s\n", agentName, msg.Log)
			h.consoleWriter.Write([]byte(line)) //nolint:errcheck
		}

	case MsgTypeHeartbeat:
		if h.db != nil {
			if _, err := h.db.Exec(`UPDATE agents SET last_seen=NOW() WHERE id=$1`, agentID); err != nil {
				log.Printf("db update heartbeat %s: %v", agentID, err)
			}
		}

	case MsgTypeJobProgress:
		h.hub.BroadcastRunLog(msg.RunID, fmt.Sprintf("%d%% %s", msg.Percent, msg.CurrentFile))

	case MsgTypeJobDone:
		if h.db != nil {
			if _, err := h.db.Exec(`
				UPDATE backup_runs SET
				  status='success', finished_at=NOW(),
				  size_bytes=$1, file_count=$2, storage_path=$3
				WHERE id=$4`,
				msg.SizeBytes, msg.FileCount, msg.StoragePath, msg.RunID); err != nil {
				log.Printf("db update run success %s: %v", msg.RunID, err)
			}
		}
		h.hub.BroadcastRunLog(msg.RunID, "completed: "+msg.StoragePath)

	case MsgTypeJobFailed:
		if h.db != nil {
			// Skip update if already cancelled by user
			var currentStatus string
			if err := h.db.QueryRow(`SELECT status FROM backup_runs WHERE id=$1`, msg.RunID).Scan(&currentStatus); err == nil && currentStatus == "cancelled" {
				break
			}
			if _, err := h.db.Exec(`
				UPDATE backup_runs SET status='failed', finished_at=NOW(), error_message=$1
				WHERE id=$2`,
				msg.Error, msg.RunID); err != nil {
				log.Printf("db update run failed %s: %v", msg.RunID, err)
			}
		}
		h.hub.BroadcastRunLog(msg.RunID, "failed: "+msg.Error)
		go h.notifyFailure(msg.RunID, msg.Error)
	}
}

func (h *AgentHandler) notifyFailure(runID, errMsg string) {
	if h.failureNotifier == nil {
		log.Printf("backup run %s failed (no notifier configured): %s", runID, errMsg)
		return
	}
	var jobName, agentName string
	if h.db != nil {
		err := h.db.QueryRow(`
			SELECT bj.name, a.name
			FROM backup_runs br
			JOIN backup_jobs bj ON br.job_id=bj.id
			JOIN agents a ON bj.agent_id=a.id
			WHERE br.id=$1`, runID).Scan(&jobName, &agentName)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("notifyFailure: lookup run %s: %v", runID, err)
		}
		if jobName == "" {
			jobName = "<unknown>"
		}
		if agentName == "" {
			agentName = "<unknown>"
		}
	}
	if err := h.failureNotifier.SendFailureAlert(jobName, agentName, runID, errMsg); err != nil {
		log.Printf("send failure alert: %v", err)
	}
}

func (h *AgentHandler) authenticateAPIKey(apiKey string) (string, error) {
	if h.db == nil {
		return "test-agent-id", nil
	}
	rows, err := h.db.Query(`SELECT id, api_key FROM agents`)
	if err != nil {
		return "", fmt.Errorf("db query: %w", err)
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
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("rows error: %w", err)
	}
	return "", fmt.Errorf("invalid api key")
}
