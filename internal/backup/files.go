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

	// OnProgress, if set, is called while the archive is written with a monotonically
	// increasing percent (never above 100) and the file being archived. It is
	// throttled — see progressReporter for the policy — and the last call is always
	// (100, "done"). When encryption is on it runs on the compression goroutine, so
	// implementations must be safe to call from a goroutine other than the caller's.
	OnProgress func(percent int, currentFile string)
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

	// Real progress: the counting walk above gives the denominator, and the archiving
	// walk reports the numerator (files completed plus the fraction of the file in
	// flight). reporter is nil when cfg.OnProgress is nil, which keeps TarCompress on
	// its no-progress code path.
	reporter := newProgressReporter(cfg.OnProgress, fileCount)
	var onProgress TarProgressFunc
	if reporter != nil {
		onProgress = reporter.report
	}

	var totalBytes int64
	var compErr error

	if cfg.Encrypt {
		// Pipeline: TarCompress → pipe → EncryptStream → w
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			n, err := TarCompressProgress(cfg.Paths, pw, cfg.Compression, onProgress)
			totalBytes = n
			if err != nil {
				pw.CloseWithError(err)
			} else {
				pw.Close()
			}
			errCh <- err
		}()

		if err := EncryptStream(pr, w, cfg.Passphrase); err != nil {
			// Unblock the writer side, otherwise the goroutine stays parked forever on
			// pw.Write holding a file open — and, now that it reports progress, keeps
			// calling cfg.OnProgress long after BackupFiles returned.
			pr.CloseWithError(err)
			<-errCh
			return BackupResult{}, fmt.Errorf("encrypt: %w", err)
		}
		if err := <-errCh; err != nil {
			return BackupResult{}, fmt.Errorf("compress: %w", err)
		}
	} else {
		totalBytes, compErr = TarCompressProgress(cfg.Paths, w, cfg.Compression, onProgress)
		if compErr != nil {
			return BackupResult{}, fmt.Errorf("compress: %w", compErr)
		}
	}

	// Always land on exactly 100, whatever the throttle did with the updates before.
	reporter.finish()

	return BackupResult{
		SizeBytes: totalBytes,
		FileCount: fileCount,
	}, nil
}
