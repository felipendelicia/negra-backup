// internal/api/runs.go
package api

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := `SELECT id, job_id, started_at, finished_at, status, size_bytes, file_count, error_message, storage_path FROM backup_runs WHERE 1=1`
	var args []any
	i := 1

	if jobID := q.Get("job_id"); jobID != "" {
		if id, err := uuid.Parse(jobID); err == nil {
			query += fmt.Sprintf(" AND job_id=$%d", i)
			args = append(args, id)
			i++
		}
	}
	if status := q.Get("status"); status != "" {
		query += fmt.Sprintf(" AND status=$%d", i)
		args = append(args, status)
		i++
	}
	query += " ORDER BY started_at DESC LIMIT 100"

	runs := []models.BackupRun{}
	if err := s.db.Select(&runs, query, args...); err != nil {
		log.Printf("handleListRuns: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusOK, runs)
}

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
	msg := ""
	if run.ErrorMessage != nil {
		msg = *run.ErrorMessage
	}
	respond(w, http.StatusOK, map[string]any{
		"run_id":  id,
		"status":  run.Status,
		"message": msg,
	})
}

var logsUpgrader = websocket.Upgrader{
	ReadBufferSize:  256,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func (s *Server) handleRunLogsWS(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	conn, err := logsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("handleRunLogsWS upgrade: %v", err)
		return
	}
	defer conn.Close()

	ch := s.hub.SubscribeRunLogs(id.String())
	defer s.hub.UnsubscribeRunLogs(id.String(), ch)

	// Check if run is already finished
	var status string
	if err := s.db.QueryRow(`SELECT status FROM backup_runs WHERE id=$1`, id).Scan(&status); err != nil || status != "running" {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "run not active"))
		return
	}

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				return
			}
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
