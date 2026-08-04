package backup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"syscall"
)

// ErrMissingPassphrase is returned when encryption is requested without a passphrase.
var ErrMissingPassphrase = errors.New("encryption requested but passphrase is empty")

// ErrTooManySkipped is returned when so many files could not be read that the archive
// can no longer be trusted as a backup of the requested paths. Use errors.Is.
var ErrTooManySkipped = errors.New("too many files could not be read")

// ErrIOFailure is returned on the first hardware-level read error. Use errors.Is.
var ErrIOFailure = errors.New("I/O error reading source data")

// Tolerance policy for files that cannot be read.
//
// Skipping is only ever applied to entries that never entered the archive, and only
// "suspicious" skips count against the budget — a socket or a file deleted mid-backup
// is normal and says nothing about the health of the source.
//
// The budget is deliberately made of three numbers instead of a single ratio or a
// single count, because either one alone misbehaves at one end of the scale:
//
//   - a pure ratio fails a 5-file backup because one file was locked (20% > any sane
//     threshold), which is exactly the nightly Outlook .pst case this tolerance exists
//     for;
//   - a pure count either fails huge trees for a handful of files or lets a 50-file
//     backup lose all 50.
//
// So: always forgive the first skipSuspiciousGrace files, never forgive more than
// skipSuspiciousHardLimit, and in between allow up to skipSuspiciousRatio of the tree.
const (
	// skipSuspiciousGrace is forgiven unconditionally: a few locked or protected files
	// in a home directory or a Windows profile are the normal state of the world.
	skipSuspiciousGrace = 10
	// skipSuspiciousRatio is the share of the tree that may be missing before the run
	// is called a failure.
	skipSuspiciousRatio = 0.05
	// skipSuspiciousHardLimit caps the ratio: 5% of a 200k-file tree is 10k silently
	// missing files, which is not a backup anyone should be told succeeded.
	skipSuspiciousHardLimit = 1000
)

// Bounds on the sample of skipped paths reported back. The point is to let an operator
// see *what* went missing without dragging 50k paths through memory, the WebSocket and
// the run log. Suspicious entries get most of the room because they are the ones worth
// acting on.
const (
	maxSuspiciousSamples = 20
	maxExpectedSamples   = 5
)

// FilesConfig holds configuration for a file backup operation.
type FilesConfig struct {
	Paths []string

	// Exclude lists patterns for entries to leave out. A matching directory is pruned,
	// so nothing inside it is even visited. See exclude.go for the syntax; briefly:
	// "*.log" (any depth), "node_modules", ".cache/" (directories only),
	// "www/*/cache" (path suffix), "/var/www/*/node_modules" (absolute), "**" for any
	// number of segments. Negations are not supported.
	Exclude []string

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

// SkippedFile records one entry that was left out of the archive.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	// Suspicious separates what points at a real problem (permission denied, a locked
	// file, an unlistable directory) from what is normal to leave out (sockets, FIFOs,
	// device nodes, files deleted while the backup was running). Only suspicious skips
	// count against the failure budget.
	Suspicious bool `json:"suspicious"`
}

// BackupResult contains metadata about a completed backup.
type BackupResult struct {
	SizeBytes int64
	// FileCount counts the entries actually written to the archive.
	FileCount int
	// SkippedCount is how many entries were left out because they could not be read.
	// Excluded entries are not skips: they were never meant to be in the backup.
	SkippedCount int
	// SkippedSuspicious is the part of SkippedCount that indicates a real problem.
	SkippedSuspicious int
	// Skipped is a bounded sample of SkippedCount (suspicious entries first), never
	// the whole list.
	Skipped []SkippedFile
}

// BackupFiles compresses (and optionally encrypts) the given paths and writes to w.
//
// If cfg.Encrypt is set, cfg.Passphrase must be non-empty: a missing passphrase is
// an error, never a silent fallback to plaintext. The caller names the output ".enc"
// based on cfg.Encrypt alone, so degrading here would produce a plaintext archive
// labelled as encrypted.
//
// A file that cannot be read does not abort the backup: it is skipped and reported in
// BackupResult. That tolerance has two hard limits — an I/O error fails immediately
// (see ErrIOFailure) and too many suspicious skips fail the run (see ErrTooManySkipped)
// — because "99% of your data" must be an explicit outcome, not a silent one.
func BackupFiles(cfg FilesConfig, w io.Writer) (BackupResult, error) {
	if cfg.Encrypt && cfg.Passphrase == "" {
		return BackupResult{}, ErrMissingPassphrase
	}
	if cfg.Compression == "" {
		cfg.Compression = CompressionZstd
	}

	exclude, err := newExcludeMatcher(cfg.Exclude)
	if err != nil {
		return BackupResult{}, err
	}

	// Count files for progress tracking. It applies the same exclusions and the same
	// pruning as the archiving walk below, otherwise the denominator would include
	// everything under node_modules and the bar would crawl to 12% and jump to 100.
	fileCount, err := countArchivableFiles(cfg.Paths, exclude)
	if err != nil {
		return BackupResult{}, err
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

	// The accountant is written from the archiving goroutine and read after it is
	// known to have finished (the channel receive below, or the plain return of
	// tarCompress), so it needs no lock of its own.
	acct := &skipAccountant{total: fileCount}
	opts := tarOptions{
		exclude:    exclude,
		onProgress: onProgress,
		onSkip:     acct.record,
	}

	var st tarStats
	var compErr error

	if cfg.Encrypt {
		// Pipeline: tarCompress → pipe → EncryptStream → w
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			s, err := tarCompress(cfg.Paths, pw, cfg.Compression, opts)
			st = s
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
		st, compErr = tarCompress(cfg.Paths, w, cfg.Compression, opts)
		if compErr != nil {
			return BackupResult{}, fmt.Errorf("compress: %w", compErr)
		}
	}

	// Always land on exactly 100, whatever the throttle did with the updates before —
	// including when files were skipped and the numerator never reached the
	// denominator computed by the counting walk.
	reporter.finish()

	return BackupResult{
		SizeBytes:         st.totalBytes,
		FileCount:         st.files,
		SkippedCount:      acct.count,
		SkippedSuspicious: acct.suspicious,
		Skipped:           acct.samples(),
	}, nil
}

// countArchivableFiles is the progress denominator: the number of entries the
// archiving walk will try to write, under the same exclusions.
func countArchivableFiles(paths []string, exclude *excludeMatcher) (int, error) {
	var n int
	for _, root := range paths {
		if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				// A broken root is fatal here for the same reason it is fatal in the
				// archiving walk: an empty backup must never look like a success.
				if p == root {
					return werr
				}
				// Everything else is the archiving walk's problem — it classifies the
				// failure and decides whether the run survives. Counting an entry that
				// will not be archived would only skew the percentage.
				return nil
			}
			if exclude.match(p, d.IsDir()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			// Only what the archiver will actually write: regular files and symlinks.
			if mode := d.Type(); mode.IsRegular() || mode&fs.ModeSymlink != 0 {
				n++
			}
			return nil
		}); err != nil {
			return 0, fmt.Errorf("count files in %s: %w", root, err)
		}
	}
	return n, nil
}

// skipAccountant applies the tolerance policy to the entries the archiver could not
// read, and keeps a bounded sample of them.
type skipAccountant struct {
	// total is the expected number of archivable files, used as the denominator of the
	// ratio rule.
	total int

	count      int
	suspicious int

	suspiciousSamples []SkippedFile
	expectedSamples   []SkippedFile
}

// record is the tarOptions.onSkip hook. Returning an error aborts the backup.
func (a *skipAccountant) record(path string, err error) error {
	// An I/O error is never tolerated, not even once, and never counts against a
	// budget. EIO means the kernel could not read the block: the disk (or the network
	// share behind it) is failing, every other file in this run is suspect, and
	// carrying on would both hammer a dying device and produce an archive whose gaps
	// nobody would notice until restore day.
	if isIOError(err) {
		return fmt.Errorf("%w: %s: %v", ErrIOFailure, path, err)
	}

	a.count++

	if !isExpectedSkip(err) {
		a.suspicious++
		if len(a.suspiciousSamples) < maxSuspiciousSamples {
			a.suspiciousSamples = append(a.suspiciousSamples,
				SkippedFile{Path: path, Reason: err.Error(), Suspicious: true})
		}
		if a.exceeded() {
			return fmt.Errorf("%w: %d of %d files unreadable (last: %s: %v)",
				ErrTooManySkipped, a.suspicious, a.total, path, err)
		}
		return nil
	}

	if len(a.expectedSamples) < maxExpectedSamples {
		a.expectedSamples = append(a.expectedSamples,
			SkippedFile{Path: path, Reason: err.Error()})
	}
	return nil
}

// exceeded applies the grace / ratio / hard-limit rule documented above.
func (a *skipAccountant) exceeded() bool {
	if a.suspicious <= skipSuspiciousGrace {
		return false
	}
	if a.suspicious > skipSuspiciousHardLimit {
		return true
	}
	return a.total > 0 && float64(a.suspicious) > skipSuspiciousRatio*float64(a.total)
}

// samples returns the bounded report, suspicious entries first.
func (a *skipAccountant) samples() []SkippedFile {
	if len(a.suspiciousSamples)+len(a.expectedSamples) == 0 {
		return nil
	}
	out := make([]SkippedFile, 0, len(a.suspiciousSamples)+len(a.expectedSamples))
	out = append(out, a.suspiciousSamples...)
	out = append(out, a.expectedSamples...)
	return out
}

// isExpectedSkip reports whether leaving this entry out says nothing about the health
// of the source: it had no content to archive, or it was gone by the time we looked.
func isExpectedSkip(err error) bool {
	return errors.Is(err, errIrregularFile) || errors.Is(err, fs.ErrNotExist)
}

// isIOError reports a hardware/transport level read failure.
//
// Permission denied, sharing violations and the like are policy: annoying, tolerable,
// and counted against the budget. EIO is not policy — it is the storage layer telling
// us it could not deliver the bytes.
func isIOError(err error) bool {
	return errors.Is(err, syscall.EIO)
}
