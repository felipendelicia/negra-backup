package backup_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// progressUpdate is one OnProgress invocation.
type progressUpdate struct {
	percent int
	file    string
}

// progressCollector records OnProgress calls. It locks because the callback runs on
// the compression goroutine when encryption is on.
type progressCollector struct {
	mu      sync.Mutex
	updates []progressUpdate
}

func (c *progressCollector) fn() func(int, string) {
	return func(percent int, file string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.updates = append(c.updates, progressUpdate{percent, file})
	}
}

func (c *progressCollector) snapshot() []progressUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]progressUpdate(nil), c.updates...)
}

// makeFiles writes n small files into a fresh dir and returns the dir plus the set of
// base names it created.
func makeFiles(t *testing.T, n int) (string, map[string]bool) {
	t.Helper()
	dir := t.TempDir()
	names := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file%03d.txt", i)
		names[name] = true
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, name),
			bytes.Repeat([]byte("payload "), 64),
			0644,
		))
	}
	return dir, names
}

// assertProgressContract checks what the UI depends on: percentages that only grow,
// never exceed 100, land exactly on 100, and name files that really are in the backup.
func assertProgressContract(t *testing.T, updates []progressUpdate, names map[string]bool) {
	t.Helper()

	require.Greater(t, len(updates), 1,
		"progress must be reported while the archive is written, not only at the end")

	last := -1
	for i, u := range updates {
		assert.GreaterOrEqual(t, u.percent, last, "update %d: percent went backwards", i)
		assert.LessOrEqual(t, u.percent, 100, "update %d: percent above 100", i)
		assert.GreaterOrEqual(t, u.percent, 0, "update %d: negative percent", i)
		last = u.percent
	}

	assert.Equal(t, 100, updates[len(updates)-1].percent, "the last update must be 100%%")

	for i, u := range updates[:len(updates)-1] {
		assert.True(t, names[filepath.Base(u.file)],
			"update %d: %q is not one of the backed up files", i, u.file)
	}
}

func TestBackupFiles_ProgressIsRealAndMonotonic(t *testing.T) {
	dir, names := makeFiles(t, 50)

	var c progressCollector
	var buf bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		OnProgress:  c.fn(),
	}, &buf)
	require.NoError(t, err)
	require.Equal(t, 50, res.FileCount)

	assertProgressContract(t, c.snapshot(), names)
}

// Same contract when the archive goes through the encryption pipeline, where
// TarCompress (and therefore the callback) runs on its own goroutine. Run with -race.
func TestBackupFiles_ProgressWithEncryption(t *testing.T) {
	dir, names := makeFiles(t, 50)

	var c progressCollector
	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		Encrypt:     true,
		Passphrase:  "progress-passphrase",
		OnProgress:  c.fn(),
	}, &buf)
	require.NoError(t, err)

	assertProgressContract(t, c.snapshot(), names)

	// The archive must still be a valid, decryptable backup.
	var decrypted bytes.Buffer
	require.NoError(t, backup.DecryptStream(&buf, &decrypted, "progress-passphrase"))
}

// slowWriter stretches the backup over the throttling window so that intermediate
// updates (not just the first one and the final 100%) actually happen.
type slowWriter struct {
	w     io.Writer
	delay time.Duration
}

func (s *slowWriter) Write(b []byte) (int, error) {
	time.Sleep(s.delay)
	return s.w.Write(b)
}

func TestBackupFiles_ProgressReportsIntermediateUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a couple of seconds by design: it has to outlast the throttle window")
	}

	dir := t.TempDir()
	// Incompressible payloads so the compressor keeps flushing blocks to the writer,
	// each one paying the delay below.
	payload := make([]byte, 64<<10)
	for i := 0; i < 40; i++ {
		if _, err := rand.Read(payload); err != nil {
			t.Fatalf("rand: %v", err)
		}
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, fmt.Sprintf("blob%02d.bin", i)),
			payload,
			0644,
		))
	}

	var c progressCollector
	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		OnProgress:  c.fn(),
	}, &slowWriter{w: &buf, delay: 150 * time.Millisecond})
	require.NoError(t, err)

	updates := c.snapshot()
	var intermediate int
	for _, u := range updates[:len(updates)-1] {
		if u.percent > 0 && u.percent < 100 {
			intermediate++
		}
	}
	assert.GreaterOrEqual(t, intermediate, 2,
		"a multi-second backup must produce intermediate updates, got %v", updates)

	// Throttled: at ~150ms per write over a few seconds, an unthrottled callback would
	// fire hundreds of times.
	assert.Less(t, len(updates), 60, "updates are not throttled: %d", len(updates))
}

// A nil OnProgress must be free and must not change what gets written.
func TestBackupFiles_NilProgressProducesSameArchive(t *testing.T) {
	dir, _ := makeFiles(t, 10)

	var withCB, withoutCB bytes.Buffer
	resCB, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		OnProgress:  func(int, string) {},
	}, &withCB)
	require.NoError(t, err)

	resNil, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		OnProgress:  nil,
	}, &withoutCB)
	require.NoError(t, err)

	assert.Equal(t, resCB, resNil)
	assert.Equal(t, withCB.Bytes(), withoutCB.Bytes(),
		"progress reporting must not alter the archive")
}
