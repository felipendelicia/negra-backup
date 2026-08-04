// internal/notify/provider.go
package notify

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/jmoiron/sqlx"
)

// DBNotifier resolves the email configuration from the database on every alert.
//
// The notifier used to be built once at startup from whatever was in the database
// at that moment. That made notifications unreachable in practice: on a fresh
// install the row did not exist yet, so the notifier stayed nil forever, and saving
// settings from the UI had no effect until someone restarted the server. Reading the
// row per alert costs one indexed query on a path that only runs when a backup has
// already failed.
type DBNotifier struct {
	db *sqlx.DB
}

func NewDBNotifier(db *sqlx.DB) *DBNotifier {
	return &DBNotifier{db: db}
}

func (n *DBNotifier) SendFailureAlert(jobName, agentName, runID, errMsg string) error {
	if n.db == nil {
		return nil
	}
	var raw []byte
	err := n.db.QueryRow(
		`SELECT config FROM notification_settings WHERE type='email' LIMIT 1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil // notifications not configured; not an error
	}
	if err != nil {
		return fmt.Errorf("load notification settings: %w", err)
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
