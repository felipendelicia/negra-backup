// internal/notify/provider.go
package notify

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/jmoiron/sqlx"
)

// legacyPlaintextOnce keeps the "your row is still in the clear" warning to a single
// line per process instead of one per failed backup.
var legacyPlaintextOnce sync.Once

// DecryptSettingsConfig returns the plaintext JSON of a notification_settings.config
// value. The column holds AES-256-GCM ciphertext (same scheme as
// storage_destinations.config), but installations that predate that change still have
// the JSON stored verbatim, so a value that fails to decrypt is accepted when it is
// valid JSON. Anything else is an error: silently forwarding garbage would surface as
// an unrelated SMTP failure much later.
func DecryptSettingsConfig(encKey, stored string) (json.RawMessage, error) {
	if stored == "" {
		return nil, errors.New("notification settings config is empty")
	}
	if encKey != "" {
		if plaintext, err := crypto.Decrypt(encKey, stored); err == nil {
			if !isJSONObject(plaintext) {
				return nil, errors.New("decrypted notification settings config is not a JSON object")
			}
			return json.RawMessage(plaintext), nil
		}
	}
	if isJSONObject(stored) {
		legacyPlaintextOnce.Do(func() {
			log.Printf("notification settings are stored unencrypted (legacy row); saving them again from the UI will encrypt them")
		})
		return json.RawMessage(stored), nil
	}
	if encKey == "" {
		return nil, errors.New("notification settings config is encrypted but no encryption key is configured")
	}
	return nil, errors.New("notification settings config could not be decrypted and is not valid JSON")
}

func isJSONObject(s string) bool {
	var obj map[string]any
	return json.Unmarshal([]byte(s), &obj) == nil
}

// DBNotifier resolves the email configuration from the database on every alert.
//
// The notifier used to be built once at startup from whatever was in the database
// at that moment. That made notifications unreachable in practice: on a fresh
// install the row did not exist yet, so the notifier stayed nil forever, and saving
// settings from the UI had no effect until someone restarted the server. Reading the
// row per alert costs one indexed query on a path that only runs when a backup has
// already failed.
type DBNotifier struct {
	db     *sqlx.DB
	encKey string
}

// NewDBNotifier builds a notifier without an encryption key. It can only read legacy
// plaintext rows; prefer NewDBNotifierWithKey so rows written by the current API
// (which encrypts) can be read back.
func NewDBNotifier(db *sqlx.DB) *DBNotifier {
	return NewDBNotifierWithKey(db, "")
}

// NewDBNotifierWithKey builds a notifier that decrypts the stored SMTP configuration
// with the server's ENCRYPTION_KEY.
func NewDBNotifierWithKey(db *sqlx.DB, encKey string) *DBNotifier {
	return &DBNotifier{db: db, encKey: encKey}
}

func (n *DBNotifier) SendFailureAlert(jobName, agentName, runID, errMsg string) error {
	if n.db == nil {
		return nil
	}
	var stored string
	err := n.db.QueryRow(
		`SELECT config FROM notification_settings WHERE type='email' LIMIT 1`).Scan(&stored)
	if err == sql.ErrNoRows {
		return nil // notifications not configured; not an error
	}
	if err != nil {
		return fmt.Errorf("load notification settings: %w", err)
	}

	raw, err := DecryptSettingsConfig(n.encKey, stored)
	if err != nil {
		return fmt.Errorf("read notification settings: %w", err)
	}

	var cfg models.EmailNotificationConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse notification settings: %w", err)
	}
	if cfg.SMTPHost == "" || len(cfg.To) == 0 {
		return nil // configured but incomplete; nothing to send to
	}

	return NewEmailSender(cfg).SendFailureAlert(jobName, agentName, runID, errMsg)
}
