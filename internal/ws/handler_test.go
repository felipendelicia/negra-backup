// internal/ws/handler_test.go
package ws_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/stretchr/testify/assert"
)

func TestAgentHandler_RejectsNonWS(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()
	handler := ws.NewAgentHandler(hub)

	req := httptest.NewRequest(http.MethodGet, "/ws/agent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Not a WS request — upgrader returns 400
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
