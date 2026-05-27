// internal/models/models_test.go
package models_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgent_APIKeyHiddenFromJSON(t *testing.T) {
	now := time.Now()
	a := models.Agent{
		ID:       uuid.New(),
		Name:     "test-agent",
		APIKey:   "super-secret",
		OS:       "linux",
		Arch:     "amd64",
		Status:   models.AgentStatusOnline,
		LastSeen: &now,
	}
	data, err := json.Marshal(a)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "super-secret")
	assert.Contains(t, string(data), "test-agent")
}

func TestFilesSource_RoundTrip(t *testing.T) {
	src := models.FilesSource{Paths: []string{"/etc", "/home"}}
	data, err := json.Marshal(src)
	require.NoError(t, err)
	var got models.FilesSource
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, src.Paths, got.Paths)
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "online", models.AgentStatusOnline)
	assert.Equal(t, "offline", models.AgentStatusOffline)
	assert.Equal(t, "local", models.StorageTypeLocal)
	assert.Equal(t, "s3", models.StorageTypeS3)
	assert.Equal(t, "sftp", models.StorageTypeSFTP)
	assert.Equal(t, "files", models.JobTypeFiles)
	assert.Equal(t, "postgres", models.JobTypePostgres)
	assert.Equal(t, "mysql", models.JobTypeMySQL)
	assert.Equal(t, "sqlite", models.JobTypeSQLite)
	assert.Equal(t, "mongodb", models.JobTypeMongoDB)
	assert.Equal(t, "running", models.RunStatusRunning)
	assert.Equal(t, "success", models.RunStatusSuccess)
	assert.Equal(t, "failed", models.RunStatusFailed)
}
