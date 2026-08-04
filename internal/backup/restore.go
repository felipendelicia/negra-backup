package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Errors returned by Restore. They are wrapped, so callers should use errors.Is.
var (
	// ErrEncryptedNoPassphrase means the archive is encrypted and no passphrase was given.
	ErrEncryptedNoPassphrase = errors.New("archive is encrypted but no passphrase was provided")
	// ErrUnknownFormat means the (decrypted) stream is neither zstd nor gzip.
	ErrUnknownFormat = errors.New("unrecognized archive format: expected zstd or gzip")
	// ErrUnsafePath means a tar entry tried to write outside the destination directory.
	ErrUnsafePath = errors.New("archive entry would escape the destination directory")
	// ErrEmptyArchive means the input stream carried no usable data.
	ErrEmptyArchive = errors.New("archive is empty or truncated")
)

var (
	magicZstd = []byte{0x28, 0xB5, 0x2F, 0xFD}
	magicGzip = []byte{0x1F, 0x8B}
)

// RestoreEntry describes one archive member as seen during a restore.
type RestoreEntry struct {
	Name     string
	Size     int64
	Mode     fs.FileMode
	Typeflag byte
	// Linkname is set for symlinks and hard links.
	Linkname string
}

// RestoreResult contains metadata about a completed restore.
type RestoreResult struct {
	// FileCount counts every non-directory entry restored (regular files, symlinks,
	// hard links), matching how BackupFiles counts what it archived.
	FileCount int
	DirCount  int
	// TotalBytes is the uncompressed size of the restored regular files.
	TotalBytes int64
	// Encrypted reports whether the input stream was encrypted.
	Encrypted bool
	// Compression is the detected algorithm ("zstd" or "gzip").
	Compression string
	// Skipped counts entries that were not restored (unsupported types).
	Skipped int
}

// RestoreOptions tunes a restore without changing the common-case signature.
type RestoreOptions struct {
	// DryRun lists the archive contents without creating or writing anything.
	DryRun bool
	// OnEntry, when set, is called for every archive member that passes the safety
	// checks, before it is written.
	OnEntry func(RestoreEntry)
	// Logf receives non-fatal warnings. Defaults to discarding them.
	Logf func(format string, args ...any)
}

// IsEncrypted reports whether an archive that starts with these bytes was produced
// with encryption enabled.
//
// The pipeline is tar -> zstd|gzip -> AES-256-GCM, and EncryptStream prepends a
// 32-byte random salt. So an unencrypted archive always starts with a compression
// magic number and an encrypted one starts with random bytes. Detecting by magic
// avoids trusting the ".enc" suffix of a filename an operator may have renamed.
func IsEncrypted(header []byte) bool {
	if len(header) >= 4 && bytes.Equal(header[:4], magicZstd) {
		return false
	}
	if len(header) >= 2 && bytes.Equal(header[:2], magicGzip) {
		return false
	}
	return true
}

// Restore reverses BackupFiles: it decrypts (if needed), decompresses and extracts
// the archive read from r into destDir.
//
// Encryption and compression are detected from the stream itself, so an operator
// holding only the file can recover it without knowing how it was produced. If the
// archive is encrypted, passphrase must be non-empty. If a passphrase is supplied
// for a plaintext archive it is ignored with a warning.
func Restore(r io.Reader, destDir string, passphrase string) (RestoreResult, error) {
	return RestoreWithOptions(r, destDir, passphrase, RestoreOptions{})
}

// RestoreWithOptions is Restore with dry-run and progress reporting.
func RestoreWithOptions(r io.Reader, destDir, passphrase string, opts RestoreOptions) (RestoreResult, error) {
	var res RestoreResult

	if destDir == "" {
		return res, errors.New("restore: destination directory required")
	}

	br := bufio.NewReader(r)
	head, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return res, fmt.Errorf("restore: read archive header: %w", err)
	}
	if len(head) < 2 {
		return res, fmt.Errorf("restore: %w", ErrEmptyArchive)
	}

	res.Encrypted = IsEncrypted(head)

	var src io.Reader = br
	// waitDecrypt tears down the decrypt goroutine and reports its error. It is a
	// no-op for plaintext archives.
	waitDecrypt := func() error { return nil }

	switch {
	case res.Encrypted && passphrase == "":
		return res, fmt.Errorf("restore: %w", ErrEncryptedNoPassphrase)

	case res.Encrypted:
		pr, pw := io.Pipe()
		decErrCh := make(chan error, 1)
		go func() {
			decErr := DecryptStream(br, pw, passphrase)
			if decErr != nil {
				pw.CloseWithError(decErr)
			} else {
				pw.Close()
			}
			decErrCh <- decErr
		}()
		src = pr
		waitDecrypt = func() error {
			// Unblocks the goroutine if extraction stopped early.
			pr.Close()
			return <-decErrCh
		}

	case passphrase != "":
		warnf(opts, "passphrase supplied but the archive is not encrypted; ignoring it")
	}

	extractErr := restoreDecompressed(src, destDir, opts, &res)

	if extractErr == nil && res.Encrypted {
		// Decompressors do not always consume the trailing bytes of their input.
		// Draining lets DecryptStream finish on its own instead of dying on a closed
		// pipe, which would otherwise look like a decryption failure.
		_, _ = io.Copy(io.Discard, src)
	}

	// A wrong passphrase surfaces here rather than as a corrupt-stream error
	// downstream, so it takes precedence over whatever the extractor reported.
	// io.ErrClosedPipe is only ever the consequence of an earlier failure.
	if decErr := waitDecrypt(); decErr != nil && !errors.Is(decErr, io.ErrClosedPipe) {
		return res, fmt.Errorf("restore: decrypt (wrong passphrase?): %w", decErr)
	}

	return res, extractErr
}

// restoreDecompressed detects the compression of src by magic bytes and extracts the
// tar stream underneath it.
func restoreDecompressed(src io.Reader, destDir string, opts RestoreOptions, res *RestoreResult) error {
	cbr := bufio.NewReader(src)
	head, err := cbr.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("restore: read compression header: %w", err)
	}

	var tarSrc io.Reader
	switch {
	case len(head) >= 4 && bytes.Equal(head[:4], magicZstd):
		dec, err := zstd.NewReader(cbr)
		if err != nil {
			return fmt.Errorf("restore: zstd reader: %w", err)
		}
		defer dec.Close()
		res.Compression = CompressionZstd
		tarSrc = dec

	case len(head) >= 2 && bytes.Equal(head[:2], magicGzip):
		gz, err := gzip.NewReader(cbr)
		if err != nil {
			return fmt.Errorf("restore: gzip reader: %w", err)
		}
		defer gz.Close()
		res.Compression = CompressionGzip
		tarSrc = gz

	default:
		return fmt.Errorf("restore: %w", ErrUnknownFormat)
	}

	return extractTar(tar.NewReader(tarSrc), destDir, opts, res)
}

// pendingDir records a directory whose permissions are applied after its contents,
// so a read-only or non-writable mode does not block the files inside it.
type pendingDir struct {
	path string
	mode fs.FileMode
}

func extractTar(tr *tar.Reader, destDir string, opts RestoreOptions, res *RestoreResult) error {
	root, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return fmt.Errorf("restore: resolve dest %s: %w", destDir, err)
	}
	if !opts.DryRun {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return fmt.Errorf("restore: mkdir %s: %w", root, err)
		}
		// Containment is compared on resolved paths: if destDir itself sits behind a
		// symlink, every target would otherwise look like it escapes.
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
	}

	var dirs []pendingDir

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("restore: read tar entry: %w", err)
		}

		if err := validateEntryName(hdr.Name); err != nil {
			return fmt.Errorf("restore: refusing entry %q (%v): %w", hdr.Name, err, ErrUnsafePath)
		}

		target := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if !withinRoot(root, target) {
			return fmt.Errorf("restore: refusing entry %q: %w", hdr.Name, ErrUnsafePath)
		}

		info := hdr.FileInfo()
		mode := info.Mode().Perm()

		if opts.OnEntry != nil {
			opts.OnEntry(RestoreEntry{
				Name:     hdr.Name,
				Size:     hdr.Size,
				Mode:     info.Mode(),
				Typeflag: hdr.Typeflag,
				Linkname: hdr.Linkname,
			})
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			res.DirCount++
			if opts.DryRun {
				continue
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("restore: mkdir %s: %w", target, err)
			}
			dirs = append(dirs, pendingDir{path: target, mode: mode})

		case tar.TypeReg:
			res.FileCount++
			if opts.DryRun {
				res.TotalBytes += hdr.Size
				continue
			}
			// Count the bytes actually written rather than the declared header size.
			n, err := writeRegular(tr, target, mode)
			if err != nil {
				return err
			}
			res.TotalBytes += n
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			}

		case tar.TypeSymlink:
			if hdr.Linkname == "" {
				warnf(opts, "skipping symlink %q: archive records no target", hdr.Name)
				res.Skipped++
				continue
			}
			if !linkTargetInside(root, target, hdr.Linkname) {
				return fmt.Errorf("restore: refusing symlink %q -> %q: %w", hdr.Name, hdr.Linkname, ErrUnsafePath)
			}
			res.FileCount++
			if opts.DryRun {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("restore: mkdir %s: %w", filepath.Dir(target), err)
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("restore: symlink %s: %w", target, err)
			}

		case tar.TypeLink:
			if err := validateEntryName(hdr.Linkname); err != nil {
				return fmt.Errorf("restore: refusing hard link %q -> %q (%v): %w",
					hdr.Name, hdr.Linkname, err, ErrUnsafePath)
			}
			linkSrc := filepath.Join(root, filepath.FromSlash(hdr.Linkname))
			if !withinRoot(root, linkSrc) {
				return fmt.Errorf("restore: refusing hard link %q -> %q: %w", hdr.Name, hdr.Linkname, ErrUnsafePath)
			}
			res.FileCount++
			if opts.DryRun {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("restore: mkdir %s: %w", filepath.Dir(target), err)
			}
			_ = os.Remove(target)
			if err := os.Link(linkSrc, target); err != nil {
				return fmt.Errorf("restore: hard link %s: %w", target, err)
			}

		default:
			// Devices, FIFOs and sockets are never produced by BackupFiles and
			// recreating them from an untrusted archive is not worth the risk.
			warnf(opts, "skipping unsupported entry %q (type %q)", hdr.Name, string(hdr.Typeflag))
			res.Skipped++
		}
	}

	// Deepest first, so tightening a parent never blocks a child.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].path) > len(dirs[j].path) })
	for _, d := range dirs {
		if err := os.Chmod(d.path, d.mode); err != nil {
			return fmt.Errorf("restore: chmod %s: %w", d.path, err)
		}
	}

	return nil
}

// writeRegular creates target with the archived permissions and copies the entry
// body into it.
func writeRegular(tr *tar.Reader, target string, mode fs.FileMode) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("restore: mkdir %s: %w", filepath.Dir(target), err)
	}

	// An earlier entry in the same archive may have planted a symlink here. It can
	// only point inside destDir, but replacing it keeps writes going to the file the
	// archive names rather than through the link.
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(target); err != nil {
			return 0, fmt.Errorf("restore: remove symlink %s: %w", target, err)
		}
	}

	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("restore: create %s: %w", target, err)
	}

	n, err := io.Copy(f, tr)
	if err != nil {
		f.Close()
		return n, fmt.Errorf("restore: write %s: %w", target, err)
	}
	if err := f.Close(); err != nil {
		return n, fmt.Errorf("restore: close %s: %w", target, err)
	}

	// OpenFile applies the process umask, so the archived mode has to be restored
	// explicitly or every file comes back as 0644 regardless of what was backed up.
	if err := os.Chmod(target, mode); err != nil {
		return n, fmt.Errorf("restore: chmod %s: %w", target, err)
	}

	return n, nil
}

// validateEntryName rejects archive member names that could be used to write
// outside the destination (Zip Slip).
func validateEntryName(name string) error {
	if name == "" {
		return errors.New("empty name")
	}
	if strings.ContainsRune(name, 0) {
		return errors.New("NUL byte in name")
	}

	// A tar written on Windows can use backslashes. Normalising them means "..\evil"
	// is caught by the ".." check even when restoring on Linux.
	n := strings.ReplaceAll(filepath.ToSlash(name), `\`, "/")

	if strings.HasPrefix(n, "/") {
		return errors.New("absolute path")
	}
	if len(n) >= 2 && n[1] == ':' {
		return errors.New("path with drive letter")
	}
	for _, el := range strings.Split(n, "/") {
		if el == ".." {
			return errors.New(`".." path element`)
		}
	}
	return nil
}

// withinRoot reports whether target is root itself or lives underneath it.
func withinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(os.PathSeparator))
}

// linkTargetInside resolves a symlink target the way the kernel would — relative to
// the directory holding the link — and reports whether it stays inside root.
func linkTargetInside(root, linkPath, linkname string) bool {
	t := filepath.FromSlash(linkname)
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(linkPath), t)
	}
	return withinRoot(root, filepath.Clean(t))
}

func warnf(opts RestoreOptions, format string, args ...any) {
	if opts.Logf != nil {
		opts.Logf(format, args...)
	}
}
