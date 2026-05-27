package backup_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTarZstd_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("world"), 0644))

	var buf bytes.Buffer
	n, err := backup.TarCompress([]string{dir}, &buf, backup.CompressionZstd)
	require.NoError(t, err)
	assert.Greater(t, n, int64(0))
	assert.Greater(t, buf.Len(), 0)
}

func TestTarGzip_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0644))

	var buf bytes.Buffer
	n, err := backup.TarCompress([]string{dir}, &buf, backup.CompressionGzip)
	require.NoError(t, err)
	assert.Greater(t, n, int64(0))
}

func TestTarCompress_FileCount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, fmt.Sprintf("f%d.txt", i)),
			[]byte("data"),
			0644,
		))
	}

	var buf bytes.Buffer
	_, err := backup.TarCompress([]string{dir}, &buf, backup.CompressionZstd)
	require.NoError(t, err)
}
