// internal/config/config.go
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL   string
	JWTSecret     string
	EncryptionKey string
	AdminPassword string
	Port          string
	TLSEnabled    bool
	TLSCertFile   string
	TLSKeyFile    string
	UploadDir     string

	// MetricsToken is the bearer token a Prometheus scraper must present on
	// /metrics. Empty disables the endpoint entirely rather than serving it open:
	// the exposition names every job, every agent and when each last succeeded,
	// which is a map of what to attack and when nobody is watching. One knob, and
	// the safe value is the one you get by doing nothing.
	MetricsToken string
}

func Load() (Config, error) {
	var missing []string

	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	getOr := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		DatabaseURL:   get("DATABASE_URL"),
		JWTSecret:     get("JWT_SECRET"),
		EncryptionKey: get("ENCRYPTION_KEY"),
		AdminPassword: get("ADMIN_PASSWORD"),
		Port:          getOr("PORT", "8080"),
		UploadDir:     getOr("UPLOAD_DIR", "./data/uploads"),
		TLSCertFile:   os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:    os.Getenv("TLS_KEY_FILE"),
		MetricsToken:  os.Getenv("METRICS_TOKEN"),
	}

	if v := os.Getenv("TLS_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid TLS_ENABLED: %w", err)
		}
		cfg.TLSEnabled = b
	}

	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}

	// Validate the key here rather than letting the server boot and fail later on
	// the first job dispatch, when the operator is no longer watching the logs.
	// crypto expects a 64-char hex string decoding to a 32-byte AES-256 key.
	if len(cfg.EncryptionKey) != 64 {
		return Config{}, fmt.Errorf(
			"ENCRYPTION_KEY must be 64 hex characters (32 bytes), got %d", len(cfg.EncryptionKey))
	}
	if _, err := hex.DecodeString(cfg.EncryptionKey); err != nil {
		return Config{}, fmt.Errorf("ENCRYPTION_KEY must be valid hex: %w", err)
	}

	if cfg.TLSEnabled && (cfg.TLSCertFile == "" || cfg.TLSKeyFile == "") {
		return Config{}, fmt.Errorf("TLS_ENABLED requires TLS_CERT_FILE and TLS_KEY_FILE")
	}

	return cfg, nil
}
