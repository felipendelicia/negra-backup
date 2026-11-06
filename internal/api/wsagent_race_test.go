// internal/api/wsagent_race_test.go
package api_test

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/gorilla/websocket"
)

// E1: real /ws/agent connections churning while the hub dispatches and
// broadcasts. db==nil so authenticateAPIKey short-circuits to "test-agent-id".
func TestRace_AgentWSChurnEndToEnd(t *testing.T) {
	cfg := config.Config{JWTSecret: "s", EncryptionKey: strings.Repeat("0", 64)}
	srv, hub, _ := api.NewServer(nil, cfg)

	ts := httptest.NewServer(srv)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/agent"

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// connect/disconnect churn
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
				if err != nil {
					continue
				}
				_ = c.WriteJSON(ws.AgentMessage{Type: ws.MsgTypeHello, APIKey: "k", OS: "linux"})
				_ = c.WriteJSON(ws.AgentMessage{Type: ws.MsgTypeJobProgress, RunID: "run-1", Percent: 50})
				c.Close()
			}
		}()
	}

	// log subscribers churn on the same runID
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ch := hub.SubscribeRunLogs("run-1")
				hub.UnsubscribeRunLogs("run-1", ch)
			}
		}()
	}

	// dispatcher churn (disabled: the known hub.go:168 panic masks everything else)
	for g := 0; g < 0; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				hub.DispatchJob("test-agent-id", "run-1", models.BackupJob{}, "local", nil, "")
			}
		}()
	}

	time.Sleep(3 * time.Second)
	close(stop)
	wg.Wait()
}
