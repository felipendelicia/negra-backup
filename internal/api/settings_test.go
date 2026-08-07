// internal/api/settings_test.go
package api

import (
	"encoding/json"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEncryptionKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func mustRedact(t *testing.T, channel, config string) map[string]any {
	t.Helper()
	got, err := redactSecrets(channel, json.RawMessage(config))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(got, &cfg))
	return cfg
}

// The SMTP password must never reach the browser: the read path hands back a sentinel
// that the update path swaps for the stored value.
func TestRedactSecrets_Email(t *testing.T) {
	cfg := mustRedact(t, models.NotificationTypeEmail, `{"smtp_host":"h","password":"s3cr3t"}`)
	assert.Equal(t, redactedPasswordSentinel, cfg["password"])
	assert.Equal(t, "h", cfg["smtp_host"])

	raw, err := redactSecrets(models.NotificationTypeEmail,
		json.RawMessage(`{"smtp_host":"h","password":"s3cr3t"}`))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "s3cr3t")
}

func TestRedactSecrets_LeavesConfigsWithoutSecretsAlone(t *testing.T) {
	for _, in := range []string{`{"smtp_host":"h"}`, `{"smtp_host":"h","password":""}`} {
		cfg := mustRedact(t, models.NotificationTypeEmail, in)
		assert.NotEqual(t, redactedPasswordSentinel, cfg["password"])
		assert.Equal(t, "h", cfg["smtp_host"])
	}
}

func TestRedactSecrets_RejectsInvalidJSON(t *testing.T) {
	_, err := redactSecrets(models.NotificationTypeEmail, json.RawMessage(`not json`))
	assert.Error(t, err)
}

// For Slack, Discord and Telegram the URL is the credential. The page gets enough to
// recognise the endpoint and nothing that can post to it.
func TestRedactSecrets_WebhookURLIsSecretButPreviewable(t *testing.T) {
	cfg := mustRedact(t, models.NotificationTypeWebhook,
		`{"url":"https://hooks.slack.com/services/T000/B000/tOkEn","format":"slack","auth_header":"Bearer abc"}`)

	assert.Equal(t, redactedPasswordSentinel, cfg["url"])
	assert.Equal(t, redactedPasswordSentinel, cfg["auth_header"])
	assert.Equal(t, "https://hooks.slack.com/…", cfg["url_preview"])
	assert.Equal(t, "slack", cfg["format"])

	raw, err := redactSecrets(models.NotificationTypeWebhook,
		json.RawMessage(`{"url":"https://hooks.slack.com/services/T000/B000/tOkEn"}`))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "tOkEn")
	assert.NotContains(t, string(raw), "services")
}

func TestPreviewURL(t *testing.T) {
	cases := map[string]string{
		"https://hooks.slack.com/services/T/B/X": "https://hooks.slack.com/…",
		"https://ntfy.sh":                        "https://ntfy.sh",
		"https://ntfy.sh/":                       "https://ntfy.sh",
		"http://192.168.1.5:8080/hook":           "http://192.168.1.5:8080/…",
		"://nonsense":                            "(invalid URL)",
	}
	for in, want := range cases {
		assert.Equal(t, want, previewURL(in), "input %q", in)
	}
}

func TestMergeSecrets_Email(t *testing.T) {
	stored := json.RawMessage(`{"smtp_host":"old","password":"s3cr3t"}`)

	tests := []struct {
		name     string
		incoming string
		stored   json.RawMessage
		wantPw   any
	}{
		{"sentinel keeps the stored password", `{"smtp_host":"h","password":"` + redactedPasswordSentinel + `"}`, stored, "s3cr3t"},
		{"empty password keeps the stored one", `{"smtp_host":"h","password":""}`, stored, "s3cr3t"},
		{"omitted password keeps the stored one", `{"smtp_host":"h"}`, stored, "s3cr3t"},
		{"a real new password wins", `{"smtp_host":"h","password":"new"}`, stored, "new"},
		{"sentinel with nothing stored is dropped", `{"smtp_host":"h","password":"` + redactedPasswordSentinel + `"}`, nil, nil},
		{"sentinel with a stored config that has no password is dropped", `{"smtp_host":"h","password":"` + redactedPasswordSentinel + `"}`, json.RawMessage(`{"smtp_host":"old"}`), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergeSecrets(models.NotificationTypeEmail, json.RawMessage(tc.incoming), tc.stored)
			require.NoError(t, err)
			var cfg map[string]any
			require.NoError(t, json.Unmarshal(got, &cfg))
			assert.Equal(t, tc.wantPw, cfg["password"])
			assert.Equal(t, "h", cfg["smtp_host"])
		})
	}
}

// Editing the format of an existing webhook must not require re-typing the URL, which
// the operator no longer has — the page only ever showed them a preview of it.
func TestMergeSecrets_WebhookSentinelKeepsStoredURL(t *testing.T) {
	stored := json.RawMessage(`{"url":"https://hooks.slack.com/services/T/B/X","format":"slack"}`)
	incoming := json.RawMessage(`{"url":"` + redactedPasswordSentinel + `","format":"discord","url_preview":"https://hooks.slack.com/…"}`)

	got, err := mergeSecrets(models.NotificationTypeWebhook, incoming, stored)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal(got, &cfg))
	assert.Equal(t, "https://hooks.slack.com/services/T/B/X", cfg["url"])
	assert.Equal(t, "discord", cfg["format"])
	// The display-only field must not be stored, or the next read previews a preview.
	assert.NotContains(t, cfg, "url_preview")
}

func TestMergeSecrets_RejectsInvalidIncoming(t *testing.T) {
	_, err := mergeSecrets(models.NotificationTypeEmail, json.RawMessage(`not json`), nil)
	assert.Error(t, err)
}

// End to end over the pure pieces of the read/write path: what the update handler
// writes is ciphertext, and what the get handler reads back is the same config with
// the password redacted.
func TestNotificationConfig_EncryptedRoundTripKeepsPasswordOutOfTheWire(t *testing.T) {
	incoming := json.RawMessage(`{"smtp_host":"h","smtp_port":587,"password":"s3cr3t"}`)

	config, err := mergeSecrets(models.NotificationTypeEmail, incoming, nil)
	require.NoError(t, err)
	stored, err := crypto.Encrypt(testEncryptionKey, string(config))
	require.NoError(t, err)
	assert.NotContains(t, stored, "s3cr3t")

	decrypted, err := notify.DecryptSettingsConfig(testEncryptionKey, stored)
	require.NoError(t, err)
	wire, err := redactSecrets(models.NotificationTypeEmail, decrypted)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "s3cr3t")

	// The UI echoes the redacted config back; the stored password survives the save.
	saved, err := mergeSecrets(models.NotificationTypeEmail, wire, decrypted)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(saved, &cfg))
	assert.Equal(t, "s3cr3t", cfg["password"])
}

// A row written before encryption existed must still be readable, and re-saving it
// must produce ciphertext.
func TestNotificationConfig_LegacyPlaintextRowStillReadable(t *testing.T) {
	legacy := `{"smtp_host":"h","password":"s3cr3t"}`

	decrypted, err := notify.DecryptSettingsConfig(testEncryptionKey, legacy)
	require.NoError(t, err)
	assert.JSONEq(t, legacy, string(decrypted))

	wire, err := redactSecrets(models.NotificationTypeEmail, decrypted)
	require.NoError(t, err)
	saved, err := mergeSecrets(models.NotificationTypeEmail, wire, decrypted)
	require.NoError(t, err)
	stored, err := crypto.Encrypt(testEncryptionKey, string(saved))
	require.NoError(t, err)

	reread, err := notify.DecryptSettingsConfig(testEncryptionKey, stored)
	require.NoError(t, err)
	assert.JSONEq(t, legacy, string(reread))
}

func TestValidateChannel_Email(t *testing.T) {
	valid := `{"smtp_host":"h","smtp_port":587,"from":"a@b.c","to":["d@e.f"]}`
	require.NoError(t, validateChannel(models.NotificationTypeEmail, json.RawMessage(valid)))

	// Each of these used to save happily and then deliver nothing, which is the
	// failure mode the whole alerting feature exists to prevent.
	invalid := map[string]string{
		"no from":      `{"smtp_host":"h","smtp_port":587,"to":["d@e.f"]}`,
		"no to":        `{"smtp_host":"h","smtp_port":587,"from":"a@b.c","to":[]}`,
		"port zero":    `{"smtp_host":"h","smtp_port":0,"from":"a@b.c","to":["d@e.f"]}`,
		"port too big": `{"smtp_host":"h","smtp_port":70000,"from":"a@b.c","to":["d@e.f"]}`,
	}
	for name, cfg := range invalid {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateChannel(models.NotificationTypeEmail, json.RawMessage(cfg)))
		})
	}
}

func TestValidateChannel_Webhook(t *testing.T) {
	valid := map[string]string{
		"generic":                          `{"url":"https://example.com/hook","format":"generic"}`,
		"slack":                            `{"url":"https://hooks.slack.com/services/x","format":"slack"}`,
		"ntfy":                             `{"url":"http://ntfy.lan/backups","format":"ntfy"}`,
		"telegram":                         `{"url":"https://api.telegram.org/botX/sendMessage","format":"telegram","chat_id":"123"}`,
		"empty format defaults to generic": `{"url":"https://example.com/hook"}`,
	}
	for name, cfg := range valid {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, validateChannel(models.NotificationTypeWebhook, json.RawMessage(cfg)))
		})
	}

	invalid := map[string]string{
		"no url":            `{"format":"slack"}`,
		"wrong scheme":      `{"url":"ftp://example.com/hook","format":"generic"}`,
		"unknown format":    `{"url":"https://example.com/hook","format":"mattermost"}`,
		"telegram, no chat": `{"url":"https://api.telegram.org/botX/sendMessage","format":"telegram"}`,
	}
	for name, cfg := range invalid {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateChannel(models.NotificationTypeWebhook, json.RawMessage(cfg)))
		})
	}
}
