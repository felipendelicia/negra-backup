package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrMissingPassphrase is returned when encryption is requested without a passphrase.
var ErrMissingPassphrase = errors.New("encryption requested but passphrase is empty")

// FilesConfig holds configuration for a file backup operation.
type FilesConfig struct {
	Paths       []string
	Compression string
	Encrypt     bool
	Passphrase  string
	OnProgress  func(percent int, currentFile string)
}

// BackupResult contains metadata about a completed backup.
type BackupResult struct {
	SizeBytes int64
	FileCount int
}

// BackupFiles compresses (and optionally encrypts) the given paths and writes to w.
//
// If cfg.Encrypt is set, cfg.Passphrase must be non-empty: a missing passphrase is
// an error, never a silent fallback to plaintext. The caller names the output ".enc"
// based on cfg.Encrypt alone, so degrading here would produce a plaintext archive
// labelled as encrypted.
func BackupFiles(cfg FilesConfig, w io.Writer) (BackupResult, error) {
	if cfg.Encrypt && cfg.Passphrase == "" {
		return BackupResult{}, ErrMissingPassphrase
	}
	if cfg.Compression == "" {
		cfg.Compression = CompressionZstd
	}

	// Count files for progress tracking
	var fileCount int
	for _, path := range cfg.Paths {
		if err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				fileCount++
			}
			return nil
		}); err != nil {
			return BackupResult{}, fmt.Errorf("count files in %s: %w", path, err)
		}
	}

	var totalBytes int64
	var compErr error

	if cfg.Encrypt {
		// Pipeline: TarCompress → pipe → EncryptStream → w
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			n, err := TarCompress(cfg.Paths, pw, cfg.Compression)
			totalBytes = n
			if err != nil {
				pw.CloseWithError(err)
			} else {
				pw.Close()
			}
			errCh <- err
		}()

		if err := EncryptStream(pr, w, cfg.Passphrase); err != nil {
			return BackupResult{}, fmt.Errorf("encrypt: %w", err)
		}
		if err := <-errCh; err != nil {
			return BackupResult{}, fmt.Errorf("compress: %w", err)
		}
	} else {
		totalBytes, compErr = TarCompress(cfg.Paths, w, cfg.Compression)
		if compErr != nil {
			return BackupResult{}, fmt.Errorf("compress: %w", compErr)
		}
	}

	if cfg.OnProgress != nil {
		cfg.OnProgress(100, "done")
	}

	return BackupResult{
		SizeBytes: totalBytes,
		FileCount: fileCount,
	}, nil
}
