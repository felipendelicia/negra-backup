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
	DispatchJob(agentID, runID string, job models.BackupJob, storageType string, storageConfig json.RawMessage, passphrase string) error
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
		// Without cron.Recover a panic inside a job kills the whole server: cron
		// runs each job in a bare goroutine, where no HTTP middleware can catch it.
		cron:       cron.New(cron.WithChain(cron.Recover(cron.DefaultLogger))),
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

	// Everything that can fail is resolved before the run row exists. Inserting
	// first and hoping meant an offline agent, an unreadable destination or an
	// undecryptable passphrase left a row stuck in 'running' with nobody to close
	// it: invisible to retention, and a Cancel button in the UI that did nothing.
	storageType, storageConfig, err := s.resolveStorage(job)
	if err != nil {
		s.recordFailedRun(job, fmt.Sprintf("storage destination unavailable: %v", err))
		return
	}

	passphrase, err := s.resolvePassphrase(job)
	if err != nil {
		// Dispatching anyway would produce a plaintext archive named ".enc".
		s.recordFailedRun(job, fmt.Sprintf("backup passphrase unavailable: %v", err))
		return
	}

	var runID string
	if err := s.db.QueryRow(
		`INSERT INTO backup_runs (job_id, status) VALUES ($1, 'running') RETURNING id`,
		job.ID,
	).Scan(&runID); err != nil {
		log.Printf("scheduler create run for job %s: %v", job.ID, err)
		return
	}

	log.Printf("scheduler dispatching job %s (run %s) to agent %s", job.ID, runID, job.AgentID)
	if err := s.dispatcher.DispatchJob(
		job.AgentID.String(), runID, job, storageType, storageConfig, passphrase,
	); err != nil {
		log.Printf("scheduler dispatch job %s: %v", job.ID, err)
		s.failRun(runID, err.Error())
	}
}

// resolveStorage loads and decrypts the job's storage destination.
func (s *Scheduler) resolveStorage(job models.BackupJob) (string, json.RawMessage, error) {
	var destType, encryptedConfig string
	if err := s.db.QueryRow(
		`SELECT type, config FROM storage_destinations WHERE id = $1`,
		job.StorageDestinationID,
	).Scan(&destType, &encryptedConfig); err != nil {
		return "", nil, err
	}
	if s.encKey == "" {
		return "", nil, fmt.Errorf("no encryption key configured")
	}
	decrypted, err := crypto.Decrypt(s.encKey, encryptedConfig)
	if err != nil {
		return "", nil, fmt.Errorf("decrypt storage config: %w", err)
	}
	return destType, json.RawMessage(decrypted), nil
}

// resolvePassphrase decrypts the job's backup passphrase. For an encrypted job a
// missing or undecryptable passphrase is fatal, never an empty string.
func (s *Scheduler) resolvePassphrase(job models.BackupJob) (string, error) {
	if !job.Encrypt {
		return "", nil
	}
	if job.EncryptPassphrase == nil {
		return "", fmt.Errorf("job is encrypted but has no stored passphrase")
	}
	decPass, err := crypto.Decrypt(s.encKey, *job.EncryptPassphrase)
	if err != nil {
		return "", fmt.Errorf("decrypt passphrase: %w", err)
	}
	if decPass == "" {
		return "", fmt.Errorf("stored passphrase is empty")
	}
	return decPass, nil
}

// recordFailedRun writes a terminal run so the failure is visible in the UI instead
// of the job silently not running.
func (s *Scheduler) recordFailedRun(job models.BackupJob, msg string) {
	log.Printf("scheduler job %s: %s", job.ID, msg)
	if _, err := s.db.Exec(
		`INSERT INTO backup_runs (job_id, status, finished_at, error_message)
		 VALUES ($1, 'failed', NOW(), $2)`, job.ID, msg,
	); err != nil {
		log.Printf("scheduler record failed run for job %s: %v", job.ID, err)
	}
}

func (s *Scheduler) failRun(runID, msg string) {
	if _, err := s.db.Exec(
		`UPDATE backup_runs SET status='failed', finished_at=NOW(), error_message=$2 WHERE id=$1`,
		runID, msg,
	); err != nil {
		log.Printf("scheduler fail run %s: %v", runID, err)
	}
}

// ValidateCron returns an error if the cron expression is invalid.
func ValidateCron(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	_, err := parser.Parse(expr)
	return err
}
