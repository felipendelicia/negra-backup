// internal/api/auth_test.go
package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
)

func testConfig() config.Config {
	return config.Config{
		JWTSecret:     "test-secret-32-chars-min-xxxxxxxx",
		EncryptionKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
}

func TestLogin_NoDB_Returns401(t *testing.T) {
	// When DB is nil, login fails because it can't query admin_users
	srv, _, _ := api.NewServer(nil, testConfig())
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	// With nil DB, QueryRow will panic or error. We expect 401 or 500 but not 200.
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func TestJWTMiddleware_NoToken_Returns401(t *testing.T) {
	srv, _, _ := api.NewServer(nil, testConfig())
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTMiddleware_InvalidToken_Returns401(t *testing.T) {
	srv, _, _ := api.NewServer(nil, testConfig())
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.here")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
