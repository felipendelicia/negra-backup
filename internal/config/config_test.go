// internal/config/config_test.go
package config_test

import (
	"os"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A syntactically valid AES-256 key: 64 hex chars decoding to 32 bytes.
//
// Deliberately an obvious dummy. Reusing a key that was ever real — even one already
// known to be compromised — spreads it into more files and outlives any attempt to
// purge it from history.
const testEncryptionKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func setEnv(t *testing.T, pairs ...string) func() {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		os.Setenv(pairs[i], pairs[i+1])
	}
	return func() {
		for i := 0; i < len(pairs); i += 2 {
			os.Unsetenv(pairs[i])
		}
	}
}

func TestLoad_AllRequiredPresent(t *testing.T) {
	cleanup := setEnv(t,
		"DATABASE_URL", "postgres://test",
		"JWT_SECRET", "secret",
		"ENCRYPTION_KEY", testEncryptionKey,
		"ADMIN_PASSWORD", "admin",
	)
	defer cleanup()

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "postgres://test", cfg.DatabaseURL)
	assert.Equal(t, "secret", cfg.JWTSecret)
	assert.Equal(t, testEncryptionKey, cfg.EncryptionKey)
	assert.Equal(t, "8080", cfg.Port)
	assert.False(t, cfg.TLSEnabled)
}

func TestLoad_MissingRequired(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("JWT_SECRET")
	os.Unsetenv("ENCRYPTION_KEY")
	os.Unsetenv("ADMIN_PASSWORD")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestLoad_TLSEnabled(t *testing.T) {
	cleanup := setEnv(t,
		"DATABASE_URL", "postgres://test",
		"JWT_SECRET", "secret",
		"ENCRYPTION_KEY", testEncryptionKey,
		"ADMIN_PASSWORD", "admin",
		"TLS_ENABLED", "true",
		"TLS_CERT_FILE", "/certs/cert.pem",
		"TLS_KEY_FILE", "/certs/key.pem",
	)
	defer cleanup()

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.True(t, cfg.TLSEnabled)
	assert.Equal(t, "/certs/cert.pem", cfg.TLSCertFile)
	assert.Equal(t, "/certs/key.pem", cfg.TLSKeyFile)
}

// A malformed ENCRYPTION_KEY must stop the server at startup. It used to boot fine
// and only fail later, on the first job dispatch, long after anyone was watching.
func TestLoad_RejectsMalformedEncryptionKey(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"too short", "aabbccdd"},
		{"not hex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"64 bytes raw instead of hex", "0123456789012345678901234567890123456789012345678901234567890123x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanup := setEnv(t,
				"DATABASE_URL", "postgres://test",
				"JWT_SECRET", "secret",
				"ENCRYPTION_KEY", tc.key,
				"ADMIN_PASSWORD", "admin",
			)
			defer cleanup()

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "ENCRYPTION_KEY")
		})
	}
}

// TLS_ENABLED without cert paths used to reach log.Fatalf inside the goroutine
// serving traffic, after the process had already reported itself as started.
func TestLoad_TLSEnabledRequiresCertPaths(t *testing.T) {
	cleanup := setEnv(t,
		"DATABASE_URL", "postgres://test",
		"JWT_SECRET", "secret",
		"ENCRYPTION_KEY", testEncryptionKey,
		"ADMIN_PASSWORD", "admin",
		"TLS_ENABLED", "true",
	)
	defer cleanup()

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS_CERT_FILE")
}
