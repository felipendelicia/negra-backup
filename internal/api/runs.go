// internal/api/runs.go
package api

import (
	"fmt"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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

	var runs []models.BackupRun
	if err := s.db.Select(&runs, query, args...); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
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
