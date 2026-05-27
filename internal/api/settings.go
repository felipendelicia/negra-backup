// internal/api/settings.go
package api

import (
	"encoding/json"
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
	// Try insert; if one already exists, delete and re-insert
	_, err := s.db.Exec(`INSERT INTO notification_settings (type, config) VALUES ($1, $2)`, body.Type, body.Config)
	if err != nil {
		s.db.Exec(`DELETE FROM notification_settings`)
		_, err = s.db.Exec(`INSERT INTO notification_settings (type, config) VALUES ($1, $2)`, body.Type, body.Config)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
