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

// LocalConfig holds config for the local (server HTTP upload) backend.
type LocalConfig struct {
	ServerURL string
	APIKey    string
	RunID     string
}

// NewBackend creates a storage backend from a type + raw JSON config.
func NewBackend(destType string, rawConfig json.RawMessage, runID, serverURL, apiKey string) (Backend, error) {
	switch destType {
	case models.StorageTypeLocal:
		return NewLocalBackend(LocalConfig{
			ServerURL: serverURL,
			APIKey:    apiKey,
			RunID:     runID,
		}), nil

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
