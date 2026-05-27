package storage_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalBackend_Upload(t *testing.T) {
	dir := t.TempDir()

	backend := storage.NewLocalBackend(storage.LocalConfig{Path: dir})

	data := bytes.NewReader([]byte("backup-content"))
	err := backend.Upload("backup-2026.tar.zst", data, int64(data.Len()))
	require.NoError(t, err)

	written, err := os.ReadFile(filepath.Join(dir, "backup-2026.tar.zst"))
	require.NoError(t, err)
	assert.Equal(t, "backup-content", string(written))
}

func TestStorageFactory_Local(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": dir})
	backend, err := storage.NewBackend("local", cfg, "run-id", "server-url", "api-key")
	require.NoError(t, err)
	assert.NotNil(t, backend)
}

func TestStorageFactory_UnknownType(t *testing.T) {
	_, err := storage.NewBackend("unknown", nil, "run-id", "server-url", "api-key")
	require.Error(t, err)
}
