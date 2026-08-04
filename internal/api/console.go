// internal/api/console.go
package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

const consoleHistorySize = 500

// ConsoleHub broadcasts server log lines to WebSocket subscribers.
// It implements io.Writer so it can be used with log.SetOutput.
// It keeps a ring buffer of the last consoleHistorySize lines so new
// subscribers receive recent history immediately on connect.
type ConsoleHub struct {
	mu      sync.RWMutex
	subs    map[chan []byte]struct{}
	history [][]byte // ring buffer
	head    int      // next write position
	full    bool     // ring has wrapped at least once
}

func NewConsoleHub() *ConsoleHub {
	return &ConsoleHub{
		subs:    make(map[chan []byte]struct{}),
		history: make([][]byte, consoleHistorySize),
	}
}

// Write implements io.Writer — appends to history and broadcasts to subscribers.
func (h *ConsoleHub) Write(p []byte) (int, error) {
	line := make([]byte, len(p))
	copy(line, p)

	h.mu.Lock()
	h.history[h.head] = line
	h.head = (h.head + 1) % consoleHistorySize
	if h.head == 0 {
		h.full = true
	}
	// The fan-out stays under the lock. Collecting the subscribers and releasing
	// first left a window in which unsubscribe could close a channel this loop was
	// about to send on. Since Write is the io.Writer behind the global logger, that
	// panic could fire from any goroutine — including the cron scheduler and the
	// agent write pumps, which have no recover and take the process down with them.
	// The sends are non-blocking, so holding the lock adds no latency.
	for ch := range h.subs {
		select {
		case ch <- line:
		default: // drop if subscriber is slow
		}
	}
	h.mu.Unlock()

	return len(p), nil
}

// snapshot returns buffered history lines in order (oldest first).
func (h *ConsoleHub) snapshot() [][]byte {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var lines [][]byte
	if h.full {
		// wrap: from head to end, then 0 to head
		for i := h.head; i < consoleHistorySize; i++ {
			if h.history[i] != nil {
				lines = append(lines, h.history[i])
			}
		}
		for i := 0; i < h.head; i++ {
			if h.history[i] != nil {
				lines = append(lines, h.history[i])
			}
		}
	} else {
		for i := 0; i < h.head; i++ {
			if h.history[i] != nil {
				lines = append(lines, h.history[i])
			}
		}
	}
	return lines
}

func (h *ConsoleHub) subscribe() chan []byte {
	ch := make(chan []byte, 512)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *ConsoleHub) unsubscribe(ch chan []byte) {
	// close must happen under the same lock that guards the fan-out in Write,
	// otherwise a concurrent broadcast can send on the closed channel.
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, ch)
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
	// This socket is read-only for the client; anything it sends is a control frame
	// at most. Bound it so a client cannot stream unbounded data into the server.
	conn.SetReadLimit(4096)

	ch := s.consoleHub.subscribe()
	defer s.consoleHub.unsubscribe(ch)

	// Replay history to the new subscriber
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	for _, line := range s.consoleHub.snapshot() {
		if err := conn.WriteMessage(websocket.TextMessage, line); err != nil {
			return
		}
	}

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
