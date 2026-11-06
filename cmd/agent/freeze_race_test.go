package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/gorilla/websocket"
)

// A WS server that upgrades and then NEVER reads — simulates a wedged server,
// a stalled proxy, or a saturated link.
func deafWSServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		<-r.Context().Done() // never call ReadMessage
		c.Close()
	}))
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// F1: Agent.writeJSON blocks forever when the peer stops reading, because the
// agent never calls SetWriteDeadline (cmd/agent/main.go:84-91).
func TestAgentFreeze_WriteJSONNeverTimesOut(t *testing.T) {
	srv, wsURL := deafWSServer(t)
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	a := &Agent{runCtxs: make(map[string]context.CancelFunc)}
	a.conn = conn

	big := strings.Repeat("x", 1<<20) // 1 MiB log line
	stuck := make(chan int, 1)
	go func() {
		n := 0
		for {
			if err := a.writeJSON(ws.AgentMessage{Type: ws.MsgTypeAgentLog, Log: big}); err != nil {
				stuck <- -1
				return
			}
			n++
			if n > 500 {
				stuck <- n
				return
			}
		}
	}()

	select {
	case n := <-stuck:
		t.Fatalf("writeJSON did not block (n=%d) — increase payload", n)
	case <-time.After(3 * time.Second):
		t.Log("CONFIRMED: writeJSON is still blocked after 3s with no deadline and no error")
	}

	// F2: while writeJSON holds a.mu, the whole agent's logging deadlocks,
	// because wsLogWriter.Write -> writeJSON -> a.mu is called while the stdlib
	// log package holds its own mutex (cmd/agent/main.go:126,362-369).
	orig := log.Writer()
	log.SetOutput(io.MultiWriter(io.Discard, &wsLogWriter{agent: a}))
	defer log.SetOutput(orig)

	logged := make(chan struct{})
	go func() {
		log.Printf("any log line from any goroutine")
		close(logged)
	}()
	select {
	case <-logged:
		t.Error("log.Printf returned — no freeze")
	case <-time.After(2 * time.Second):
		t.Log("CONFIRMED: log.Printf blocked >2s — every goroutine that logs is now wedged")
	}

	// F3: and cancel_job can no longer be answered — heartbeat too.
	hb := make(chan error, 1)
	go func() { hb <- a.writeJSON(ws.AgentMessage{Type: ws.MsgTypeHeartbeat}) }()
	select {
	case err := <-hb:
		t.Errorf("heartbeat returned: %v", err)
	case <-time.After(1 * time.Second):
		t.Log("CONFIRMED: heartbeat writeJSON blocked -> server sees the agent time out " +
			"while the agent process is alive but frozen")
	}
}
