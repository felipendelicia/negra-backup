// internal/api/storage_test.go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/stretchr/testify/assert"
)

func TestListStorage_NoAuth(t *testing.T) {
	srv, _, _ := api.NewServer(nil, testConfig())
	req := httptest.NewRequest(http.MethodGet, "/api/storage-destinations", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
