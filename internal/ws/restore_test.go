// internal/ws/restore_test.go
package ws_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartRestore_RequiresConnectedAgent(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	err := hub.StartRestore("nobody", ws.ServerMessage{Filename: "b.tar.zst", DestDir: "/tmp/x"})
	require.Error(t, err, "restoring through an agent that is not connected must fail loudly")
	assert.Contains(t, err.Error(), "not connected")
}

func TestStartRestore_DispatchesToConnectedAgent(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	ac := hub.Register("agent-1", nil)
	defer hub.Unregister(ac)
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, hub.StartRestore("agent-1", ws.ServerMessage{
		RestoreID:     "run-1",
		StorageType:   "local",
		StorageConfig: json.RawMessage(`{"path":"/backups"}`),
		Filename:      "backup.tar.zst",
		DestDir:       "/restore/here",
	}))
}

// A disconnected agent must not leave the caller waiting for a listing that will
// never arrive.
func TestListArchive_RequiresConnectedAgent(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	_, err := hub.ListArchive("nobody", "req-1", ws.ServerMessage{Filename: "b.tar.zst"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
}

// DeliverArchiveListing must never block, even when nobody is waiting for that
// request id — a late or duplicate reply from an agent would otherwise park the
// read loop of that connection forever.
func TestDeliverArchiveListing_UnknownRequestDoesNotBlock(t *testing.T) {
	hub := ws.NewHub(nil)
	go hub.Run()

	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.DeliverArchiveListing("no-such-request", []ws.ArchiveEntry{{Name: "x"}}, "")
		// A second delivery for the same id, as a duplicate reply would be.
		hub.DeliverArchiveListing("no-such-request", nil, "boom")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DeliverArchiveListing blocked on an unknown request id")
	}
}
