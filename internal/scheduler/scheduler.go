// internal/scheduler/scheduler.go
package scheduler

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/jmoiron/sqlx"
	"github.com/robfig/cron/v3"
)

type JobDispatcher interface {
	DispatchJob(agentID, runID string, job models.BackupJob, storageType string, storageConfig json.RawMessage)
}

type Scheduler struct {
	cron       *cron.Cron
	db         *sqlx.DB
	dispatcher JobDispatcher
	mu         sync.Mutex
	entryIDs   map[string]cron.EntryID
	encKey     string
}

func New(db *sqlx.DB, dispatcher JobDispatcher, encKey string) *Scheduler {
	return &Scheduler{
		cron:       cron.New(),
		db:         db,
		dispatcher: dispatcher,
		entryIDs:   make(map[string]cron.EntryID),
		encKey:     encKey,
	}
}

func (s *Scheduler) Start() {
	if s.db != nil {
		s.loadJobs()
	}
	s.cron.Start()
	log.Println("scheduler started")
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

func (s *Scheduler) loadJobs() {
	var jobs []models.BackupJob
	if err := s.db.Select(&jobs, `SELECT * FROM backup_jobs WHERE enabled=true`); err != nil {
		log.Printf("scheduler load jobs: %v", err)
		return
	}
	for _, job := range jobs {
		if err := s.AddJob(job); err != nil {
			log.Printf("scheduler add job %s: %v", job.ID, err)
		}
	}
	log.Printf("scheduler loaded %d jobs", len(jobs))
}

func (s *Scheduler) AddJob(job models.BackupJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.entryIDs[job.ID.String()]; ok {
		s.cron.Remove(id)
		delete(s.entryIDs, job.ID.String())
	}
	if !job.Enabled {
		return nil
	}
	entryID, err := s.cron.AddFunc(job.ScheduleCron, func() {
		s.triggerJob(job)
	})
	if err != nil {
		return fmt.Errorf("cron add: %w", err)
	}
	s.entryIDs[job.ID.String()] = entryID
	return nil
}

func (s *Scheduler) RemoveJob(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.entryIDs[jobID]; ok {
		s.cron.Remove(id)
		delete(s.entryIDs, jobID)
	}
}

func (s *Scheduler) triggerJob(job models.BackupJob) {
	if s.db == nil {
		return
	}

	var runID string
	err := s.db.QueryRow(
		`INSERT INTO backup_runs (job_id, status) VALUES ($1, 'running') RETURNING id`,
		job.ID,
	).Scan(&runID)
	if err != nil {
		log.Printf("scheduler create run for job %s: %v", job.ID, err)
		return
	}

	// Fetch storage destination
	var destType string
	var encryptedConfig string
	var storageType string
	var storageConfig json.RawMessage

	if err := s.db.QueryRow(
		`SELECT type, config FROM storage_destinations WHERE id = $1`,
		job.StorageDestinationID,
	).Scan(&destType, &encryptedConfig); err != nil {
		log.Printf("scheduler fetch storage for job %s: %v", job.ID, err)
	} else if s.encKey != "" {
		decrypted, decErr := crypto.Decrypt(s.encKey, encryptedConfig)
		if decErr != nil {
			log.Printf("scheduler decrypt storage for job %s: %v", job.ID, decErr)
		} else {
			storageType = destType
			storageConfig = json.RawMessage(decrypted)
		}
	}

	log.Printf("scheduler dispatching job %s (run %s) to agent %s", job.ID, runID, job.AgentID)
	s.dispatcher.DispatchJob(job.AgentID.String(), runID, job, storageType, storageConfig)
}

// ValidateCron returns an error if the cron expression is invalid.
func ValidateCron(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	_, err := parser.Parse(expr)
	return err
}
