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

	destDir := filepath.Join(os.TempDir(), "nat-backup-uploads", runID.String())
	if err := os.MkdirAll(destDir, 0700); err != nil {
		log.Printf("handleUpload: mkdir: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	destPath := filepath.Join(destDir, filepath.Base(header.Filename))
	dst, err := os.Create(destPath)
	if err != nil {
		log.Printf("handleUpload: create file: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer dst.Close()

	size, err := io.Copy(dst, file)
	if err != nil {
		log.Printf("handleUpload: write file: %v", err)
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
