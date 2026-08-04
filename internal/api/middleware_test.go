// internal/api/middleware_test.go
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The live run-log WebSocket sat behind a header-only check, which a browser can
// never satisfy on a handshake, so the feature answered 401 forever. The query
// fallback must apply to the WebSocket routes and nothing else.
func TestIsWebSocketRoute(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		upgrade string
		want    bool
	}{
		{"run logs socket upgrading", "/api/runs/abc/logs/ws", "websocket", true},
		{"run logs socket, case-insensitive header", "/api/runs/abc/logs/ws", "WebSocket", true},
		{"run logs socket without upgrade header", "/api/runs/abc/logs/ws", "", false},
		{"plain REST route with upgrade header", "/api/runs", "websocket", false},
		{"historic logs endpoint is not the socket", "/api/runs/abc/logs", "websocket", false},
		{"unrelated route", "/api/jobs", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
			}
			assert.Equal(t, tc.want, isWebSocketRoute(r))
		})
	}
}

// A token in the query string must not be accepted on ordinary API routes.
func TestJWTAuthMiddleware_RejectsQueryTokenOnRESTRoutes(t *testing.T) {
	s := &Server{}
	handler := s.jwtAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/api/jobs?token=whatever", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
