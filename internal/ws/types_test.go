// internal/ws/types_test.go
package ws_test

import (
	"encoding/json"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelloMessage_RoundTrip(t *testing.T) {
	msg := ws.AgentMessage{
		Type:    ws.MsgTypeHello,
		APIKey:  "abc123",
		OS:      "linux",
		Arch:    "amd64",
		Version: "1.0.0",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	var got ws.AgentMessage
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, ws.MsgTypeHello, got.Type)
	assert.Equal(t, "linux", got.OS)
}

func TestServerMessage_RunJob(t *testing.T) {
	msg := ws.ServerMessage{
		Type:  ws.MsgTypeRunJob,
		RunID: "run-uuid-123",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "run_job")
	assert.Contains(t, string(data), "run-uuid-123")
}

func TestMessageTypeConstants(t *testing.T) {
	assert.Equal(t, "hello", ws.MsgTypeHello)
	assert.Equal(t, "heartbeat", ws.MsgTypeHeartbeat)
	assert.Equal(t, "job_progress", ws.MsgTypeJobProgress)
	assert.Equal(t, "job_done", ws.MsgTypeJobDone)
	assert.Equal(t, "job_failed", ws.MsgTypeJobFailed)
	assert.Equal(t, "run_job", ws.MsgTypeRunJob)
}
