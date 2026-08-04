// internal/backup/restore_include_test.go
package backup_test

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildArchive writes a small tree and returns the archive bytes.
func buildArchive(t *testing.T, passphrase string) []byte {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "etc", "nginx"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "var", "log"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "etc", "nginx", "nginx.conf"), []byte("server {}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "etc", "hosts"), []byte("127.0.0.1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "var", "log", "app.log"), []byte("log line"), 0o644))

	var buf bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{dir},
		Compression: backup.CompressionZstd,
		Encrypt:     passphrase != "",
		Passphrase:  passphrase,
	}, &buf)
	require.NoError(t, err)
	return buf.Bytes()
}

// Listing is what lets the UI show a backup's contents before anyone commits to
// restoring it, so it must work without writing anything.
func TestListArchive(t *testing.T) {
	data := buildArchive(t, "")

	entries, err := backup.ListArchive(bytes.NewReader(data), "")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.Condition(t, func() bool {
		for _, n := range names {
			if filepath.Base(n) == "nginx.conf" {
				return true
			}
		}
		return false
	}, "expected nginx.conf in the listing, got %v", names)
}

func TestListArchive_Encrypted(t *testing.T) {
	data := buildArchive(t, "list-pass")

	_, err := backup.ListArchive(bytes.NewReader(data), "")
	require.ErrorIs(t, err, backup.ErrEncryptedNoPassphrase,
		"listing an encrypted archive without a passphrase must fail like a restore does")

	entries, err := backup.ListArchive(bytes.NewReader(data), "list-pass")
	require.NoError(t, err)
	assert.NotEmpty(t, entries)
}

// Recovering one deleted file is the common case; unpacking everything to get it
// is not an answer.
func TestRestore_IncludeSelectsSubset(t *testing.T) {
	data := buildArchive(t, "")

	entries, err := backup.ListArchive(bytes.NewReader(data), "")
	require.NoError(t, err)

	// Find the archive path of the file we want back.
	var wanted string
	for _, e := range entries {
		if filepath.Base(e.Name) == "nginx.conf" {
			wanted = e.Name
		}
	}
	require.NotEmpty(t, wanted)

	dest := t.TempDir()
	_, err = backup.RestoreWithOptions(bytes.NewReader(data), dest, "", backup.RestoreOptions{
		Include: []string{wanted},
	})
	require.NoError(t, err)

	var restored []string
	require.NoError(t, filepath.Walk(dest, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			restored = append(restored, filepath.Base(p))
		}
		return nil
	}))
	assert.Equal(t, []string{"nginx.conf"}, restored,
		"only the requested file may be written")
}

// Asking for a directory means asking for what is under it.
func TestRestore_IncludeDirectoryPrefix(t *testing.T) {
	data := buildArchive(t, "")

	entries, err := backup.ListArchive(bytes.NewReader(data), "")
	require.NoError(t, err)

	var nginxDir string
	for _, e := range entries {
		if filepath.Base(e.Name) == "nginx.conf" {
			nginxDir = filepath.ToSlash(filepath.Dir(e.Name))
		}
	}
	require.NotEmpty(t, nginxDir)

	dest := t.TempDir()
	_, err = backup.RestoreWithOptions(bytes.NewReader(data), dest, "", backup.RestoreOptions{
		Include: []string{nginxDir},
	})
	require.NoError(t, err)

	var restored []string
	require.NoError(t, filepath.Walk(dest, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			restored = append(restored, filepath.Base(p))
		}
		return nil
	}))
	assert.Equal(t, []string{"nginx.conf"}, restored)
}

// The include filter must never become a way around the traversal defences: a
// malicious entry has to be refused whether or not the operator asked for it.
func TestRestore_IncludeDoesNotBypassSafetyChecks(t *testing.T) {
	raw := makeTar(t, []tarEntry{
		{hdr: &tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
	})
	dest := t.TempDir()
	_, err := backup.RestoreWithOptions(
		bytes.NewReader(zstdWrap(t, raw)), dest, "", backup.RestoreOptions{
			Include: []string{"../evil.txt"},
		})
	require.ErrorIs(t, err, backup.ErrUnsafePath)

	_, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt"))
	require.True(t, os.IsNotExist(statErr), "nothing may be written outside the destination")
}
