// internal/notify/webhook.go
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
)

// webhookTimeout bounds a single delivery attempt.
//
// Alerts are sent from the cron goroutine and the dead-man's switch tick. Neither can
// afford to block on an endpoint that accepts the connection and then never answers,
// because the thing they would be blocking is the backups themselves. A late alert is
// bad; a backup that did not run because the alert channel hung is worse.
const webhookTimeout = 10 * time.Second

// maxWebhookErrorBody caps how much of a failed response is quoted back into the log.
// Endpoints answer errors with anything from four words to an HTML page.
const maxWebhookErrorBody = 512

type WebhookSender struct {
	cfg    models.WebhookNotificationConfig
	client *http.Client
}

func NewWebhookSender(cfg models.WebhookNotificationConfig) *WebhookSender {
	return &WebhookSender{
		cfg: cfg,
		// A client per sender rather than http.DefaultClient: DefaultClient has no
		// timeout at all, and sharing it would let an unrelated caller's transport
		// settings decide how long a backup alert may hang.
		client: &http.Client{Timeout: webhookTimeout},
	}
}

func (wsnd *WebhookSender) SendFailureAlert(jobName, agentName, runID, errMsg string) error {
	subject := fmt.Sprintf("[nat-backup] Backup FAILED: %s", jobName)
	body := fmt.Sprintf(
		"Backup job failed.\n\nJob: %s\nAgent: %s\nRun ID: %s\n\nError:\n%s",
		jobName, agentName, runID, errMsg,
	)
	return wsnd.SendAlert(subject, body)
}

// SendAlert posts one message, encoded for the configured service.
func (wsnd *WebhookSender) SendAlert(subject, body string) error {
	// The deadline covers building, sending, and draining the response — cancelled
	// only once the body has been read, so a slow trickle of response bytes cannot
	// outlive it either.
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()

	req, err := wsnd.buildRequest(ctx, subject, body)
	if err != nil {
		return err
	}

	resp, err := wsnd.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook post: %w", err)
	}
	defer resp.Body.Close()

	// The body is drained even on success so the connection can be reused instead of
	// being torn down after every alert.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookErrorBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s: %s",
			resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// buildRequest encodes the alert for the configured service.
func (wsnd *WebhookSender) buildRequest(ctx context.Context, subject, body string) (*http.Request, error) {
	endpoint, err := ValidateWebhookURL(wsnd.cfg.URL)
	if err != nil {
		return nil, err
	}

	var (
		payload     []byte
		contentType = "application/json"
		extra       = map[string]string{}
	)

	switch wsnd.cfg.Format {
	case models.WebhookFormatSlack:
		payload, err = json.Marshal(map[string]string{"text": subject + "\n" + body})

	case models.WebhookFormatDiscord:
		// Discord rejects a message over 2000 characters outright, so a long error
		// would turn every alert into a 400 instead of a truncated but delivered one.
		payload, err = json.Marshal(map[string]string{
			"content": truncateRunes(subject+"\n"+body, 1900),
		})

	case models.WebhookFormatTelegram:
		if wsnd.cfg.ChatID == "" {
			return nil, fmt.Errorf("telegram webhook requires chat_id")
		}
		payload, err = json.Marshal(map[string]string{
			"chat_id": wsnd.cfg.ChatID,
			"text":    truncateRunes(subject+"\n\n"+body, 4000),
		})

	case models.WebhookFormatNtfy:
		// ntfy takes the message as the raw body and the title as a header, so the
		// subject crosses into header space. CR/LF there would let a job name inject
		// arbitrary headers — the same hole sanitizeHeader closes for SMTP.
		payload = []byte(body)
		contentType = "text/plain; charset=utf-8"
		extra["Title"] = sanitizeHeader(subject)

	case models.WebhookFormatGeneric, "":
		payload, err = json.Marshal(map[string]string{
			"source":  "nat-backup",
			"subject": subject,
			"body":    body,
		})

	default:
		return nil, fmt.Errorf("unknown webhook format %q", wsnd.cfg.Format)
	}
	if err != nil {
		return nil, fmt.Errorf("encode webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "nat-backup")
	if wsnd.cfg.AuthHeader != "" {
		req.Header.Set("Authorization", sanitizeHeader(wsnd.cfg.AuthHeader))
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	return req, nil
}

// ValidateWebhookURL parses and checks a webhook endpoint.
//
// Only http and https are accepted. Without this, a `file://` or `gopher://` URL
// reaches http.NewRequest, and more to the point an operator who pastes a malformed
// value gets a confusing transport error at 3 AM instead of a rejection at save time.
//
// Private and loopback addresses are deliberately allowed: a self-hosted install
// pointing at its own ntfy or Gotify on the LAN is the normal case here, not an SSRF
// attempt. The URL comes from an authenticated admin, who can already configure a
// storage destination pointing anywhere.
func ValidateWebhookURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("webhook URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("webhook URL must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("webhook URL has no host")
	}
	return u, nil
}

// truncateRunes cuts a string to at most n runes, never splitting one in half.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
