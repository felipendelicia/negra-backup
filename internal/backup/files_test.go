package backup_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/klauspost/compress/zstd"
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

	// Asserting the plaintext is absent proves nothing: compression alone removes
	// the literal. The output is encrypted only if the full chain reverses.
	var decrypted bytes.Buffer
	require.NoError(t, backup.DecryptStream(&buf, &decrypted, "my-passphrase"))

	zr, err := zstd.NewReader(&decrypted)
	require.NoError(t, err)
	defer zr.Close()
	tarball, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Contains(t, string(tarball), "sensitive data")
}

// The plaintext archive produced when encryption is off must NOT be decryptable —
// this is what distinguishes a real encrypted backup from a merely compressed one.
func TestBackupFiles_UnencryptedIsNotDecryptable(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("sensitive data"), 0644))

	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		Encrypt:     false,
	}, &buf)
	require.NoError(t, err)

	var decrypted bytes.Buffer
	assert.Error(t, backup.DecryptStream(&buf, &decrypted, "my-passphrase"))
}

// Encryption requested without a passphrase must fail loudly. Degrading to plaintext
// here produced an unencrypted archive that the agent still named ".enc".
func TestBackupFiles_EncryptWithoutPassphraseFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("sensitive data"), 0644))

	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		Encrypt:     true,
		Passphrase:  "",
	}, &buf)

	require.ErrorIs(t, err, backup.ErrMissingPassphrase)
	assert.Zero(t, buf.Len(), "nothing must be written when the passphrase is missing")
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
