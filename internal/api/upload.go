// internal/api/upload.go
package api

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxUploadMemory = 32 << 20

// maxUploadBytes caps a single agent upload. The endpoint previously accepted an
// unbounded body straight to disk.
const maxUploadBytes = 100 << 30 // 100 GiB

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid run_id")
		return
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM backup_runs WHERE id=$1`, runID).Scan(&status); err != nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	if status != "running" {
		respondError(w, http.StatusConflict, "run is not in running state")
		return
	}

	// Verify the run's job belongs to the authenticated agent
	authedAgentID, _ := r.Context().Value(contextKeyAgentID).(string)
	var jobAgentID string
	if err := s.db.QueryRow(`
		SELECT bj.agent_id FROM backup_runs br
		JOIN backup_jobs bj ON bj.id = br.job_id
		WHERE br.id = $1`, runID).Scan(&jobAgentID); err != nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	if jobAgentID != authedAgentID {
		respondError(w, http.StatusForbidden, "forbidden")
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

	// Uploads used to land in os.TempDir() while being recorded as the run's
	// permanent storage_path — so the OS could reap the only copy of a backup that
	// the database still claimed existed. UPLOAD_DIR defaults to ./data/uploads.
	destDir := filepath.Join(s.cfg.UploadDir, runID.String())
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		log.Printf("handleUpload: mkdir: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	destPath := filepath.Join(destDir, filepath.Base(header.Filename))
	dst, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		log.Printf("handleUpload: create file: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer dst.Close()

	// Cap the write so a single agent cannot fill the server's disk.
	size, err := io.Copy(dst, io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		log.Printf("handleUpload: write file: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if size > maxUploadBytes {
		os.Remove(destPath)
		respondError(w, http.StatusRequestEntityTooLarge, "upload exceeds maximum size")
		return
	}
	// Close flushes; skipping the check records a truncated archive as a success.
	if err := dst.Close(); err != nil {
		log.Printf("handleUpload: close file: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	_, err = s.db.Exec(
		`UPDATE backup_runs SET size_bytes=COALESCE(size_bytes,0)+$1, storage_path=$2 WHERE id=$3`,
		size, fmt.Sprintf("local://%s", destPath), runID,
	)
	if err != nil {
		log.Printf("handleUpload: db update: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusOK, map[string]any{"bytes_received": size})
}
