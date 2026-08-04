// internal/api/settings.go
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/models"
)

// redactedPasswordSentinel is echoed to the UI in place of the stored SMTP
// password. Submitting it back means "keep the existing password".
const redactedPasswordSentinel = "__stored__"

func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var settings models.NotificationSettings
	err := s.db.Get(&settings, `SELECT id, type, config FROM notification_settings LIMIT 1`)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{"configured": false})
		return
	}

	// The SMTP password used to be returned verbatim, so every visit to the
	// settings page shipped the mail credential to the browser and echoed it back
	// on save. Replace it with a sentinel; the update handler restores the stored
	// value when it sees it.
	var cfg map[string]any
	if err := json.Unmarshal(settings.Config, &cfg); err == nil {
		if pw, ok := cfg["password"].(string); ok && pw != "" {
			cfg["password"] = redactedPasswordSentinel
			if redacted, err := json.Marshal(cfg); err == nil {
				settings.Config = redacted
			}
		}
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

	// Restore the stored password when the UI sends back the redaction sentinel or
	// an empty value, so saving an unrelated field cannot silently clear it.
	config, err := s.preserveStoredPassword(body.Config)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid config")
		return
	}
	body.Config = config

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

// preserveStoredPassword swaps the redaction sentinel (or an omitted password) for
// the value already in the database.
func (s *Server) preserveStoredPassword(incoming json.RawMessage) (json.RawMessage, error) {
	var cfg map[string]any
	if err := json.Unmarshal(incoming, &cfg); err != nil {
		return nil, err
	}
	pw, _ := cfg["password"].(string)
	if pw != "" && pw != redactedPasswordSentinel {
		return incoming, nil // a real new password was supplied
	}

	var storedRaw []byte
	if err := s.db.QueryRow(
		`SELECT config FROM notification_settings LIMIT 1`).Scan(&storedRaw); err != nil {
		return incoming, nil // nothing stored yet
	}
	var stored map[string]any
	if err := json.Unmarshal(storedRaw, &stored); err != nil {
		return incoming, nil
	}
	if storedPw, ok := stored["password"].(string); ok && storedPw != "" {
		cfg["password"] = storedPw
	}
	return json.Marshal(cfg)
}
