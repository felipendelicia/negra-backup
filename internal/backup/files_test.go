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

func TestBackupFiles_NoEncryption(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world"), 0644))

	var buf bytes.Buffer
	result, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		Encrypt:     false,
	}, &buf)

	require.NoError(t, err)
	assert.Greater(t, result.SizeBytes, int64(0))
	assert.Greater(t, result.FileCount, 0)
	assert.Greater(t, buf.Len(), 0)
}

func TestBackupFiles_WithEncryption(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("sensitive data"), 0644))

	var buf bytes.Buffer
	result, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		Encrypt:     true,
		Passphrase:  "my-passphrase",
	}, &buf)

	require.NoError(t, err)
	assert.Greater(t, result.SizeBytes, int64(0))
	assert.NotContains(t, buf.String(), "sensitive data")
}

func TestBackupFiles_ProgressCallback(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, fmt.Sprintf("f%d.txt", i)),
			[]byte("data"),
			0644,
		))
	}

	var progressUpdates []int
	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionGzip,
		OnProgress: func(percent int, currentFile string) {
			progressUpdates = append(progressUpdates, percent)
		},
	}, &buf)

	require.NoError(t, err)
	assert.NotEmpty(t, progressUpdates)
}
