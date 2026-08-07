// internal/api/settings.go
package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/jmoiron/sqlx"
)

// redactedPasswordSentinel is echoed to the UI in place of a stored secret.
// Submitting it back means "keep the existing value".
const redactedPasswordSentinel = "__stored__"

// secretFields lists, per channel, the config keys that must never leave the server.
//
// The webhook URL is in here alongside the SMTP password because for Slack, Discord
// and Telegram the URL *is* the credential: the token sits in the path, and anyone
// who reads it can post as this server forever. Returning it to the browser on every
// visit to the settings page is the same mistake the SMTP password used to make.
var secretFields = map[string][]string{
	models.NotificationTypeEmail:   {"password"},
	models.NotificationTypeWebhook: {"url", "auth_header"},
}

// requiredField is the key whose emptiness means "this channel is not configured".
// Saving a channel with it blank deletes the channel.
var requiredField = map[string]string{
	models.NotificationTypeEmail:   "smtp_host",
	models.NotificationTypeWebhook: "url",
}

// notificationSettingsBody is both the request and the response shape.
//
// A pointer per channel so "absent" and "explicitly cleared" are the same thing on
// write — the PUT replaces the entire set. An update that silently preserved omitted
// channels would make "remove my webhook" impossible to express.
type notificationSettingsBody struct {
	Email   json.RawMessage `json:"email"`
	Webhook json.RawMessage `json:"webhook"`
}

func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	out := notificationSettingsBody{}
	for _, channel := range []string{models.NotificationTypeEmail, models.NotificationTypeWebhook} {
		raw, err := s.storedChannelConfig(channel)
		if err != nil {
			log.Printf("handleGetNotificationSettings %s: %v", channel, err)
			respondError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if raw == nil {
			continue
		}
		redacted, err := redactSecrets(channel, raw)
		if err != nil {
			log.Printf("handleGetNotificationSettings redact %s: %v", channel, err)
			respondError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		switch channel {
		case models.NotificationTypeEmail:
			out.Email = redacted
		case models.NotificationTypeWebhook:
			out.Webhook = redacted
		}
	}
	respond(w, http.StatusOK, out)
}

// storedChannelConfig reads and decrypts one channel row. A missing row yields
// (nil, nil): not configured is not an error.
func (s *Server) storedChannelConfig(channel string) (json.RawMessage, error) {
	if s.db == nil {
		return nil, nil
	}
	var stored string
	err := s.db.QueryRow(
		`SELECT config FROM notification_settings WHERE type=$1 LIMIT 1`, channel).Scan(&stored)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load %s settings: %w", channel, err)
	}
	return notify.DecryptSettingsConfig(s.cfg.EncryptionKey, stored)
}

// redactSecrets replaces every secret field of a channel with the sentinel, and adds
// a non-secret preview of the webhook URL so the page can still show which endpoint
// is configured without handing over the token that reaches it.
func redactSecrets(channel string, config json.RawMessage) (json.RawMessage, error) {
	var cfg map[string]any
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}

	if channel == models.NotificationTypeWebhook {
		if raw, ok := cfg["url"].(string); ok && raw != "" {
			cfg["url_preview"] = previewURL(raw)
		}
	}

	for _, field := range secretFields[channel] {
		if v, ok := cfg[field].(string); ok && v != "" {
			cfg[field] = redactedPasswordSentinel
		}
	}
	return json.Marshal(cfg)
}

// previewURL reduces an endpoint to scheme, host and a marker, which identifies the
// service without disclosing the secret that lives in the path.
func previewURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	preview := u.Scheme + "://" + u.Host
	if u.Path != "" && u.Path != "/" {
		preview += "/…"
	}
	return preview
}

func (s *Server) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var body notificationSettingsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}

	incoming := map[string]json.RawMessage{
		models.NotificationTypeEmail:   body.Email,
		models.NotificationTypeWebhook: body.Webhook,
	}

	// Resolve every channel before writing anything, so a bad webhook URL cannot
	// leave the email channel half-saved.
	resolved := map[string]json.RawMessage{}
	for _, channel := range []string{models.NotificationTypeEmail, models.NotificationTypeWebhook} {
		config, err := s.resolveChannel(channel, incoming[channel])
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		resolved[channel] = config // nil means "delete this channel"
	}

	tx, err := s.db.Beginx()
	if err != nil {
		log.Printf("handleUpdateNotificationSettings begin tx: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback()

	for channel, config := range resolved {
		if err := s.writeChannel(tx, channel, config); err != nil {
			log.Printf("handleUpdateNotificationSettings %s: %v", channel, err)
			respondError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("handleUpdateNotificationSettings commit: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveChannel merges the submitted config with what is already stored and
// validates the result. A nil return means the channel should be removed.
func (s *Server) resolveChannel(channel string, incoming json.RawMessage) (json.RawMessage, error) {
	if len(incoming) == 0 || string(incoming) == "null" {
		return nil, nil
	}

	stored, err := s.storedChannelConfig(channel)
	if err != nil {
		// A config that cannot be read back is treated as absent: the operator is in
		// the middle of fixing it, and refusing the save would trap them.
		log.Printf("resolveChannel %s: reading stored config: %v", channel, err)
		stored = nil
	}

	merged, err := mergeSecrets(channel, incoming, stored)
	if err != nil {
		return nil, fmt.Errorf("invalid %s config", channel)
	}

	var cfg map[string]any
	if err := json.Unmarshal(merged, &cfg); err != nil {
		return nil, fmt.Errorf("invalid %s config", channel)
	}
	// The UI submits both channels on every save, so an untouched, empty form arrives
	// as an object of blank strings rather than as null. Treat it as "not configured"
	// instead of storing a row that can never deliver anything.
	if v, _ := cfg[requiredField[channel]].(string); strings.TrimSpace(v) == "" {
		return nil, nil
	}

	if err := validateChannel(channel, merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// mergeSecrets carries stored secrets into the incoming config wherever the UI sent
// the sentinel or nothing at all. When there is nothing to carry over, the sentinel is
// dropped so it never becomes the actual secret.
func mergeSecrets(channel string, incoming, stored json.RawMessage) (json.RawMessage, error) {
	var cfg map[string]any
	if err := json.Unmarshal(incoming, &cfg); err != nil {
		return nil, err
	}
	// A display-only field. Echoing it back would store it, and a later read would
	// show a preview of a preview.
	delete(cfg, "url_preview")

	var prev map[string]any
	_ = json.Unmarshal(stored, &prev) // nil stored simply yields a nil map

	for _, field := range secretFields[channel] {
		v, _ := cfg[field].(string)
		if v != "" && v != redactedPasswordSentinel {
			continue // a real new value was supplied
		}
		if storedVal, ok := prev[field].(string); ok && storedVal != "" {
			cfg[field] = storedVal
			continue
		}
		if v == redactedPasswordSentinel {
			delete(cfg, field)
		}
	}
	return json.Marshal(cfg)
}

// validateChannel rejects configurations that would fail silently at delivery time.
//
// Every check here covers a case where the service answers 200 to a request that
// posts nothing, or where the failure only surfaces on the night an alert actually
// matters — which is the one night nobody wants to discover a typo.
func validateChannel(channel string, config json.RawMessage) error {
	switch channel {
	case models.NotificationTypeEmail:
		var cfg models.EmailNotificationConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return fmt.Errorf("invalid email config")
		}
		if cfg.From == "" {
			return fmt.Errorf("email notifications need a From address")
		}
		if len(cfg.To) == 0 {
			return fmt.Errorf("email notifications need at least one To address")
		}
		if cfg.SMTPPort <= 0 || cfg.SMTPPort > 65535 {
			return fmt.Errorf("SMTP port must be between 1 and 65535")
		}

	case models.NotificationTypeWebhook:
		var cfg models.WebhookNotificationConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return fmt.Errorf("invalid webhook config")
		}
		if _, err := notify.ValidateWebhookURL(cfg.URL); err != nil {
			return err
		}
		if cfg.Format == "" {
			cfg.Format = models.WebhookFormatGeneric
		}
		if !models.ValidWebhookFormat(cfg.Format) {
			return fmt.Errorf("unknown webhook format %q; expected one of %s",
				cfg.Format, strings.Join(models.WebhookFormats, ", "))
		}
		// Telegram's sendMessage takes the destination in the payload, not the URL.
		// Without it the API answers 400 for every alert.
		if cfg.Format == models.WebhookFormatTelegram && strings.TrimSpace(cfg.ChatID) == "" {
			return fmt.Errorf("the telegram format requires a chat_id")
		}
	}
	return nil
}

// writeChannel upserts or removes one channel row. Secrets are encrypted at rest with
// the same key and scheme as storage destinations and backup passphrases.
func (s *Server) writeChannel(tx *sqlx.Tx, channel string, config json.RawMessage) error {
	if config == nil {
		_, err := tx.Exec(`DELETE FROM notification_settings WHERE type=$1`, channel)
		return err
	}
	encConfig, err := crypto.Encrypt(s.cfg.EncryptionKey, string(config))
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	_, err = tx.Exec(`
		INSERT INTO notification_settings (type, config) VALUES ($1, $2)
		ON CONFLICT (type) DO UPDATE SET config = EXCLUDED.config`, channel, encConfig)
	return err
}
