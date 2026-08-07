// internal/notify/webhook_test.go
package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captured is what the fake endpoint saw.
type captured struct {
	method  string
	path    string
	headers http.Header
	body    string
}

// serveCapture stands up an endpoint that records one request and answers status.
func serveCapture(t *testing.T, status int, respBody string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.headers, got.body = r.Method, r.URL.Path, r.Header.Clone(), string(b)
		w.WriteHeader(status)
		io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func sendVia(t *testing.T, cfg models.WebhookNotificationConfig, subject, body string) error {
	t.Helper()
	return NewWebhookSender(cfg).SendAlert(subject, body)
}

func TestWebhook_GenericPayload(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL + "/hook", Format: models.WebhookFormatGeneric,
	}, "subj", "line one\nline two"))

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/hook", got.path)
	assert.Equal(t, "application/json", got.headers.Get("Content-Type"))

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.body), &payload))
	assert.Equal(t, "nat-backup", payload["source"])
	assert.Equal(t, "subj", payload["subject"])
	assert.Equal(t, "line one\nline two", payload["body"])
}

// An empty format is what an upgraded install has stored; it must behave as generic
// rather than erroring out on the first alert after the upgrade.
func TestWebhook_EmptyFormatBehavesAsGeneric(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")
	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{URL: srv.URL}, "s", "b"))
	assert.Contains(t, got.body, `"source":"nat-backup"`)
}

func TestWebhook_SlackAndDiscordShapes(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "ok")

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatSlack,
	}, "subj", "body"))
	var slack map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.body), &slack))
	assert.Equal(t, "subj\nbody", slack["text"])

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatDiscord,
	}, "subj", "body"))
	var discord map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.body), &discord))
	assert.Equal(t, "subj\nbody", discord["content"])
}

// Discord rejects anything over 2000 characters, so an alert carrying a long stack
// trace would 400 instead of arriving truncated.
func TestWebhook_DiscordTruncatesLongMessages(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatDiscord,
	}, "subj", strings.Repeat("x", 5000)))

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.body), &payload))
	assert.LessOrEqual(t, len([]rune(payload["content"])), 1900)
	assert.True(t, strings.HasSuffix(payload["content"], "…"))
}

func TestWebhook_TelegramSendsChatID(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatTelegram, ChatID: "-100123",
	}, "subj", "body"))

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.body), &payload))
	assert.Equal(t, "-100123", payload["chat_id"])
	assert.Equal(t, "subj\n\nbody", payload["text"])
}

func TestWebhook_TelegramWithoutChatIDFails(t *testing.T) {
	srv, _ := serveCapture(t, http.StatusOK, "")
	err := sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatTelegram,
	}, "s", "b")
	assert.ErrorContains(t, err, "chat_id")
}

func TestWebhook_NtfyPutsBodyInBodyAndSubjectInTitle(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL + "/backups", Format: models.WebhookFormatNtfy,
	}, "Backup FAILED", "the details"))

	assert.Equal(t, "the details", got.body)
	assert.Equal(t, "Backup FAILED", got.headers.Get("Title"))
	assert.Contains(t, got.headers.Get("Content-Type"), "text/plain")
}

// ntfy carries the subject in a header, and the subject embeds an operator-supplied
// job name. Without stripping CR/LF a job called "x\r\nX-Priority: max" would inject
// headers — the same hole the SMTP path closes.
func TestWebhook_NtfyTitleCannotInjectHeaders(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatNtfy,
	}, "job\r\nX-Injected: yes", "body"))

	assert.Equal(t, "", got.headers.Get("X-Injected"))
	assert.NotContains(t, got.headers.Get("Title"), "\n")
	assert.NotContains(t, got.headers.Get("Title"), "\r")
}

func TestWebhook_AuthHeaderIsSent(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")

	require.NoError(t, sendVia(t, models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatGeneric, AuthHeader: "Bearer tk_123",
	}, "s", "b"))

	assert.Equal(t, "Bearer tk_123", got.headers.Get("Authorization"))
}

// A 2xx is delivery; anything else must surface, because the caller uses the error to
// decide whether the alert still needs sending on the next tick.
func TestWebhook_NonSuccessStatusIsAnError(t *testing.T) {
	srv, _ := serveCapture(t, http.StatusForbidden, "invalid token")

	err := sendVia(t, models.WebhookNotificationConfig{URL: srv.URL}, "s", "b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "invalid token")
}

func TestWebhook_UnknownFormatIsAnError(t *testing.T) {
	srv, _ := serveCapture(t, http.StatusOK, "")
	err := sendVia(t, models.WebhookNotificationConfig{URL: srv.URL, Format: "mattermost"}, "s", "b")
	assert.ErrorContains(t, err, "mattermost")
}

func TestWebhook_SendFailureAlertCarriesTheRunDetails(t *testing.T) {
	srv, got := serveCapture(t, http.StatusOK, "")

	require.NoError(t, NewWebhookSender(models.WebhookNotificationConfig{
		URL: srv.URL, Format: models.WebhookFormatGeneric,
	}).SendFailureAlert("nightly", "web-01", "run-abc", "disk full"))

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.body), &payload))
	assert.Contains(t, payload["subject"], "nightly")
	assert.Contains(t, payload["body"], "web-01")
	assert.Contains(t, payload["body"], "run-abc")
	assert.Contains(t, payload["body"], "disk full")
}

func TestValidateWebhookURL(t *testing.T) {
	for _, ok := range []string{
		"https://hooks.slack.com/services/x",
		"http://ntfy.lan:8080/topic",
		// A LAN or loopback endpoint is the normal self-hosted case, not an SSRF.
		"http://127.0.0.1:2586/alerts",
		"http://192.168.1.10/hook",
	} {
		_, err := ValidateWebhookURL(ok)
		assert.NoError(t, err, "url %q", ok)
	}

	for _, bad := range []string{"", "   ", "ftp://example.com", "file:///etc/passwd", "not a url", "https://"} {
		_, err := ValidateWebhookURL(bad)
		assert.Error(t, err, "url %q", bad)
	}
}

func TestTruncateRunes_DoesNotSplitMultibyte(t *testing.T) {
	// Cutting by bytes here would produce invalid UTF-8 and a payload the service
	// rejects as malformed JSON.
	out := truncateRunes(strings.Repeat("ñ", 100), 10)
	assert.Equal(t, 10, len([]rune(out)))
	assert.True(t, strings.HasSuffix(out, "…"))
}
