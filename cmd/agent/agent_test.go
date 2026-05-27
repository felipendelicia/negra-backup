package main

import (
	"testing"

	agentInternal "github.com/felipendelicia/nat-backup/cmd/agent/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_ValidYAML(t *testing.T) {
	cfg, err := agentInternal.LoadAgentConfig("testdata/agent.yaml")
	require.NoError(t, err)
	assert.Equal(t, "https://test.example.com", cfg.ServerURL)
	assert.Equal(t, "test-api-key", cfg.APIKey)
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := agentInternal.LoadAgentConfig("/nonexistent/agent.yaml")
	require.Error(t, err)
}
