// internal/config/config_test.go
package config_test

import (
	"os"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		"ENCRYPTION_KEY", "aabbccdd",
		"ADMIN_PASSWORD", "admin",
	)
	defer cleanup()

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "postgres://test", cfg.DatabaseURL)
	assert.Equal(t, "secret", cfg.JWTSecret)
	assert.Equal(t, "aabbccdd", cfg.EncryptionKey)
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
		"ENCRYPTION_KEY", "key",
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
