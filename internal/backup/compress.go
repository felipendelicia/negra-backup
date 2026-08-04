package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

const (
	CompressionZstd = "zstd"
	CompressionGzip = "gzip"
)

// errIrregularFile marks entries that simply have no content to archive: sockets,
// FIFOs, device nodes. They are never an error — opening a FIFO would block the whole
// backup forever waiting for a writer — but they are reported so the caller can say
// what was left out.
var errIrregularFile = errors.New("not a regular file (socket, FIFO or device node)")

// tarOptions is the internal knob set of the archiver. It stays unexported so the two
// published entry points (TarCompress / TarCompressProgress) keep their signatures.
type tarOptions struct {
	// exclude drops entries from the archive. A matching directory is pruned: the walk
	// never descends into it, which is the whole reason exclusions pay off on trees
	// like node_modules.
	exclude *excludeMatcher

	// onProgress receives raw, unthrottled progress reports.
	onProgress TarProgressFunc

	// onSkip is consulted when an entry cannot be archived and nothing of it has
	// reached the stream yet. Returning nil drops the entry and continues; returning an
	// error aborts the archive. A nil onSkip means "every error is fatal", which is the
	// behaviour of the exported functions.
	//
	// It is deliberately not consulted once a header has been written: from that point
	// the entry is declared in the tar stream and cannot be un-declared, so a partially
	// copied file would restore silently truncated. Those failures are always fatal.
	onSkip func(path string, err error) error
}

// tarStats is what the walk accumulates.
type tarStats struct {
	// totalBytes is the uncompressed payload written into the archive.
	totalBytes int64
	// files counts non-directory entries actually written (regular files and symlinks),
	// matching how Restore counts what it extracts.
	files int
}

// TarCompress creates a tar archive of all paths, compressed with the given algorithm.
// Writes to w. Returns total uncompressed bytes read and any error.
func TarCompress(paths []string, w io.Writer, compression string) (totalBytes int64, err error) {
	return TarCompressProgress(paths, w, compression, nil)
}

// TarCompressProgress is TarCompress with progress reporting. The walk is the only
// place that knows which file is being written and how many of its bytes have gone
// through, so onProgress is fed from here; it is called once per write chunk of the
// file in flight and once when that file completes. Throttling is the caller's job
// (see progressReporter).
//
// A nil onProgress restores the exact original code path: no closure, no wrapper
// writer, no per-chunk call.
func TarCompressProgress(paths []string, w io.Writer, compression string, onProgress TarProgressFunc) (totalBytes int64, err error) {
	st, err := tarCompress(paths, w, compression, tarOptions{onProgress: onProgress})
	return st.totalBytes, err
}

// tarCompress is the real archiver behind both exported functions.
func tarCompress(paths []string, w io.Writer, compression string, opts tarOptions) (tarStats, error) {
	var st tarStats

	var compWriter io.WriteCloser
	switch compression {
	case CompressionZstd:
		enc, e := zstd.NewWriter(w)
		if e != nil {
			return st, fmt.Errorf("zstd writer: %w", e)
		}
		compWriter = enc
	case CompressionGzip:
		compWriter = gzip.NewWriter(w)
	default:
		return st, fmt.Errorf("unknown compression: %s", compression)
	}

	tw := tar.NewWriter(compWriter)

	for _, root := range paths {
		if walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			return archiveEntry(tw, root, p, d, werr, &st, opts)
		}); walkErr != nil {
			// Close on error (best-effort)
			tw.Close()
			compWriter.Close()
			return st, fmt.Errorf("walk %s: %w", root, walkErr)
		}
	}

	// Close in order: tar first, then compressor (flushes trailer)
	if closeErr := tw.Close(); closeErr != nil {
		compWriter.Close()
		return st, fmt.Errorf("tar close: %w", closeErr)
	}
	if closeErr := compWriter.Close(); closeErr != nil {
		return st, fmt.Errorf("compress close: %w", closeErr)
	}

	return st, nil
}

// archiveEntry handles one entry of the walk: it decides between excluding, skipping
// and archiving, and is the only place that turns a filesystem error into either a
// skip or a fatal error.
func archiveEntry(tw *tar.Writer, root, p string, d fs.DirEntry, werr error, st *tarStats, opts tarOptions) error {
	if werr != nil {
		// A failure on the root itself is never tolerated. A mistyped path, an
		// unmounted volume or a disconnected share would otherwise produce a perfectly
		// "successful" empty backup, which is the worst possible outcome for a backup
		// tool: the operator only finds out on restore day.
		if p == root {
			return werr
		}
		// Anything below the root is a candidate for tolerance. For a directory this
		// error comes from ReadDir, so the directory itself is already archived and
		// only its contents are missing — one skip covers the whole subtree.
		return skipEntry(opts, p, werr)
	}

	isDir := d.IsDir()

	if opts.exclude.match(p, isDir) {
		if isDir {
			// Prune. This is the difference between reading 40k node_modules entries
			// and reading none of them.
			return fs.SkipDir
		}
		// fs.SkipDir on a file would skip the rest of the containing directory too.
		return nil
	}

	info, err := d.Info()
	if err != nil {
		// The entry vanished between the directory listing and the stat.
		if skipErr := skipEntry(opts, p, err); skipErr != nil {
			return skipErr
		}
		if isDir {
			return fs.SkipDir
		}
		return nil
	}

	if isDir {
		return writeTarHeader(tw, root, p, info, "")
	}

	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return archiveSymlink(tw, root, p, info, st, opts)
	case !info.Mode().IsRegular():
		// Sockets, FIFOs and devices carry nothing restorable (and Restore refuses to
		// recreate them), so they are dropped rather than treated as a failure.
		return skipEntry(opts, p, errIrregularFile)
	default:
		return archiveRegular(tw, root, p, info, st, opts)
	}
}

// archiveSymlink stores the link itself rather than what it points at.
//
// Following links would duplicate data and, worse, loop on a cycle. The previous
// implementation wrote a symlink header (which carries no target and declares zero
// bytes) and then tried to copy the target's contents into it, so every file backup
// containing a single symlink died with "archive/tar: write too long".
func archiveSymlink(tw *tar.Writer, root, p string, info fs.FileInfo, st *tarStats, opts tarOptions) error {
	target, err := os.Readlink(p)
	if err != nil {
		return skipEntry(opts, p, err)
	}
	if err := writeTarHeader(tw, root, p, info, target); err != nil {
		return err
	}
	st.files++
	reportFileDone(opts, p, st)
	return nil
}

// archiveRegular opens the file before declaring it in the archive, so a file that
// cannot be read is dropped while the stream is still consistent.
func archiveRegular(tw *tar.Writer, root, p string, info fs.FileInfo, st *tarStats, opts tarOptions) error {
	f, err := os.Open(p)
	if err != nil {
		// Locked (an open .pst on Windows), permission denied, or already deleted.
		return skipEntry(opts, p, err)
	}
	defer f.Close()

	// Size comes from the open descriptor, not from the directory entry: between the
	// two the file may have been rewritten, and the header must describe what is about
	// to be copied.
	fi, err := f.Stat()
	if err != nil {
		return skipEntry(opts, p, err)
	}
	size := fi.Size()

	if err := writeTarHeader(tw, root, p, fi, ""); err != nil {
		return err
	}

	var dst io.Writer = tw
	if opts.onProgress != nil {
		// Captured by value: totalBytes/files are the state *before* this file, so the
		// report is consistent even mid-copy.
		var (
			bytesBefore = st.totalBytes
			doneBefore  = st.files
		)
		dst = &progressWriter{w: tw, onWrite: func(written int64) {
			opts.onProgress(TarProgress{
				FilesDone:      doneBefore,
				CurrentFile:    p,
				CurrentDone:    written,
				CurrentSize:    size,
				TotalBytesDone: bytesBefore + written,
			})
		}}
	}

	// LimitReader caps a file that grew after the header was written; tar would
	// otherwise reject the extra bytes with ErrWriteTooLong and kill the whole backup.
	n, err := io.Copy(dst, io.LimitReader(f, size))
	if err != nil {
		// Past the header there is no way back: the entry is already declared, so a
		// half-copied file would restore as silently truncated data.
		return fmt.Errorf("copy %s: %w", p, err)
	}
	if n < size {
		// The file shrank while it was being read (log rotation, a truncating writer).
		// tar promised `size` bytes, so the rest is zero-filled; without this the
		// archive is short and tw.Close() fails with "missed writing N bytes".
		if err := writeZeros(dst, size-n); err != nil {
			return fmt.Errorf("pad %s: %w", p, err)
		}
	}

	st.totalBytes += size
	st.files++
	reportFileDone(opts, p, st)

	return nil
}

// writeTarHeader writes the header for one entry, named relative to the parent of the
// root it was found under (so a backup of /srv/data restores as data/...).
func writeTarHeader(tw *tar.Writer, root, p string, info fs.FileInfo, linkTarget string) error {
	header, err := tar.FileInfoHeader(info, linkTarget)
	if err != nil {
		return fmt.Errorf("tar header for %s: %w", p, err)
	}
	relName, relErr := filepath.Rel(filepath.Dir(root), p)
	if relErr != nil {
		relName = p // fallback
	}
	header.Name = filepath.ToSlash(relName)

	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("write header %s: %w", p, err)
	}
	return nil
}

// reportFileDone emits the "this file is complete" report: it now counts in FilesDone,
// so the current-file fields are cleared to avoid double-counting.
func reportFileDone(opts tarOptions, p string, st *tarStats) {
	if opts.onProgress == nil {
		return
	}
	opts.onProgress(TarProgress{
		FilesDone:      st.files,
		CurrentFile:    p,
		TotalBytesDone: st.totalBytes,
	})
}

// skipEntry routes a non-fatal failure to the caller's policy. With no policy every
// error is fatal, which is what the exported TarCompress functions promise.
func skipEntry(opts tarOptions, p string, err error) error {
	if opts.onSkip == nil {
		if errors.Is(err, errIrregularFile) {
			// Never an error: there is nothing to archive and opening a FIFO would
			// block until someone writes to it.
			return nil
		}
		return err
	}
	return opts.onSkip(p, err)
}

// zeroPad is a shared read-only source of zeros for writeZeros.
var zeroPad = make([]byte, 32*1024)

func writeZeros(w io.Writer, n int64) error {
	for n > 0 {
		chunk := int64(len(zeroPad))
		if n < chunk {
			chunk = n
		}
		written, err := w.Write(zeroPad[:chunk])
		if err != nil {
			return err
		}
		n -= int64(written)
	}
	return nil
}
