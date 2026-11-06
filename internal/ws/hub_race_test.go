// internal/ws/hub_race_test.go
package ws_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/ws"
)

// R1: Register/Unregister/DispatchJob hammering on the same agentID.
func TestRace_HubRegisterUnregisterDispatch(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				ac := hub.Register("agent-1", nil)
				hub.Unregister(ac)
			}
		}()
	}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 3000; i++ {
				hub.DispatchJob("agent-1", "run-1", models.BackupJob{}, "local", nil, "")
			}
		}()
	}
	wg.Wait()
}

// R2: SubscribeRunLogs/UnsubscribeRunLogs/BroadcastRunLog on the same runID.
func TestRace_HubRunLogsPubSub(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				ch := hub.SubscribeRunLogs("run-1")
				hub.UnsubscribeRunLogs("run-1", ch)
			}
		}()
	}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				hub.BroadcastRunLog("run-1", "line")
			}
		}()
	}
	wg.Wait()
}

// R3: does the register/unregister ordering leave phantom entries in h.agents?
// Each iteration registers then immediately unregisters the SAME AgentConn.
// After everything drains, IsConnected must be false for every id.
func TestOrdering_RegisterUnregisterLeavesPhantomAgents(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	const n = 200
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("agent-%03d", i)
		ac := hub.Register(id, nil)
		hub.Unregister(ac)
	}

	time.Sleep(500 * time.Millisecond) // let Run drain both channels

	phantom := 0
	for i := 0; i < n; i++ {
		if hub.IsConnected(fmt.Sprintf("agent-%03d", i)) {
			phantom++
		}
	}
	if phantom > 0 {
		t.Errorf("PHANTOM AGENTS: %d/%d disconnected agents still report IsConnected()==true", phantom, n)
	}
}

// R4: after the phantom state, DispatchJob succeeds silently into a dead channel.
func TestOrdering_PhantomAgentSwallowsDispatch(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	found := false
	for i := 0; i < 500 && !found; i++ {
		id := fmt.Sprintf("ghost-%d", i)
		ac := hub.Register(id, nil)
		hub.Unregister(ac)
		time.Sleep(2 * time.Millisecond)
		if hub.IsConnected(id) {
			found = true
			t.Logf("reproduced phantom on iteration %d (id=%s): hub says connected, "+
				"unregister already ran the offline DB update", i, id)
			// dispatch is accepted with no error whatsoever
			hub.DispatchJob(id, "run-x", models.BackupJob{}, "local", nil, "")
		}
	}
	if !found {
		t.Skip("no phantom reproduced in 500 attempts")
	}
}

// R3b: bound the window — with a delay between Register and Unregister the
// Run loop drains register first and no phantom appears.
func TestOrdering_DelayedUnregisterIsClean(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	phantom := 0
	const n = 50
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("slow-%03d", i)
		ac := hub.Register(id, nil)
		time.Sleep(2 * time.Millisecond)
		hub.Unregister(ac)
		time.Sleep(2 * time.Millisecond)
		if hub.IsConnected(id) {
			phantom++
		}
	}
	t.Logf("phantoms with a 2ms gap between Register and Unregister: %d/%d", phantom, n)
}

// R5: does a single DeliverBrowseResult after a timeout block forever?
func TestBrowse_DeliverAfterTimeoutDoesNotBlock(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()
	hub.Register("agent-b", nil)
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// no waiter registered for this id at all
		hub.DeliverBrowseResult("orphan-request", nil, "")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DeliverBrowseResult blocked with no waiter")
	}

	// now with a real waiter that already gave up
	res := make(chan error, 1)
	go func() {
		_, err := hub.BrowsePath("agent-b", "req-1", "/")
		res <- err
	}()
	time.Sleep(50 * time.Millisecond)

	d2 := make(chan struct{})
	go func() {
		defer close(d2)
		hub.DeliverBrowseResult("req-1", nil, "")
		// a SECOND delivery for the same request id (duplicate agent reply)
		hub.DeliverBrowseResult("req-1", nil, "")
	}()
	select {
	case <-d2:
		t.Log("both deliveries returned; no permanent block")
	case <-time.After(3 * time.Second):
		t.Error("BLOCKED: duplicate DeliverBrowseResult for the same requestID blocks forever")
	}
	<-res
}
