// internal/notify/email_test.go
package notify_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/stretchr/testify/assert"
)

func TestNewEmailSender(t *testing.T) {
	cfg := models.EmailNotificationConfig{
		SMTPHost: "smtp.example.com",
		SMTPPort: 587,
		From:     "backup@example.com",
		To:       []string{"admin@example.com"},
		TLS:      true,
	}
	sender := notify.NewEmailSender(cfg)
	assert.NotNil(t, sender)
}
