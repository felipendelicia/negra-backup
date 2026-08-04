// internal/api/settings.go
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/google/uuid"
)

// redactedPasswordSentinel is echoed to the UI in place of the stored SMTP
// password. Submitting it back means "keep the existing password".
const redactedPasswordSentinel = "__stored__"

func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var row struct {
		ID     uuid.UUID `db:"id"`
		Type   string    `db:"type"`
		Config string    `db:"config"`
	}
	if err := s.db.Get(&row, `SELECT id, type, config FROM notification_settings LIMIT 1`); err != nil {
		respond(w, http.StatusOK, map[string]any{"configured": false})
		return
	}

	// The config is stored encrypted (with a fallback for rows written before that
	// was true), so it has to be decrypted before anything can be read out of it.
	config, err := notify.DecryptSettingsConfig(s.cfg.EncryptionKey, row.Config)
	if err != nil {
		log.Printf("handleGetNotificationSettings: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	respond(w, http.StatusOK, models.NotificationSettings{
		ID:     row.ID,
		Type:   row.Type,
		Config: redactPassword(config),
	})
}

// redactPassword replaces the stored SMTP password with a sentinel. The password used
// to be returned verbatim, so every visit to the settings page shipped the mail
// credential to the browser and echoed it back on save; the update handler restores
// the stored value when it sees the sentinel.
func redactPassword(config json.RawMessage) json.RawMessage {
	var cfg map[string]any
	if err := json.Unmarshal(config, &cfg); err != nil {
		return config
	}
	pw, ok := cfg["password"].(string)
	if !ok || pw == "" {
		return config
	}
	cfg["password"] = redactedPasswordSentinel
	redacted, err := json.Marshal(cfg)
	if err != nil {
		return config
	}
	return redacted
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

	// SMTP credentials get the same treatment as storage credentials and backup
	// passphrases: AES-256-GCM at rest, never plaintext in the database.
	encConfig, err := crypto.Encrypt(s.cfg.EncryptionKey, string(config))
	if err != nil {
		log.Printf("handleUpdateNotificationSettings encrypt: %v", err)
		respondError(w, http.StatusInternalServerError, "encryption failed")
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
	if _, err := tx.Exec(`INSERT INTO notification_settings (type, config) VALUES ($1, $2)`, body.Type, encConfig); err != nil {
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
	var storedRaw string
	if err := s.db.QueryRow(
		`SELECT config FROM notification_settings LIMIT 1`).Scan(&storedRaw); err != nil {
		return mergeStoredPassword(incoming, nil) // nothing stored yet
	}
	stored, err := notify.DecryptSettingsConfig(s.cfg.EncryptionKey, storedRaw)
	if err != nil {
		log.Printf("preserveStoredPassword: %v", err)
		return mergeStoredPassword(incoming, nil)
	}
	return mergeStoredPassword(incoming, stored)
}

// mergeStoredPassword carries the previously stored password into the incoming config
// when the UI sent the sentinel or no password at all. When there is nothing to carry
// over, the sentinel is dropped so it never becomes the actual SMTP password.
func mergeStoredPassword(incoming, stored json.RawMessage) (json.RawMessage, error) {
	var cfg map[string]any
	if err := json.Unmarshal(incoming, &cfg); err != nil {
		return nil, err
	}
	pw, _ := cfg["password"].(string)
	if pw != "" && pw != redactedPasswordSentinel {
		return incoming, nil // a real new password was supplied
	}

	var prev map[string]any
	if err := json.Unmarshal(stored, &prev); err == nil {
		if storedPw, ok := prev["password"].(string); ok && storedPw != "" {
			cfg["password"] = storedPw
			return json.Marshal(cfg)
		}
	}
	if pw == redactedPasswordSentinel {
		delete(cfg, "password")
		return json.Marshal(cfg)
	}
	return incoming, nil
}
