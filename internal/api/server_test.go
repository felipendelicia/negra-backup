// internal/api/server_test.go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestServer_Health(t *testing.T) {
	cfg := config.Config{
		JWTSecret:     "test-secret-32-chars-min-xxxxxxxx",
		EncryptionKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	srv, _, _ := api.NewServer(nil, cfg)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
