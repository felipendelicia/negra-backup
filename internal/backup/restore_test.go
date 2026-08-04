package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourceTree lays out a small tree with distinct permissions and returns the file
// contents keyed by their path relative to the tree root.
func sourceTree(t *testing.T) (root string, want map[string][]byte, modes map[string]os.FileMode) {
	t.Helper()

	root = filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested", "deep"), 0o755))

	want = map[string][]byte{
		"hello.txt":             []byte("hello world"),
		"binary.bin":            {0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x7f},
		"nested/config.yaml":    []byte("key: value\nother: 2\n"),
		"nested/deep/script.sh": []byte("#!/bin/sh\necho hi\n"),
		"empty.txt":             {},
	}
	modes = map[string]os.FileMode{
		"hello.txt":             0o644,
		"binary.bin":            0o600,
		"nested/config.yaml":    0o640,
		"nested/deep/script.sh": 0o755,
		"empty.txt":             0o604,
	}

	// A file big enough to span several 64KB encryption chunks, so the round-trip
	// exercises more than a single Seal/Open.
	big := bytes.Repeat([]byte("0123456789abcdef"), 20000) // 320000 bytes
	want["big.dat"] = big
	modes["big.dat"] = 0o644

	for name, content := range want {
		p := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.WriteFile(p, content, modes[name]))
		require.NoError(t, os.Chmod(p, modes[name]))
	}
	return root, want, modes
}

// assertRestoredTree checks that destDir contains the tree produced by sourceTree.
// BackupFiles stores paths relative to the parent of the backed-up root, so the tree
// reappears under destDir/<basename of root>.
func assertRestoredTree(t *testing.T, destDir, srcRoot string, want map[string][]byte, modes map[string]os.FileMode) {
	t.Helper()

	base := filepath.Join(destDir, filepath.Base(srcRoot))
	for name, content := range want {
		p := filepath.Join(base, filepath.FromSlash(name))

		got, err := os.ReadFile(p)
		require.NoError(t, err, "reading restored %s", name)
		assert.Equal(t, content, got, "content mismatch for %s", name)

		fi, err := os.Stat(p)
		require.NoError(t, err)
		if runtime.GOOS != "windows" {
			assert.Equal(t, modes[name], fi.Mode().Perm(), "mode mismatch for %s", name)
		}
	}
}

func TestRestore_RoundTripEncrypted(t *testing.T) {
	srcRoot, want, modes := sourceTree(t)
	const pass = "correct horse battery staple"

	var archive bytes.Buffer
	res, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{srcRoot},
		Compression: backup.CompressionZstd,
		Encrypt:     true,
		Passphrase:  pass,
	}, &archive)
	require.NoError(t, err)
	require.Equal(t, len(want), res.FileCount)

	dest := t.TempDir()
	rres, err := backup.Restore(bytes.NewReader(archive.Bytes()), dest, pass)
	require.NoError(t, err)

	assert.True(t, rres.Encrypted)
	assert.Equal(t, backup.CompressionZstd, rres.Compression)
	assert.Equal(t, len(want), rres.FileCount)
	assert.Equal(t, res.SizeBytes, rres.TotalBytes)

	assertRestoredTree(t, dest, srcRoot, want, modes)
}

func TestRestore_RoundTripPlain(t *testing.T) {
	for _, compression := range []string{backup.CompressionZstd, backup.CompressionGzip} {
		t.Run(compression, func(t *testing.T) {
			srcRoot, want, modes := sourceTree(t)

			var archive bytes.Buffer
			_, err := backup.BackupFiles(backup.FilesConfig{
				Paths:       []string{srcRoot},
				Compression: compression,
			}, &archive)
			require.NoError(t, err)

			dest := t.TempDir()
			rres, err := backup.Restore(bytes.NewReader(archive.Bytes()), dest, "")
			require.NoError(t, err)

			assert.False(t, rres.Encrypted)
			assert.Equal(t, compression, rres.Compression)
			assertRestoredTree(t, dest, srcRoot, want, modes)
		})
	}
}

// TestRestore_FromFileOnDisk is the scenario the whole feature exists for: an
// operator holding nothing but the archive file recovers the data byte for byte.
func TestRestore_FromFileOnDisk(t *testing.T) {
	srcRoot, want, modes := sourceTree(t)
	const pass = "on-disk-passphrase"

	archivePath := filepath.Join(t.TempDir(), "backup.tar.zst.enc")
	f, err := os.Create(archivePath)
	require.NoError(t, err)
	_, err = backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{srcRoot},
		Compression: backup.CompressionZstd,
		Encrypt:     true,
		Passphrase:  pass,
	}, f)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	in, err := os.Open(archivePath)
	require.NoError(t, err)
	defer in.Close()

	dest := t.TempDir()
	rres, err := backup.Restore(in, dest, pass)
	require.NoError(t, err)
	assert.Equal(t, len(want), rres.FileCount)

	assertRestoredTree(t, dest, srcRoot, want, modes)

	// Byte-for-byte comparison against the originals, independent of the map above.
	require.NoError(t, filepath.Walk(srcRoot, func(p string, fi os.FileInfo, err error) error {
		require.NoError(t, err)
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filepath.Dir(srcRoot), p)
		require.NoError(t, err)

		orig, err := os.ReadFile(p)
		require.NoError(t, err)
		restored, err := os.ReadFile(filepath.Join(dest, rel))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(orig, restored), "bytes differ for %s", rel)
		return nil
	}))
}

func TestRestore_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("sensitive"), 0o600))

	var archive bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:      []string{dir},
		Encrypt:    true,
		Passphrase: "right-passphrase",
	}, &archive)
	require.NoError(t, err)

	dest := t.TempDir()
	_, err = backup.Restore(bytes.NewReader(archive.Bytes()), dest, "wrong-passphrase")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt")

	entries, err := os.ReadDir(dest)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing must be written when decryption fails")
}

func TestRestore_EncryptedWithoutPassphrase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("sensitive"), 0o600))

	var archive bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:      []string{dir},
		Encrypt:    true,
		Passphrase: "some-passphrase",
	}, &archive)
	require.NoError(t, err)

	_, err = backup.Restore(bytes.NewReader(archive.Bytes()), t.TempDir(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, backup.ErrEncryptedNoPassphrase)
}

// A passphrase handed to a plaintext archive is a user mistake, not a failure: warn
// and restore anyway rather than leaving the operator without their data.
func TestRestore_PassphraseOnPlaintextArchiveWarnsAndSucceeds(t *testing.T) {
	srcRoot, want, modes := sourceTree(t)

	var archive bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{srcRoot},
		Compression: backup.CompressionZstd,
	}, &archive)
	require.NoError(t, err)

	var warnings []string
	dest := t.TempDir()
	_, err = backup.RestoreWithOptions(bytes.NewReader(archive.Bytes()), dest, "unnecessary", backup.RestoreOptions{
		Logf: func(format string, args ...any) { warnings = append(warnings, format) },
	})
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "not encrypted")

	assertRestoredTree(t, dest, srcRoot, want, modes)
}

func TestRestore_DryRunWritesNothing(t *testing.T) {
	srcRoot, want, _ := sourceTree(t)

	var archive bytes.Buffer
	_, err := backup.BackupFiles(backup.FilesConfig{
		Paths:       []string{srcRoot},
		Compression: backup.CompressionZstd,
	}, &archive)
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "not-created-yet")
	var listed []string
	res, err := backup.RestoreWithOptions(bytes.NewReader(archive.Bytes()), dest, "", backup.RestoreOptions{
		DryRun:  true,
		OnEntry: func(e backup.RestoreEntry) { listed = append(listed, e.Name) },
	})
	require.NoError(t, err)

	assert.Equal(t, len(want), res.FileCount)
	assert.Len(t, listed, len(want)+3) // files + the 3 directories of the tree
	_, statErr := os.Stat(dest)
	assert.True(t, os.IsNotExist(statErr), "dry run must not create the destination")
}

func TestRestore_EmptyStream(t *testing.T) {
	_, err := backup.Restore(bytes.NewReader(nil), t.TempDir(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, backup.ErrEmptyArchive)
}

// An archive that decrypts cleanly but is not a compressed tar must be reported as
// an unknown format rather than as a decryption or tar failure.
func TestRestore_UnknownFormat(t *testing.T) {
	const pass = "unknown-format-pass"

	var encrypted bytes.Buffer
	require.NoError(t, backup.EncryptStream(
		bytes.NewReader([]byte("this is not a compressed tar, just plain junk")), &encrypted, pass))

	_, err := backup.Restore(bytes.NewReader(encrypted.Bytes()), t.TempDir(), pass)
	require.Error(t, err)
	assert.ErrorIs(t, err, backup.ErrUnknownFormat)
}

func TestIsEncrypted(t *testing.T) {
	assert.False(t, backup.IsEncrypted([]byte{0x28, 0xB5, 0x2F, 0xFD}))
	assert.False(t, backup.IsEncrypted([]byte{0x1F, 0x8B, 0x08, 0x00}))
	assert.True(t, backup.IsEncrypted([]byte{0xDE, 0xAD, 0xBE, 0xEF}))
}

// --- Path traversal ---------------------------------------------------------

// tarEntry is one member of a hand-built tar.
type tarEntry struct {
	hdr  *tar.Header
	body string
}

// makeTar builds an uncompressed tar from the given headers/bodies.
func makeTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(e.hdr))
		if e.body != "" {
			_, err := io.WriteString(tw, e.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return raw.Bytes()
}

func zstdWrap(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	enc, err := zstd.NewWriter(&out)
	require.NoError(t, err)
	_, err = enc.Write(raw)
	require.NoError(t, err)
	require.NoError(t, enc.Close())
	return out.Bytes()
}

func gzipWrap(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	_, err := gw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return out.Bytes()
}

// TestRestore_RejectsPathTraversal feeds hand-built malicious tars through the real
// pipeline and proves nothing lands outside the destination directory.
func TestRestore_RejectsPathTraversal(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		// escapee is a path relative to the parent of destDir that must not exist.
		escapee string
	}{
		{
			name: "dotdot in name",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
			},
			escapee: "evil.txt",
		},
		{
			name: "dotdot in the middle of a name",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "ok/../../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
			},
			escapee: "evil.txt",
		},
		{
			name: "absolute path",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "/etc/evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
			},
		},
		{
			name: "backslash traversal",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: `..\evil.txt`, Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
			},
			escapee: `..\evil.txt`,
		},
		{
			name: "symlink escaping the destination",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "link", Linkname: "../evil.txt", Mode: 0o777, Typeflag: tar.TypeSymlink}},
			},
			escapee: "evil.txt",
		},
		{
			name: "absolute symlink",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "link", Linkname: "/etc/passwd", Mode: 0o777, Typeflag: tar.TypeSymlink}},
			},
		},
		{
			name: "hard link escaping the destination",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "link", Linkname: "../evil.txt", Typeflag: tar.TypeLink}},
			},
			escapee: "evil.txt",
		},
		{
			name: "dotdot directory",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "../evil-dir", Mode: 0o755, Typeflag: tar.TypeDir}},
			},
			escapee: "evil-dir",
		},
		{
			// The dangerous shape: a benign file first, so the extractor is already
			// mid-stream and writing when it meets the malicious entry.
			name: "good entry followed by a traversal",
			entries: []tarEntry{
				{hdr: &tar.Header{Name: "data/good.txt", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}, body: "ok"},
				{hdr: &tar.Header{Name: "data/../../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
			},
			escapee: "evil.txt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := makeTar(t, tc.entries)

			// A sibling of destDir is the thing "../" would reach.
			outer := t.TempDir()
			dest := filepath.Join(outer, "dest")

			_, err := backup.Restore(bytes.NewReader(zstdWrap(t, raw)), dest, "")
			require.Error(t, err, "malicious entry must be rejected")
			assert.ErrorIs(t, err, backup.ErrUnsafePath)

			if tc.escapee != "" {
				escaped := filepath.Join(outer, tc.escapee)
				_, statErr := os.Lstat(escaped)
				assert.True(t, os.IsNotExist(statErr), "%s must not exist outside destDir", escaped)
			}
			// Nothing may exist above destDir other than destDir itself.
			siblings, readErr := os.ReadDir(outer)
			require.NoError(t, readErr)
			for _, s := range siblings {
				assert.Equal(t, "dest", s.Name(), "unexpected entry created outside destDir")
			}
		})
	}
}

// The traversal defence must not depend on the compression used.
func TestRestore_RejectsPathTraversalGzip(t *testing.T) {
	raw := makeTar(t, []tarEntry{
		{hdr: &tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
	})

	outer := t.TempDir()
	dest := filepath.Join(outer, "dest")

	_, err := backup.Restore(bytes.NewReader(gzipWrap(t, raw)), dest, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, backup.ErrUnsafePath)
	_, statErr := os.Lstat(filepath.Join(outer, "evil.txt"))
	assert.True(t, os.IsNotExist(statErr))
}

// The same defence has to hold once the malicious tar is encrypted, since that is
// the format the product actually produces.
func TestRestore_RejectsPathTraversalEncrypted(t *testing.T) {
	const pass = "traversal-pass"

	raw := makeTar(t, []tarEntry{
		{hdr: &tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
	})

	var encrypted bytes.Buffer
	require.NoError(t, backup.EncryptStream(bytes.NewReader(zstdWrap(t, raw)), &encrypted, pass))

	outer := t.TempDir()
	dest := filepath.Join(outer, "dest")

	_, err := backup.Restore(bytes.NewReader(encrypted.Bytes()), dest, pass)
	require.Error(t, err)
	assert.ErrorIs(t, err, backup.ErrUnsafePath)
	_, statErr := os.Lstat(filepath.Join(outer, "evil.txt"))
	assert.True(t, os.IsNotExist(statErr))
}

// A dry run must reject the same entries: it is used to inspect untrusted archives.
func TestRestore_DryRunRejectsPathTraversal(t *testing.T) {
	raw := makeTar(t, []tarEntry{
		{hdr: &tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "pwned"},
	})

	outer := t.TempDir()
	_, err := backup.RestoreWithOptions(bytes.NewReader(zstdWrap(t, raw)),
		filepath.Join(outer, "dest"), "", backup.RestoreOptions{DryRun: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, backup.ErrUnsafePath)
}

// Links that stay inside the destination are legitimate and must survive.
func TestRestore_AllowsInternalLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}

	raw := makeTar(t, []tarEntry{
		{hdr: &tar.Header{Name: "data", Mode: 0o755, Typeflag: tar.TypeDir}},
		{hdr: &tar.Header{Name: "data/real.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, body: "hello"},
		{hdr: &tar.Header{Name: "data/soft", Linkname: "real.txt", Mode: 0o777, Typeflag: tar.TypeSymlink}},
		{hdr: &tar.Header{Name: "data/hard", Linkname: "data/real.txt", Typeflag: tar.TypeLink}},
	})

	dest := t.TempDir()
	res, err := backup.Restore(bytes.NewReader(zstdWrap(t, raw)), dest, "")
	require.NoError(t, err)
	assert.Equal(t, 3, res.FileCount)
	assert.Equal(t, 1, res.DirCount)

	got, err := os.ReadFile(filepath.Join(dest, "data", "soft"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	got, err = os.ReadFile(filepath.Join(dest, "data", "hard"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

// A read-only directory in the archive must not stop its own contents from being
// written: permissions are applied after the children.
func TestRestore_ReadOnlyDirectoryKeepsItsContents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions")
	}

	raw := makeTar(t, []tarEntry{
		{hdr: &tar.Header{Name: "locked", Mode: 0o500, Typeflag: tar.TypeDir}},
		{hdr: &tar.Header{Name: "locked/inside.txt", Mode: 0o644, Size: 6, Typeflag: tar.TypeReg}, body: "inside"},
	})

	dest := t.TempDir()
	_, err := backup.Restore(bytes.NewReader(zstdWrap(t, raw)), dest, "")
	require.NoError(t, err)

	// Loosen it again so t.TempDir cleanup can remove the tree.
	dirPath := filepath.Join(dest, "locked")
	fi, err := os.Stat(dirPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o500), fi.Mode().Perm())
	require.NoError(t, os.Chmod(dirPath, 0o755))

	got, err := os.ReadFile(filepath.Join(dirPath, "inside.txt"))
	require.NoError(t, err)
	assert.Equal(t, "inside", string(got))
}
