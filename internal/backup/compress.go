package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

const (
	CompressionZstd = "zstd"
	CompressionGzip = "gzip"
)

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
	var compWriter io.WriteCloser

	switch compression {
	case CompressionZstd:
		enc, e := zstd.NewWriter(w)
		if e != nil {
			return 0, fmt.Errorf("zstd writer: %w", e)
		}
		compWriter = enc
	case CompressionGzip:
		compWriter = gzip.NewWriter(w)
	default:
		return 0, fmt.Errorf("unknown compression: %s", compression)
	}

	tw := tar.NewWriter(compWriter)

	var filesDone int

	for _, root := range paths {
		if walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}

			info, err := d.Info()
			if err != nil {
				return err
			}

			header, err := tar.FileInfoHeader(info, "")
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

			if !d.IsDir() {
				var onChunk func(int64)
				if onProgress != nil {
					// Captured by value: totalBytes/filesDone are the state *before*
					// this file, so the report is consistent even mid-copy.
					var (
						bytesBefore = totalBytes
						doneBefore  = filesDone
						size        = info.Size()
					)
					onChunk = func(written int64) {
						onProgress(TarProgress{
							FilesDone:      doneBefore,
							CurrentFile:    p,
							CurrentDone:    written,
							CurrentSize:    size,
							TotalBytesDone: bytesBefore + written,
						})
					}
				}

				n, err := copyFile(tw, p, onChunk)
				if err != nil {
					return err
				}
				totalBytes += n
				filesDone++

				if onProgress != nil {
					// File completed: it now counts in FilesDone, so the
					// current-file fields are cleared to avoid double-counting.
					onProgress(TarProgress{
						FilesDone:      filesDone,
						CurrentFile:    p,
						TotalBytesDone: totalBytes,
					})
				}
			}

			return nil
		}); walkErr != nil {
			// Close on error (best-effort)
			tw.Close()
			compWriter.Close()
			return totalBytes, fmt.Errorf("walk %s: %w", root, walkErr)
		}
	}

	// Close in order: tar first, then compressor (flushes trailer)
	if closeErr := tw.Close(); closeErr != nil {
		compWriter.Close()
		return totalBytes, fmt.Errorf("tar close: %w", closeErr)
	}
	if closeErr := compWriter.Close(); closeErr != nil {
		return totalBytes, fmt.Errorf("compress close: %w", closeErr)
	}

	return totalBytes, nil
}

// copyFile copies a single file into the tar writer, closing the file after.
// onChunk, when non-nil, receives the running byte count after every write.
func copyFile(tw *tar.Writer, path string, onChunk func(written int64)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var dst io.Writer = tw
	if onChunk != nil {
		dst = &progressWriter{w: tw, onWrite: onChunk}
	}

	n, err := io.Copy(dst, f)
	if err != nil {
		return n, fmt.Errorf("copy %s: %w", path, err)
	}
	return n, nil
}
