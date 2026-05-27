// internal/api/storage.go
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type createStorageRequest struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (s *Server) handleListStorage(w http.ResponseWriter, r *http.Request) {
	dests := []models.StorageDestination{}
	// Config column intentionally excluded from list (contains secrets)
	if err := s.db.Select(&dests, `SELECT id, name, type, created_at FROM storage_destinations ORDER BY created_at DESC`); err != nil {
		log.Printf("handleListStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusOK, dests)
}

func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	var req createStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" || req.Type == "" {
		respondError(w, http.StatusBadRequest, "name and type required")
		return
	}
	encConfig, err := crypto.Encrypt(s.cfg.EncryptionKey, string(req.Config))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	var dest models.StorageDestination
	err = s.db.QueryRowx(
		`INSERT INTO storage_destinations (name, type, config) VALUES ($1, $2, $3) RETURNING id, name, type, created_at`,
		req.Name, req.Type, encConfig,
	).StructScan(&dest)
	if err != nil {
		log.Printf("handleCreateStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusCreated, dest)
}

func (s *Server) handleUpdateStorage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req createStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	encConfig, err := crypto.Encrypt(s.cfg.EncryptionKey, string(req.Config))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	result, err := s.db.Exec(
		`UPDATE storage_destinations SET name=$1, type=$2, config=$3 WHERE id=$4`,
		req.Name, req.Type, encConfig, id,
	)
	if err != nil {
		log.Printf("handleUpdateStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	result, err := s.db.Exec(`DELETE FROM storage_destinations WHERE id=$1`, id)
	if err != nil {
		log.Printf("handleDeleteStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
