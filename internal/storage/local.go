package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalBackend writes backup files directly to a local filesystem path.
type LocalBackend struct {
	cfg LocalConfig
}

func NewLocalBackend(cfg LocalConfig) *LocalBackend {
	return &LocalBackend{cfg: cfg}
}

func (b *LocalBackend) Upload(filename string, r io.Reader, _ int64) error {
	if err := os.MkdirAll(b.cfg.Path, 0750); err != nil {
		return fmt.Errorf("mkdir %s: %w", b.cfg.Path, err)
	}

	destPath := filepath.Join(b.cfg.Path, filename)
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", destPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}

	return nil
}
