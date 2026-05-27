// internal/api/settings.go
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/models"
)

func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var settings models.NotificationSettings
	err := s.db.Get(&settings, `SELECT id, type, config FROM notification_settings LIMIT 1`)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	respond(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("handleUpdateNotificationSettings begin tx: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM notification_settings`); err != nil {
		log.Printf("handleUpdateNotificationSettings delete: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if _, err := tx.Exec(`INSERT INTO notification_settings (type, config) VALUES ($1, $2)`, body.Type, body.Config); err != nil {
		log.Printf("handleUpdateNotificationSettings insert: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("handleUpdateNotificationSettings commit: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
