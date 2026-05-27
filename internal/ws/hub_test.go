// internal/ws/hub_test.go
package ws_test

import (
	"sync"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/stretchr/testify/assert"
)

func TestHub_RegisterUnregister(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	hub.Register("agent-1", nil)
	time.Sleep(10 * time.Millisecond)
	assert.True(t, hub.IsConnected("agent-1"))

	hub.Unregister("agent-1")
	time.Sleep(20 * time.Millisecond)
	assert.False(t, hub.IsConnected("agent-1"))
}

func TestHub_SubscribeLogsFanOut(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	ch1 := hub.SubscribeRunLogs("run-abc")
	ch2 := hub.SubscribeRunLogs("run-abc")
	defer hub.UnsubscribeRunLogs("run-abc", ch1)
	defer hub.UnsubscribeRunLogs("run-abc", ch2)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		select {
		case msg := <-ch1:
			assert.Equal(t, "hello", msg)
		case <-time.After(time.Second):
			t.Error("timeout ch1")
		}
	}()

	go func() {
		defer wg.Done()
		select {
		case msg := <-ch2:
			assert.Equal(t, "hello", msg)
		case <-time.After(time.Second):
			t.Error("timeout ch2")
		}
	}()

	hub.BroadcastRunLog("run-abc", "hello")
	wg.Wait()
}
