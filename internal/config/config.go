// internal/config/config.go
package config

import (
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
		TLSCertFile:   os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:    os.Getenv("TLS_KEY_FILE"),
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

	return cfg, nil
}
