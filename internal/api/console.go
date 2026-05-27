// internal/api/console.go
package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// ConsoleHub broadcasts server log lines to WebSocket subscribers.
// It implements io.Writer so it can be used with log.SetOutput.
type ConsoleHub struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}
}

func NewConsoleHub() *ConsoleHub {
	return &ConsoleHub{subs: make(map[chan []byte]struct{})}
}

// Write implements io.Writer — broadcasts each log line to all subscribers.
func (h *ConsoleHub) Write(p []byte) (int, error) {
	line := make([]byte, len(p))
	copy(line, p)

	h.mu.RLock()
	for ch := range h.subs {
		select {
		case ch <- line:
		default: // drop if subscriber is slow
		}
	}
	h.mu.RUnlock()
	return len(p), nil
}

func (h *ConsoleHub) subscribe() chan []byte {
	ch := make(chan []byte, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *ConsoleHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

// validateJWTString parses and validates a raw JWT string.
func (s *Server) validateJWTString(tokenStr string) bool {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(s.cfg.JWTSecret), nil
	})
	return err == nil && token.Valid
}

var consoleUpgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  256,
	WriteBufferSize: 4096,
}

// handleConsoleWS streams server logs to the browser over WebSocket.
// Auth: JWT in ?token= query param (browsers can't set WS headers).
func (s *Server) handleConsoleWS(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" || !s.validateJWTString(tokenStr) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := consoleUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch := s.consoleHub.subscribe()
	defer s.consoleHub.unsubscribe(ch)

	// Ping to keep connection alive
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case line, ok := <-ch:
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, line); err != nil {
				return
			}
		}
	}
}
