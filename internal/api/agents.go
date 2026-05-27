// internal/api/agents.go
package api

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	var agents []models.Agent
	if err := s.db.Select(&agents, `SELECT id, name, os, arch, version, last_seen, status, created_at FROM agents ORDER BY created_at DESC`); err != nil {
		log.Printf("handleListAgents: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusOK, agents)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	result, err := s.db.Exec(`DELETE FROM agents WHERE id = $1`, id)
	if err != nil {
		log.Printf("handleDeleteAgent: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "agent not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generateAPIKey creates a random 32-byte hex API key and its bcrypt hash.
func generateAPIKey() (plaintext, hash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	plaintext = hex.EncodeToString(raw)
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return
	}
	hash = string(h)
	return
}
