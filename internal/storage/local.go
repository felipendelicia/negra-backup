package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalBackend writes backup files directly to a local filesystem path.
type LocalBackend struct {
	cfg LocalConfig
}

func NewLocalBackend(cfg LocalConfig) *LocalBackend {
	return &LocalBackend{cfg: cfg}
}

// resolve joins filename onto the configured path, refusing anything that would
// escape it. Job names reach this function, so a name containing ".." must not be
// able to write outside the destination.
func (b *LocalBackend) resolve(filename string) (string, error) {
	if filename == "" {
		return "", fmt.Errorf("empty filename")
	}
	clean := filepath.Clean(filepath.Join(b.cfg.Path, filename))
	base := filepath.Clean(b.cfg.Path)
	if clean != base && !strings.HasPrefix(clean, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to write outside %s", base)
	}
	return clean, nil
}

func (b *LocalBackend) Upload(filename string, r io.Reader, _ int64) error {
	destPath, err := b.resolve(filename)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.cfg.Path, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", b.cfg.Path, err)
	}

	// Write to a temp file and rename into place: a crash mid-upload would otherwise
	// leave a truncated file that is indistinguishable from a complete backup.
	tmp, err := os.CreateTemp(b.cfg.Path, ".partial-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", b.cfg.Path, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath) // no-op once the rename succeeded
	}()

	// Backups may be encrypted, but the archive itself is still sensitive.
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if _, err := io.Copy(tmp, r); err != nil {
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	// Both errors are fatal: Close flushes, and skipping the check reports a
	// truncated archive as a successful backup.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename into %s: %w", destPath, err)
	}
	return nil
}

func (b *LocalBackend) Delete(filename string) error {
	destPath, err := b.resolve(filename)
	if err != nil {
		return err
	}
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", destPath, err)
	}
	return nil
}
