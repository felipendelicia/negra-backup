// internal/scheduler/scheduler_test.go
package scheduler_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDispatcher struct{}

func (m *mockDispatcher) DispatchJob(agentID, runID string, job models.BackupJob) {}

func TestScheduler_StartStop(t *testing.T) {
	s := scheduler.New(nil, &mockDispatcher{})
	s.Start()
	s.Stop()
}

func TestScheduler_ParseCron(t *testing.T) {
	valid := []string{"0 2 * * *", "*/5 * * * *", "@daily"}
	for _, expr := range valid {
		err := scheduler.ValidateCron(expr)
		require.NoError(t, err, "expected valid: %s", expr)
	}
	invalid := []string{"not-a-cron", "abc"}
	for _, expr := range invalid {
		err := scheduler.ValidateCron(expr)
		assert.Error(t, err, "expected invalid: %s", expr)
	}
}
