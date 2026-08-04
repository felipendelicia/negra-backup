// internal/ws/hub_concurrency_test.go
package ws_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/ws"
)

// Every test here used to panic with "send on closed channel". They exercise the
// paths that the existing sequential tests never touched: a sender racing the
// goroutine that tears the subscriber down. Run with -race.

func TestHub_DispatchRacesUnregister(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	const rounds = 300
	var wg sync.WaitGroup

	// Registering and unregistering the same agent id in a tight loop while
	// dispatches keep arriving is exactly what happens when an agent flaps.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			ac := hub.Register("agent-1", nil)
			hub.Unregister(ac)
		}
	}()

	wg.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			job := models.BackupJob{Name: "job"}
			for j := 0; j < rounds; j++ {
				hub.DispatchJob("agent-1", "run-1", job, "local", json.RawMessage(`{}`), "")
				hub.UpdateAgent("agent-1")
				hub.CancelJob("agent-1", "run-1")
			}
		}()
	}

	wg.Wait()
}

func TestHub_BroadcastRacesUnsubscribe(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	const rounds = 500
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			ch := hub.SubscribeRunLogs("run-1")
			// Drain so the buffer never fills; the bug fires on the send, not the fill.
			go func() {
				for range ch {
				}
			}()
			hub.UnsubscribeRunLogs("run-1", ch)
		}
	}()

	wg.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				hub.BroadcastRunLog("run-1", "log line")
			}
		}()
	}

	wg.Wait()
}

// A stale Unregister from a previous connection must not tear down the connection
// that replaced it.
func TestHub_StaleUnregisterKeepsNewerConnection(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	old := hub.Register("agent-1", nil)
	time.Sleep(20 * time.Millisecond)
	fresh := hub.Register("agent-1", nil)
	time.Sleep(20 * time.Millisecond)

	hub.Unregister(old)
	time.Sleep(20 * time.Millisecond)

	if !hub.IsConnected("agent-1") {
		t.Fatal("stale unregister removed the newer connection")
	}

	hub.Unregister(fresh)
	time.Sleep(20 * time.Millisecond)

	if hub.IsConnected("agent-1") {
		t.Fatal("agent still connected after unregistering the live connection")
	}
}
