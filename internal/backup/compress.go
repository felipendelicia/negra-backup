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
				n, err := copyFile(tw, p)
				if err != nil {
					return err
				}
				totalBytes += n
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
func copyFile(tw *tar.Writer, path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	n, err := io.Copy(tw, f)
	if err != nil {
		return n, fmt.Errorf("copy %s: %w", path, err)
	}
	return n, nil
}
