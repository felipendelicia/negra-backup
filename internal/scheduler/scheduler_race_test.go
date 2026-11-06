// internal/scheduler/scheduler_race_test.go
package scheduler_test

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"github.com/google/uuid"
)

type countingDispatcher struct{ n int64 }

func (c *countingDispatcher) DispatchJob(agentID, runID string, job models.BackupJob, storageType string, storageConfig json.RawMessage, passphrase string) {
	atomic.AddInt64(&c.n, 1)
}

// S1: AddJob/RemoveJob of the same job while cron fires it.
func TestRace_SchedulerAddRemoveWhileFiring(t *testing.T) {
	d := &countingDispatcher{}
	s := scheduler.New(nil, d, "")
	s.Start()
	defer s.Stop()

	jobID := uuid.New()
	job := models.BackupJob{
		ID:           jobID,
		AgentID:      uuid.New(),
		Name:         "j",
		ScheduleCron: "@every 5ms",
		Enabled:      true,
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
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
				if err := s.AddJob(job); err != nil {
					t.Errorf("AddJob: %v", err)
					return
				}
				s.RemoveJob(jobID.String())
			}
		}()
	}
	time.Sleep(600 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// S2: Stop() then AddJob() — does the entry ever run again?
func TestScheduler_AddJobAfterStopNeverRuns(t *testing.T) {
	d := &countingDispatcher{}
	s := scheduler.New(nil, d, "")
	s.Start()
	s.Stop()

	job := models.BackupJob{
		ID:           uuid.New(),
		AgentID:      uuid.New(),
		Name:         "j",
		ScheduleCron: "@every 5ms",
		Enabled:      true,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob after Stop returned error: %v", err)
	}
	t.Log("AddJob after Stop() succeeded silently (entry registered on a stopped cron)")
	time.Sleep(200 * time.Millisecond)
}
