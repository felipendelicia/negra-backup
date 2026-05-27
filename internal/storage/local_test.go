package storage_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalBackend_Upload(t *testing.T) {
	var receivedBytes int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		defer file.Close()
		var buf bytes.Buffer
		buf.ReadFrom(file)
		receivedBytes = int64(buf.Len())
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	backend := storage.NewLocalBackend(storage.LocalConfig{
		ServerURL: srv.URL,
		APIKey:    "test-key",
		RunID:     "run-123",
	})

	data := bytes.NewReader([]byte("backup-content"))
	err := backend.Upload("backup-2026.tar.zst", data, int64(data.Len()))
	require.NoError(t, err)
	assert.Equal(t, int64(14), receivedBytes)
}

func TestStorageFactory_UnknownType(t *testing.T) {
	_, err := storage.NewBackend("unknown", nil, "run-id", "server-url", "api-key")
	require.Error(t, err)
}
