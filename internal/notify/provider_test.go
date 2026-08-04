// internal/notify/provider_test.go
package notify_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testEncKey  = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	otherEncKey = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
)

const smtpConfigJSON = `{"smtp_host":"smtp.example.com","smtp_port":587,"from":"backup@example.com","to":["admin@example.com"],"tls":true,"username":"u","password":"s3cr3t"}`

// The SMTP credentials are stored with the same AES-256-GCM scheme as the storage
// destinations, so what goes in must come back out byte for byte.
func TestDecryptSettingsConfig_RoundTrip(t *testing.T) {
	stored, err := crypto.Encrypt(testEncKey, smtpConfigJSON)
	require.NoError(t, err)
	require.NotContains(t, stored, "s3cr3t", "ciphertext must not leak the password")

	got, err := notify.DecryptSettingsConfig(testEncKey, stored)
	require.NoError(t, err)
	assert.JSONEq(t, smtpConfigJSON, string(got))
}

// Installations that predate encryption have the JSON sitting in the clear. Upgrading
// the binary must not break their notifications.
func TestDecryptSettingsConfig_AcceptsLegacyPlaintext(t *testing.T) {
	got, err := notify.DecryptSettingsConfig(testEncKey, smtpConfigJSON)
	require.NoError(t, err)
	assert.JSONEq(t, smtpConfigJSON, string(got))
}

func TestDecryptSettingsConfig_LegacyPlaintextWithoutKey(t *testing.T) {
	got, err := notify.DecryptSettingsConfig("", smtpConfigJSON)
	require.NoError(t, err)
	assert.JSONEq(t, smtpConfigJSON, string(got))
}

// The legacy fallback must not swallow anything that merely failed to decrypt.
func TestDecryptSettingsConfig_Rejects(t *testing.T) {
	ciphertext, err := crypto.Encrypt(testEncKey, smtpConfigJSON)
	require.NoError(t, err)

	tests := []struct {
		name   string
		encKey string
		stored string
	}{
		{"empty row", testEncKey, ""},
		{"garbage that is neither ciphertext nor JSON", testEncKey, "not-json-at-all"},
		{"valid JSON that is not an object", testEncKey, `["a","b"]`},
		{"ciphertext with the wrong key", otherEncKey, ciphertext},
		{"ciphertext with no key configured", "", ciphertext},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := notify.DecryptSettingsConfig(tc.encKey, tc.stored)
			assert.Error(t, err)
		})
	}
}

func TestDBNotifier_NilDBIsNoop(t *testing.T) {
	assert.NoError(t, notify.NewDBNotifier(nil).SendFailureAlert("job", "agent", "run", "boom"))
	assert.NoError(t, notify.NewDBNotifierWithKey(nil, testEncKey).SendFailureAlert("job", "agent", "run", "boom"))
}
