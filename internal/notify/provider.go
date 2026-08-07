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

// alertSender is one delivery channel. EmailSender and WebhookSender both satisfy it.
type alertSender interface {
	SendFailureAlert(jobName, agentName, runID, errMsg string) error
	SendAlert(subject, body string) error
}

// emailSender resolves the current email configuration and returns a sender for it.
//
// A nil sender with a nil error means "there is nothing configured to send with",
// which is not a failure: a fresh install has no notification row, and an
// incomplete one has nowhere to deliver to. Callers must check for nil.
func (n *DBNotifier) emailSender() (alertSender, error) {
	raw, err := n.channelConfig(models.NotificationTypeEmail)
	if err != nil || raw == nil {
		return nil, err
	}

	var cfg models.EmailNotificationConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse email notification settings: %w", err)
	}
	if cfg.SMTPHost == "" || len(cfg.To) == 0 {
		return nil, nil // configured but incomplete; nothing to send to
	}
	return NewEmailSender(cfg), nil
}

// webhookSender resolves the current webhook configuration, with the same
// nil-means-not-configured contract as emailSender.
func (n *DBNotifier) webhookSender() (alertSender, error) {
	raw, err := n.channelConfig(models.NotificationTypeWebhook)
	if err != nil || raw == nil {
		return nil, err
	}

	var cfg models.WebhookNotificationConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse webhook notification settings: %w", err)
	}
	if _, err := ValidateWebhookURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("webhook notification settings: %w", err)
	}
	return NewWebhookSender(cfg), nil
}

// channelConfig reads and decrypts one channel's stored config. A missing row is not
// an error: that channel is simply not configured.
func (n *DBNotifier) channelConfig(channelType string) (json.RawMessage, error) {
	if n.db == nil {
		return nil, nil
	}
	var stored string
	err := n.db.QueryRow(
		`SELECT config FROM notification_settings WHERE type=$1 LIMIT 1`, channelType).Scan(&stored)
	if err == sql.ErrNoRows {
		return nil, nil // channel not configured; not an error
	}
	if err != nil {
		return nil, fmt.Errorf("load %s notification settings: %w", channelType, err)
	}
	raw, err := DecryptSettingsConfig(n.encKey, stored)
	if err != nil {
		return nil, fmt.Errorf("read %s notification settings: %w", channelType, err)
	}
	return raw, nil
}

// senders returns every configured channel, plus the errors of the ones that could
// not be built.
//
// Both are returned rather than one or the other: a webhook whose URL is corrupt must
// not stop the email from going out, and it must not vanish from the logs either. The
// whole point of having two channels is that one of them failing is survivable.
func (n *DBNotifier) senders() ([]alertSender, error) {
	var (
		out  []alertSender
		errs []error
	)
	for _, resolve := range []func() (alertSender, error){n.emailSender, n.webhookSender} {
		sender, err := resolve()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if sender != nil {
			out = append(out, sender)
		}
	}
	return out, errors.Join(errs...)
}

// fanOut delivers through every configured channel and joins the failures.
//
// Delivery is attempted on all of them even after one fails: the caller is reporting
// that a backup broke, and a broken mail server is not a reason to also skip the
// webhook that would have worked.
func (n *DBNotifier) fanOut(send func(alertSender) error) error {
	senders, err := n.senders()
	errs := []error{err}
	for _, s := range senders {
		errs = append(errs, send(s))
	}
	return errors.Join(errs...)
}

func (n *DBNotifier) SendFailureAlert(jobName, agentName, runID, errMsg string) error {
	return n.fanOut(func(s alertSender) error {
		return s.SendFailureAlert(jobName, agentName, runID, errMsg)
	})
}

// SendAlert delivers a pre-composed alert through every configured channel, reading
// their configuration from the database exactly like SendFailureAlert does. It is
// what the dead-man's switch notifies through.
func (n *DBNotifier) SendAlert(subject, body string) error {
	return n.fanOut(func(s alertSender) error {
		return s.SendAlert(subject, body)
	})
}
