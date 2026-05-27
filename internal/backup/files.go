package backup

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

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
func BackupFiles(cfg FilesConfig, w io.Writer) (BackupResult, error) {
	if cfg.Compression == "" {
		cfg.Compression = CompressionZstd
	}

	// Count files for progress tracking
	var fileCount int
	for _, path := range cfg.Paths {
		filepath.WalkDir(path, func(_ string, d os.DirEntry, _ error) error {
			if !d.IsDir() {
				fileCount++
			}
			return nil
		})
	}

	var compTarget io.Writer
	var encBuf *bytes.Buffer

	if cfg.Encrypt && cfg.Passphrase != "" {
		encBuf = &bytes.Buffer{}
		compTarget = encBuf
	} else {
		compTarget = w
	}

	totalBytes, err := TarCompress(cfg.Paths, compTarget, cfg.Compression)
	if err != nil {
		return BackupResult{}, fmt.Errorf("compress: %w", err)
	}

	if cfg.Encrypt && cfg.Passphrase != "" {
		if err := EncryptStream(encBuf, w, cfg.Passphrase); err != nil {
			return BackupResult{}, fmt.Errorf("encrypt: %w", err)
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
