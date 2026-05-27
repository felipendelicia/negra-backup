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
func TarCompress(paths []string, w io.Writer, compression string) (int64, error) {
	var compWriter io.WriteCloser
	var err error

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
	defer compWriter.Close()

	tw := tar.NewWriter(compWriter)
	defer tw.Close()

	var totalBytes int64

	for _, path := range paths {
		if err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}

			info, err := d.Info()
			if err != nil {
				return err
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return fmt.Errorf("tar header for %s: %w", p, err)
			}
			header.Name = p

			if err := tw.WriteHeader(header); err != nil {
				return fmt.Errorf("write header %s: %w", p, err)
			}

			if !d.IsDir() {
				f, err := os.Open(p)
				if err != nil {
					return fmt.Errorf("open %s: %w", p, err)
				}
				defer f.Close()

				n, err := io.Copy(tw, f)
				if err != nil {
					return fmt.Errorf("copy %s: %w", p, err)
				}
				totalBytes += n
			}

			return nil
		}); err != nil {
			return totalBytes, fmt.Errorf("walk %s: %w", path, err)
		}
	}

	return totalBytes, nil
}
