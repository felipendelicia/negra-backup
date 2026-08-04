// internal/scheduler/scheduler.go
package scheduler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/robfig/cron/v3"
)

const (
	// AttemptFirst is the attempt number of a job's first execution. Retries count up
	// from here.
	AttemptFirst = 1

	// attemptSkipped marks a run row that was recorded but never dispatched, so the
	// retry scanner can tell "this failed" from "this never ran".
	attemptSkipped = 0

	// retryScanInterval is how often pending retries are looked for. The resolution
	// of a retry is therefore ±30s, which is irrelevant next to a backoff measured in
	// minutes and keeps the query off the database the rest of the time.
	retryScanInterval = 30 * time.Second

	// retryGracePeriod bounds how late a retry may fire. Without it a server that was
	// down for two days would come back and immediately dispatch every retry that
	// came due while it was off — a thundering herd of backups nobody asked for at a
	// time nobody chose. Past this window the failure stands as recorded.
	retryGracePeriod = time.Hour

	// retryBackoffMaxShift caps the exponential so the delay stops doubling at 64x the
	// base. At the maximum retry_backoff_seconds this is still a bounded wait rather
	// than a retry scheduled a week out.
	retryBackoffMaxShift = 6

	// pgUniqueViolation is the SQLSTATE raised by the partial unique index that
	// enforces one running run per job.
	pgUniqueViolation = "23505"
)

// ErrRunOverlap reports that another run of the same job is already in progress, so
// no new run was started.
var ErrRunOverlap = errors.New("a previous run of this job is still in progress")

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

	// done stops the retry scanner. stopOnce keeps Stop idempotent — it is called
	// from tests and from shutdown paths that may both fire.
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
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
		done:       make(chan struct{}),
	}
}

func (s *Scheduler) Start() {
	if s.db != nil {
		s.loadJobs()
		s.wg.Add(1)
		go s.retryLoop()
	}
	s.cron.Start()
	log.Println("scheduler started")
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
	s.stopOnce.Do(func() { close(s.done) })
	s.wg.Wait()
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
		s.triggerJob(job, AttemptFirst)
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

// BeginRun claims the single "in progress" slot for a job and returns the new run.
// It returns ErrRunOverlap when another run of the same job is already running.
//
// Two layers, on purpose:
//
//   - WHERE NOT EXISTS handles the common case cheaply and yields a clean "nothing
//     was inserted" signal (sql.ErrNoRows) instead of an error to classify.
//   - the partial unique index idx_backup_runs_one_running_per_job (migration 004) is
//     what actually makes this safe. Under READ COMMITTED the NOT EXISTS subquery
//     reads the snapshot taken when the statement began, so two concurrent inserts
//     can both see "nothing running" and both succeed. The index cannot be fooled:
//     the second writer blocks until the first commits and then fails with 23505.
//
// The guard lives in the database rather than in a mutex on Scheduler because a
// mutex dies with the process: after a restart a run left in flight would no longer
// be protected by it, and two servers pointed at one database never would be.
func BeginRun(db *sqlx.DB, jobID uuid.UUID, attempt int) (models.BackupRun, error) {
	var run models.BackupRun
	err := db.QueryRowx(`
		INSERT INTO backup_runs (job_id, status, attempt)
		SELECT $1, 'running', $2
		WHERE NOT EXISTS (
		    SELECT 1 FROM backup_runs WHERE job_id = $1 AND status = 'running'
		)
		RETURNING id, job_id, started_at, status, attempt`,
		jobID, attempt,
	).StructScan(&run)
	switch {
	case errors.Is(err, sql.ErrNoRows), isUniqueViolation(err):
		return models.BackupRun{}, ErrRunOverlap
	case err != nil:
		return models.BackupRun{}, err
	}
	return run, nil
}

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && string(pqErr.Code) == pgUniqueViolation
}

func (s *Scheduler) triggerJob(job models.BackupJob, attempt int) {
	if s.db == nil {
		return
	}

	// Cheap pre-check. It is not the guard — BeginRun's index is — but it stops a job
	// that is still running from decrypting a passphrase and reading a destination
	// for nothing on every cron tick.
	if s.jobIsRunning(job.ID) {
		s.recordSkippedRun(job)
		return
	}

	// Everything that can fail is resolved before the run row exists. Inserting
	// first and hoping meant an offline agent, an unreadable destination or an
	// undecryptable passphrase left a row stuck in 'running' with nobody to close
	// it: invisible to retention, and a Cancel button in the UI that did nothing.
	storageType, storageConfig, err := s.resolveStorage(job)
	if err != nil {
		s.recordFailedRun(job, attempt, fmt.Sprintf("storage destination unavailable: %v", err))
		return
	}

	passphrase, err := s.resolvePassphrase(job)
	if err != nil {
		// Dispatching anyway would produce a plaintext archive named ".enc".
		s.recordFailedRun(job, attempt, fmt.Sprintf("backup passphrase unavailable: %v", err))
		return
	}

	run, err := BeginRun(s.db, job.ID, attempt)
	if errors.Is(err, ErrRunOverlap) {
		s.recordSkippedRun(job)
		return
	}
	if err != nil {
		log.Printf("scheduler create run for job %s: %v", job.ID, err)
		return
	}
	runID := run.ID.String()

	log.Printf("scheduler dispatching job %s (run %s, attempt %d) to agent %s",
		job.ID, runID, attempt, job.AgentID)
	if err := s.dispatcher.DispatchJob(
		job.AgentID.String(), runID, job, storageType, storageConfig, passphrase,
	); err != nil {
		log.Printf("scheduler dispatch job %s: %v", job.ID, err)
		s.failRun(runID, err.Error())
	}
}

// jobIsRunning is advisory only: a false answer costs a wasted decrypt, never a
// duplicate run, because BeginRun re-checks atomically.
func (s *Scheduler) jobIsRunning(jobID uuid.UUID) bool {
	var running bool
	if err := s.db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM backup_runs WHERE job_id = $1 AND status = 'running')`,
		jobID,
	).Scan(&running); err != nil {
		log.Printf("scheduler overlap check job %s: %v", jobID, err)
		return false
	}
	return running
}

// retryLoop re-dispatches failed runs whose backoff has elapsed.
//
// Design note. The obvious implementation — time.AfterFunc, or a one-shot cron entry
// per failure — was rejected on two counts. A timer holds the pending retry in this
// process's memory, so a restart drops every scheduled retry silently, which is the
// exact class of bug this feature exists to fix. And a job that fails on a tight
// schedule stacks one live timer or goroutine per failure with nothing cancelling
// them.
//
// Here the pending state IS the failed row in backup_runs. One goroutine for the
// whole scheduler, started in Start and joined in Stop; nothing to leak. After a
// restart the same query finds the same rows and the retries resume on their own,
// bounded by retryGracePeriod so a long outage does not produce a stampede.
//
// It also cannot fight the overlap guard from migration 004: a retry goes through the
// same triggerJob -> BeginRun path as everything else, so if a run of that job is in
// progress the retry is skipped and reconsidered on the next tick.
func (s *Scheduler) retryLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(retryScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.scanRetries()
		}
	}
}

// retryCandidateQuery finds failed runs that are due for another attempt.
//
//   - attempt >= 1 skips rows recorded without ever being dispatched (overlap skips):
//     there is nothing to retry, the run that caused the skip is still going.
//   - attempt <= retry_max makes retry_max the number of RETRIES, so a job with
//     retry_max = 2 executes at most three times.
//   - the NOT EXISTS clause keeps only the newest run of the job. Once a retry has
//     been created the run it came from stops being a candidate, which is what makes
//     the scan idempotent between ticks and safe to run after a restart.
//   - $1 caps the exponent, $2 is the grace window in seconds.
const retryCandidateQuery = `
SELECT r.id, r.job_id, r.attempt
FROM backup_runs r
JOIN backup_jobs j ON j.id = r.job_id
WHERE r.status = 'failed'
  AND r.attempt >= 1
  AND r.finished_at IS NOT NULL
  AND j.enabled = true
  AND j.retry_max > 0
  AND r.attempt <= j.retry_max
  AND NOW() >= r.finished_at + make_interval(secs =>
        (j.retry_backoff_seconds * POWER(2, LEAST(r.attempt - 1, $1::int)))::double precision)
  AND NOW() <  r.finished_at + make_interval(secs =>
        (j.retry_backoff_seconds * POWER(2, LEAST(r.attempt - 1, $1::int)) + $2::int)::double precision)
  AND NOT EXISTS (
      SELECT 1 FROM backup_runs newer
      WHERE newer.job_id = r.job_id
        AND (newer.started_at, newer.id) > (r.started_at, r.id)
  )
ORDER BY r.finished_at`

type retryCandidate struct {
	RunID   uuid.UUID `db:"id"`
	JobID   uuid.UUID `db:"job_id"`
	Attempt int       `db:"attempt"`
}

func (s *Scheduler) scanRetries() {
	var candidates []retryCandidate
	if err := s.db.Select(&candidates, retryCandidateQuery,
		retryBackoffMaxShift, int(retryGracePeriod.Seconds()),
	); err != nil {
		log.Printf("scheduler scan retries: %v", err)
		return
	}
	for _, c := range candidates {
		var job models.BackupJob
		if err := s.db.Get(&job, `SELECT * FROM backup_jobs WHERE id=$1`, c.JobID); err != nil {
			log.Printf("scheduler retry: load job %s: %v", c.JobID, err)
			continue
		}
		next := c.Attempt + 1
		log.Printf("scheduler retrying job %s after failed run %s (attempt %d, %d allowed)",
			job.ID, c.RunID, next, job.RetryMax+1)
		s.triggerJob(job, next)
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
// of the job silently not running. It carries the attempt number so a setup failure —
// an unreachable destination is very often transient — is retried like any other.
func (s *Scheduler) recordFailedRun(job models.BackupJob, attempt int, msg string) {
	log.Printf("scheduler job %s: %s", job.ID, msg)
	if _, err := s.db.Exec(
		`INSERT INTO backup_runs (job_id, status, attempt, finished_at, error_message)
		 VALUES ($1, 'failed', $2, NOW(), $3)`, job.ID, attempt, msg,
	); err != nil {
		log.Printf("scheduler record failed run for job %s: %v", job.ID, err)
	}
}

// recordSkippedRun leaves a trace of an execution that never happened. A log line
// alone dies with the process; a terminal run row shows up in the Runs list, is
// swept by retention like any other, and makes an unachievable schedule — a daily
// backup that takes more than a day — visible instead of silent.
//
// It is stored with attempt = attemptSkipped so the retry scanner ignores it: there
// is nothing to retry here, the run that caused the overlap is still going and will
// be retried on its own if it fails.
func (s *Scheduler) recordSkippedRun(job models.BackupJob) {
	msg := "Skipped: " + ErrRunOverlap.Error()
	log.Printf("scheduler job %s: %s", job.ID, msg)
	if _, err := s.db.Exec(
		`INSERT INTO backup_runs (job_id, status, attempt, finished_at, error_message)
		 VALUES ($1, 'failed', $2, NOW(), $3)`, job.ID, attemptSkipped, msg,
	); err != nil {
		log.Printf("scheduler record skipped run for job %s: %v", job.ID, err)
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
