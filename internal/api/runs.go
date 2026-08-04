// internal/api/runs.go
package api

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	// Fetch run + agent_id via job
	var run models.BackupRun
	if err := s.db.Get(&run, `SELECT * FROM backup_runs WHERE id=$1`, id); err != nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	if run.Status != "running" {
		respondError(w, http.StatusConflict, "run is not active")
		return
	}

	// Mark cancelled in DB
	if _, err := s.db.Exec(
		`UPDATE backup_runs SET status='cancelled', finished_at=NOW(), error_message='Cancelled by user' WHERE id=$1`,
		id,
	); err != nil {
		log.Printf("handleCancelRun: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Send cancel signal to agent (best-effort)
	if s.hub != nil {
		var agentID string
		if err := s.db.QueryRow(
			`SELECT agent_id FROM backup_jobs WHERE id=$1`, run.JobID,
		).Scan(&agentID); err == nil {
			s.hub.CancelJob(agentID, id.String())
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

const (
	defaultRunsLimit = 50
	maxRunsLimit     = 500
)

// parsePagination reads limit/offset, clamping both. An unbounded limit would let
// one request pull the entire run history into memory.
func parsePagination(q url.Values) (limit, offset int) {
	limit = defaultRunsLimit
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, maxRunsLimit)
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}

// handleListRuns returns a page of runs plus the total matching the filters.
//
// It used to return a bare array capped at a hard-coded LIMIT 100 with no offset
// and no total, so the UI could neither page through history nor tell how much of
// it was being shown — which is why the dashboard counted "all time" over at most
// 100 rows.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := " WHERE 1=1"
	var args []any
	i := 1

	if jobID := q.Get("job_id"); jobID != "" {
		id, err := uuid.Parse(jobID)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid job_id")
			return
		}
		where += fmt.Sprintf(" AND job_id=$%d", i)
		args = append(args, id)
		i++
	}
	if status := q.Get("status"); status != "" {
		if !isValidRunStatus(status) {
			respondError(w, http.StatusBadRequest, "invalid status")
			return
		}
		where += fmt.Sprintf(" AND status=$%d", i)
		args = append(args, status)
		i++
	}

	var total int
	if err := s.db.Get(&total, `SELECT COUNT(*) FROM backup_runs`+where, args...); err != nil {
		log.Printf("handleListRuns count: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	limit, offset := parsePagination(q)
	query := `SELECT id, job_id, started_at, finished_at, status, size_bytes, file_count,
	                 error_message, storage_path, checksum
	          FROM backup_runs` + where +
		fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d OFFSET $%d", i, i+1)
	args = append(args, limit, offset)

	runs := []models.BackupRun{}
	if err := s.db.Select(&runs, query, args...); err != nil {
		log.Printf("handleListRuns: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	respond(w, http.StatusOK, map[string]any{
		"runs":   runs,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// isValidRunStatus guards the status filter. The column has no CHECK constraint, so
// an unknown value would silently return an empty page instead of an error.
func isValidRunStatus(status string) bool {
	switch status {
	case models.RunStatusRunning, models.RunStatusSuccess,
		models.RunStatusFailed, models.RunStatusCancelled:
		return true
	}
	return false
}

// handleRunStats returns aggregate counts computed in the database.
//
// The dashboard previously derived its totals from whatever page of runs it happened
// to have fetched, so "all time" meant "the last 100 rows" and the weekly chart
// silently truncated once a week held more than that.
func (s *Server) handleRunStats(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 {
		days = min(v, 365)
	}

	var totals struct {
		Total     int `db:"total"     json:"total"`
		Success   int `db:"success"   json:"success"`
		Failed    int `db:"failed"    json:"failed"`
		Running   int `db:"running"   json:"running"`
		Cancelled int `db:"cancelled" json:"cancelled"`
	}
	if err := s.db.Get(&totals, `
		SELECT COUNT(*)                                            AS total,
		       COUNT(*) FILTER (WHERE status='success')            AS success,
		       COUNT(*) FILTER (WHERE status='failed')             AS failed,
		       COUNT(*) FILTER (WHERE status='running')            AS running,
		       COUNT(*) FILTER (WHERE status='cancelled')          AS cancelled
		FROM backup_runs`); err != nil {
		log.Printf("handleRunStats totals: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Buckets are generated rather than derived from the rows, so days with no runs
	// still appear and the chart keeps a constant width.
	type dayBucket struct {
		Date    string `db:"date"    json:"date"`
		Success int    `db:"success" json:"success"`
		Failed  int    `db:"failed"  json:"failed"`
	}
	daily := []dayBucket{}
	if err := s.db.Select(&daily, `
		SELECT to_char(d.day, 'YYYY-MM-DD')                                  AS date,
		       COUNT(br.id) FILTER (WHERE br.status='success')               AS success,
		       COUNT(br.id) FILTER (WHERE br.status='failed')                AS failed
		FROM generate_series(CURRENT_DATE - ($1::int - 1), CURRENT_DATE, '1 day') AS d(day)
		LEFT JOIN backup_runs br ON br.started_at::date = d.day
		GROUP BY d.day
		ORDER BY d.day`, days); err != nil {
		log.Printf("handleRunStats daily: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	respond(w, http.StatusOK, map[string]any{
		"totals": totals,
		"daily":  daily,
		"days":   days,
	})
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

	// Stored log lines, in the order they were produced. This endpoint used to
	// return status and error only, which is why the UI synthesised a handful of
	// cosmetic lines for finished runs.
	type logLine struct {
		LoggedAt time.Time `db:"logged_at" json:"logged_at"`
		Line     string    `db:"line"      json:"line"`
	}
	lines := []logLine{}
	if err := s.db.Select(&lines,
		`SELECT logged_at, line FROM run_logs WHERE run_id=$1 ORDER BY id`, id); err != nil {
		// The run itself is still worth returning if its log cannot be read.
		log.Printf("handleRunLogs: load lines for %s: %v", id, err)
	}

	respond(w, http.StatusOK, map[string]any{
		"run_id":  id,
		"status":  run.Status,
		"message": msg,
		"lines":   lines,
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
	// Read-only for the client; bound anything it sends.
	conn.SetReadLimit(4096)

	ch := s.hub.SubscribeRunLogs(id.String())
	defer s.hub.UnsubscribeRunLogs(id.String(), ch)

	// The run must exist, but it does not have to be running. This used to close the
	// socket for any finished run, which made the stream useless for the one thing
	// that happens after a run finishes: restoring from it. Restore progress is
	// published on this run's channel, and only a successful — therefore finished —
	// run can be restored, so refusing finished runs closed the only channel the
	// restore had to report on.
	var status string
	if err := s.db.QueryRow(`SELECT status FROM backup_runs WHERE id=$1`, id).Scan(&status); err != nil {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "run not found"))
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
