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

// The raw hook is unthrottled, so this pins down the mechanism BackupFiles builds on:
// one report per file at least, real names, counters that only move forward.
func TestTarCompressProgress_ReportsEveryFile(t *testing.T) {
	const nFiles = 50

	dir := t.TempDir()
	names := make(map[string]bool, nFiles)
	for i := 0; i < nFiles; i++ {
		name := fmt.Sprintf("f%02d.txt", i)
		names[name] = true
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, name),
			bytes.Repeat([]byte("x"), 128),
			0644,
		))
	}

	var reports []backup.TarProgress
	var buf bytes.Buffer
	total, err := backup.TarCompressProgress([]string{dir}, &buf, backup.CompressionZstd,
		func(p backup.TarProgress) { reports = append(reports, p) })
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(reports), nFiles, "expected at least one report per file")

	lastFiles := 0
	var lastBytes int64
	for i, p := range reports {
		assert.GreaterOrEqual(t, p.FilesDone, lastFiles, "report %d: FilesDone went backwards", i)
		assert.GreaterOrEqual(t, p.TotalBytesDone, lastBytes, "report %d: TotalBytesDone went backwards", i)
		assert.LessOrEqual(t, p.FilesDone, nFiles)
		assert.True(t, names[filepath.Base(p.CurrentFile)],
			"report %d: %q is not one of the archived files", i, p.CurrentFile)
		lastFiles, lastBytes = p.FilesDone, p.TotalBytesDone
	}

	last := reports[len(reports)-1]
	assert.Equal(t, nFiles, last.FilesDone, "every file must be accounted for at the end")
	assert.Equal(t, total, last.TotalBytesDone, "final report must match the returned byte count")
}

// A single big file (what the database path produces) must still report movement:
// per-file counting alone would sit at 0 until the whole dump is archived.
func TestTarCompressProgress_ReportsBytesWithinOneFile(t *testing.T) {
	// ~2 MiB, i.e. dozens of chunks of io.Copy's 32 KiB buffer.
	content := bytes.Repeat([]byte("SELECT 1;\n"), 200000)
	size := int64(len(content))

	dir := t.TempDir()
	path := filepath.Join(dir, "dump.sql")
	require.NoError(t, os.WriteFile(path, content, 0644))

	var reports []backup.TarProgress
	var buf bytes.Buffer
	_, err := backup.TarCompressProgress([]string{path}, &buf, backup.CompressionZstd,
		func(p backup.TarProgress) { reports = append(reports, p) })
	require.NoError(t, err)

	var partial []backup.TarProgress
	for _, p := range reports {
		if p.CurrentSize > 0 {
			partial = append(partial, p)
		}
	}
	require.Greater(t, len(partial), 1, "a 2 MiB file must report byte progress more than once")

	var lastDone int64
	for i, p := range partial {
		assert.Equal(t, size, p.CurrentSize)
		assert.Greater(t, p.CurrentDone, lastDone, "report %d: CurrentDone must advance", i)
		assert.LessOrEqual(t, p.CurrentDone, p.CurrentSize)
		lastDone = p.CurrentDone
	}
	assert.Equal(t, size, lastDone, "byte progress must reach the file size")
}
