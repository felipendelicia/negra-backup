package storage

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/felipendelicia/nat-backup/internal/models"
)

// Backend is the interface all storage backends implement.
type Backend interface {
	Upload(filename string, r io.Reader, size int64) error
}

// LocalConfig holds config for the local filesystem backend.
type LocalConfig struct {
	Path string
}

// NewBackend creates a storage backend from a type + raw JSON config.
func NewBackend(destType string, rawConfig json.RawMessage, runID, serverURL, apiKey string) (Backend, error) {
	switch destType {
	case models.StorageTypeLocal:
		var cfg models.LocalStorageConfig
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return nil, fmt.Errorf("local config: %w", err)
		}
		if cfg.Path == "" {
			return nil, fmt.Errorf("local storage: path required")
		}
		return NewLocalBackend(LocalConfig{Path: cfg.Path}), nil

	case models.StorageTypeS3:
		var cfg models.S3StorageConfig
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return nil, fmt.Errorf("s3 config: %w", err)
		}
		return NewS3Backend(cfg)

	case models.StorageTypeSFTP:
		var cfg models.SFTPStorageConfig
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return nil, fmt.Errorf("sftp config: %w", err)
		}
		return NewSFTPBackend(cfg)

	default:
		return nil, fmt.Errorf("unknown storage type: %s", destType)
	}
}
