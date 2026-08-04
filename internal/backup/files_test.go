package backup_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
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

// restoredTree restores an archive into a fresh directory and returns the set of
// entries it contains, keyed by their path relative to the restored root. Going
// through Restore rather than reading the tar directly also proves, in every test that
// uses it, that the archive is still a valid and restorable backup.
func restoredTree(t *testing.T, archive []byte, rootName string) map[string]string {
	t.Helper()

	dest := t.TempDir()
	_, err := backup.Restore(bytes.NewReader(archive), dest, "")
	require.NoError(t, err)

	base := filepath.Join(dest, rootName)
	got := map[string]string{}
	require.NoError(t, filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		require.NoError(t, err)
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		require.NoError(t, err)
		content, err := os.ReadFile(p)
		require.NoError(t, err)
		got[filepath.ToSlash(rel)] = string(content)
		return nil
	}))
	return got
}

// excludeTree lays out the shape the feature exists for: a couple of real files next
// to a fat dependency directory and some noise.
func excludeTree(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "www")
	for _, dir := range []string{
		filepath.Join(root, "site1", "node_modules", "left-pad"),
		filepath.Join(root, "site1", "src"),
		filepath.Join(root, "site2", "logs"),
	} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}

	files := map[string]string{
		"site1/src/app.php":                        "<?php",
		"site1/node_modules/left-pad/index.js":     "module.exports",
		"site1/node_modules/left-pad/package.json": "{}",
		"site2/logs/access.log":                    "GET /",
		"site2/index.html":                         "<html>",
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o644))
	}
	return root
}

func TestBackupFiles_ExcludeByName(t *testing.T) {
	root := excludeTree(t)

	var buf bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Exclude:     []string{"node_modules", "*.log"},
		Compression: backup.CompressionZstd,
	}, &buf)
	require.NoError(t, err)

	assert.Equal(t, 2, res.FileCount)
	assert.Zero(t, res.SkippedCount, "an excluded file is not a skipped file")

	got := restoredTree(t, buf.Bytes(), "www")
	assert.Equal(t, map[string]string{
		"site1/src/app.php": "<?php",
		"site2/index.html":  "<html>",
	}, got)
}

func TestBackupFiles_ExcludeByPath(t *testing.T) {
	root := excludeTree(t)

	var buf bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths: []string{root},
		Exclude: []string{
			filepath.ToSlash(filepath.Join(root, "*", "node_modules")), // absolute, wildcard segment
			"site2/logs", // relative path suffix
		},
		Compression: backup.CompressionZstd,
	}, &buf)
	require.NoError(t, err)
	assert.Equal(t, 2, res.FileCount)

	got := restoredTree(t, buf.Bytes(), "www")
	assert.Equal(t, map[string]string{
		"site1/src/app.php": "<?php",
		"site2/index.html":  "<html>",
	}, got)
}

// The progress denominator must apply the same exclusions, otherwise the bar is
// computed against files that will never be archived.
func TestBackupFiles_ExcludedFilesDoNotSkewProgress(t *testing.T) {
	root := excludeTree(t)

	var c progressCollector
	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Exclude:     []string{"node_modules", "*.log"},
		Compression: backup.CompressionZstd,
		OnProgress:  c.fn(),
	}, &buf)
	require.NoError(t, err)

	updates := c.snapshot()
	require.NotEmpty(t, updates)
	assert.Equal(t, 100, updates[len(updates)-1].percent)
	for _, u := range updates {
		assert.NotContains(t, u.file, "node_modules",
			"an excluded file was reported as being archived")
	}
}

// unreadableDir writes n files and makes the first `bad` of them impossible to open.
func unreadableDir(t *testing.T, n, bad int) (root string, unreadable []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not remove read access on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions do not stop the read")
	}

	root = filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(root, 0o755))
	for i := 0; i < n; i++ {
		p := filepath.Join(root, fmt.Sprintf("f%02d.txt", i))
		require.NoError(t, os.WriteFile(p, []byte("content"), 0o644))
		if i < bad {
			require.NoError(t, os.Chmod(p, 0o000))
			unreadable = append(unreadable, p)
		}
	}
	return root, unreadable
}

// The whole point of 5b: one locked or protected file must not cost the other 99%.
func TestBackupFiles_UnreadableFileIsSkippedNotFatal(t *testing.T) {
	root, unreadable := unreadableDir(t, 20, 1)

	var buf bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Compression: backup.CompressionZstd,
	}, &buf)
	require.NoError(t, err, "one unreadable file must not fail the backup")

	assert.Equal(t, 19, res.FileCount)
	assert.Equal(t, 1, res.SkippedCount)
	assert.Equal(t, 1, res.SkippedSuspicious, "permission denied is suspicious, not routine")
	require.Len(t, res.Skipped, 1)
	assert.Equal(t, unreadable[0], res.Skipped[0].Path)
	assert.True(t, res.Skipped[0].Suspicious)
	assert.Contains(t, res.Skipped[0].Reason, "permission denied")

	// And the 19 that could be read are really there, restorable.
	got := restoredTree(t, buf.Bytes(), "data")
	assert.Len(t, got, 19)
	assert.NotContains(t, got, filepath.Base(unreadable[0]))
	for _, content := range got {
		assert.Equal(t, "content", content)
	}
}

// Same, through the encryption pipeline, where the skip is recorded on the compression
// goroutine. Run with -race.
func TestBackupFiles_UnreadableFileSkippedWhenEncrypted(t *testing.T) {
	root, _ := unreadableDir(t, 20, 1)

	const pass = "skip-passphrase"
	var buf bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Compression: backup.CompressionZstd,
		Encrypt:     true,
		Passphrase:  pass,
	}, &buf)
	require.NoError(t, err)
	assert.Equal(t, 19, res.FileCount)
	assert.Equal(t, 1, res.SkippedSuspicious)

	dest := t.TempDir()
	rres, err := backup.Restore(bytes.NewReader(buf.Bytes()), dest, pass)
	require.NoError(t, err)
	assert.Equal(t, 19, rres.FileCount)
}

// Tolerance has a limit: a tree where most files cannot be read is a broken disk or a
// broken permission set, and reporting that as a successful backup is the failure mode
// this whole exercise is about.
func TestBackupFiles_TooManyUnreadableFilesFails(t *testing.T) {
	root, _ := unreadableDir(t, 20, 11)

	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Compression: backup.CompressionZstd,
	}, &buf)
	require.ErrorIs(t, err, backup.ErrTooManySkipped)
}

// Progress must still land on 100 when part of the tree could not be read: the
// denominator counted those files, the numerator never will.
func TestBackupFiles_ProgressStillReaches100WithSkips(t *testing.T) {
	root, _ := unreadableDir(t, 20, 5)

	var c progressCollector
	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Compression: backup.CompressionZstd,
		OnProgress:  c.fn(),
	}, &buf)
	require.NoError(t, err)

	updates := c.snapshot()
	require.NotEmpty(t, updates)
	last := -1
	for i, u := range updates {
		assert.GreaterOrEqual(t, u.percent, last, "update %d: percent went backwards", i)
		assert.LessOrEqual(t, u.percent, 100)
		last = u.percent
	}
	assert.Equal(t, 100, updates[len(updates)-1].percent)
}

// A file that disappears between the listing and the read, and a socket, are both
// normal. Neither may count against the failure budget.
func TestBackupFiles_ExpectedSkipsAreNotSuspicious(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not part of the Windows backup story")
	}

	root := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "real.txt"), []byte("real"), 0o644))

	sock := filepath.Join(root, "daemon.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot create unix socket: %v", err)
	}
	defer l.Close()

	var buf bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Compression: backup.CompressionZstd,
	}, &buf)
	require.NoError(t, err)

	assert.Equal(t, 1, res.FileCount)
	assert.Equal(t, 1, res.SkippedCount)
	assert.Zero(t, res.SkippedSuspicious, "a socket is not a symptom of anything")
	require.Len(t, res.Skipped, 1)
	assert.False(t, res.Skipped[0].Suspicious)

	assert.Equal(t, map[string]string{"real.txt": "real"}, restoredTree(t, buf.Bytes(), "data"))
}

// A missing root is not a skip: it is a mistyped path or an unmounted volume, and an
// empty archive reported as a success is the worst outcome a backup tool can produce.
func TestBackupFiles_MissingRootIsFatal(t *testing.T) {
	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{filepath.Join(t.TempDir(), "does-not-exist")},
		Compression: backup.CompressionZstd,
	}, &buf)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// Symlinks used to kill a file backup outright: the header declared a symlink (zero
// bytes) and the copy then pushed the target's contents into it, so tar answered
// "write too long" and the run failed.
func TestBackupFiles_SymlinkIsArchivedAsLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}

	root := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "real.txt"), []byte("real"), 0o644))
	require.NoError(t, os.Symlink("real.txt", filepath.Join(root, "link.txt")))

	var buf bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{root},
		Compression: backup.CompressionZstd,
	}, &buf)
	require.NoError(t, err)
	assert.Equal(t, 2, res.FileCount)
	assert.Zero(t, res.SkippedCount)

	dest := t.TempDir()
	_, err = backup.Restore(bytes.NewReader(buf.Bytes()), dest, "")
	require.NoError(t, err)

	fi, err := os.Lstat(filepath.Join(dest, "data", "link.txt"))
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the link must come back as a link")
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
