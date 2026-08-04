// internal/api/settings_test.go
package api

import (
	"encoding/json"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEncryptionKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// The SMTP password must never reach the browser: the read path hands back a sentinel
// that the update path swaps for the stored value.
func TestRedactPassword(t *testing.T) {
	got := redactPassword(json.RawMessage(`{"smtp_host":"h","password":"s3cr3t"}`))
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(got, &cfg))
	assert.Equal(t, redactedPasswordSentinel, cfg["password"])
	assert.Equal(t, "h", cfg["smtp_host"])
	assert.NotContains(t, string(got), "s3cr3t")
}

func TestRedactPassword_LeavesConfigsWithoutPasswordAlone(t *testing.T) {
	for _, in := range []string{`{"smtp_host":"h"}`, `{"smtp_host":"h","password":""}`, `not json`} {
		assert.Equal(t, in, string(redactPassword(json.RawMessage(in))))
	}
}

func TestMergeStoredPassword(t *testing.T) {
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
			got, err := mergeStoredPassword(json.RawMessage(tc.incoming), tc.stored)
			require.NoError(t, err)
			var cfg map[string]any
			require.NoError(t, json.Unmarshal(got, &cfg))
			assert.Equal(t, tc.wantPw, cfg["password"])
			assert.Equal(t, "h", cfg["smtp_host"])
		})
	}
}

func TestMergeStoredPassword_RejectsInvalidIncoming(t *testing.T) {
	_, err := mergeStoredPassword(json.RawMessage(`not json`), nil)
	assert.Error(t, err)
}

// End to end over the pure pieces of the read/write path: what the update handler
// writes is ciphertext, and what the get handler reads back is the same config with
// the password redacted.
func TestNotificationConfig_EncryptedRoundTripKeepsPasswordOutOfTheWire(t *testing.T) {
	incoming := json.RawMessage(`{"smtp_host":"h","smtp_port":587,"password":"s3cr3t"}`)

	config, err := mergeStoredPassword(incoming, nil)
	require.NoError(t, err)
	stored, err := crypto.Encrypt(testEncryptionKey, string(config))
	require.NoError(t, err)
	assert.NotContains(t, stored, "s3cr3t")

	decrypted, err := notify.DecryptSettingsConfig(testEncryptionKey, stored)
	require.NoError(t, err)
	wire := redactPassword(decrypted)
	assert.NotContains(t, string(wire), "s3cr3t")

	// The UI echoes the redacted config back; the stored password survives the save.
	saved, err := mergeStoredPassword(wire, decrypted)
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

	saved, err := mergeStoredPassword(redactPassword(decrypted), decrypted)
	require.NoError(t, err)
	stored, err := crypto.Encrypt(testEncryptionKey, string(saved))
	require.NoError(t, err)

	reread, err := notify.DecryptSettingsConfig(testEncryptionKey, stored)
	require.NoError(t, err)
	assert.JSONEq(t, legacy, string(reread))
}
