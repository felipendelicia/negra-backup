// internal/api/jobs.go
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type createJobRequest struct {
	AgentID              uuid.UUID       `json:"agent_id"`
	Name                 string          `json:"name"`
	Enabled              bool            `json:"enabled"`
	Type                 string          `json:"type"`
	Source               json.RawMessage `json:"source"`
	StorageDestinationID uuid.UUID       `json:"storage_destination_id"`
	ScheduleCron         string          `json:"schedule_cron"`
	RetentionDays        int             `json:"retention_days"`
	Compression          string          `json:"compression"`
	Encrypt              bool            `json:"encrypt"`
	// EncryptPassphrase is a pointer so an absent field ("keep the stored one")
	// is distinguishable from an explicit empty string ("clear it"). On update,
	// a nil value leaves the stored passphrase untouched.
	EncryptPassphrase *string `json:"encrypt_passphrase,omitempty"`
	// Retry policy. Pointers for the same reason as the passphrase: a client that
	// does not know about these fields yet (the current UI does not send them) must
	// not silently reset a policy set through the API. Absent means "the default" on
	// create and "keep what is stored" on update.
	RetryMax            *int `json:"retry_max,omitempty"`
	RetryBackoffSeconds *int `json:"retry_backoff_seconds,omitempty"`
}

// Retry policy bounds. They mirror the CHECK constraints added by migration 004 so a
// bad value comes back as a 400 instead of a 500 from the database driver.
const (
	defaultRetryMax            = 0
	defaultRetryBackoffSeconds = 300
	maxRetryMax                = 10
	minRetryBackoffSeconds     = 10
	maxRetryBackoffSeconds     = 86400
)

// validateRetryPolicy checks the fields the request actually set. Nil is always
// valid: the caller decides whether nil means "default" or "keep stored".
func validateRetryPolicy(req *createJobRequest) error {
	if req.RetryMax != nil && (*req.RetryMax < 0 || *req.RetryMax > maxRetryMax) {
		return fmt.Errorf("retry_max must be between 0 and %d", maxRetryMax)
	}
	if req.RetryBackoffSeconds != nil &&
		(*req.RetryBackoffSeconds < minRetryBackoffSeconds || *req.RetryBackoffSeconds > maxRetryBackoffSeconds) {
		return fmt.Errorf("retry_backoff_seconds must be between %d and %d",
			minRetryBackoffSeconds, maxRetryBackoffSeconds)
	}
	return nil
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs := []models.BackupJob{}
	if err := s.db.Select(&jobs, `
		SELECT id, agent_id, name, enabled, type, source, storage_destination_id,
		       schedule_cron, retention_days, compression, encrypt,
		       retry_max, retry_backoff_seconds, created_at, updated_at
		FROM backup_jobs ORDER BY created_at DESC`); err != nil {
		log.Printf("handleListJobs: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusOK, jobs)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" || req.Type == "" || req.ScheduleCron == "" {
		respondError(w, http.StatusBadRequest, "name, type, schedule_cron required")
		return
	}
	if err := scheduler.ValidateCron(req.ScheduleCron); err != nil {
		respondError(w, http.StatusBadRequest, "invalid cron expression: "+err.Error())
		return
	}
	if req.Compression == "" {
		req.Compression = models.CompressionZstd
	}
	if req.RetentionDays <= 0 {
		req.RetentionDays = 30
	}
	if err := validateRetryPolicy(&req); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Conservative defaults: a job created without a retry policy behaves exactly as
	// jobs did before migration 004 — one attempt, no retry.
	retryMax := defaultRetryMax
	if req.RetryMax != nil {
		retryMax = *req.RetryMax
	}
	retryBackoff := defaultRetryBackoffSeconds
	if req.RetryBackoffSeconds != nil {
		retryBackoff = *req.RetryBackoffSeconds
	}
	// An encrypted job without a passphrase would produce plaintext archives named
	// ".enc", so refuse it here rather than degrade silently downstream.
	if req.Encrypt && (req.EncryptPassphrase == nil || *req.EncryptPassphrase == "") {
		respondError(w, http.StatusBadRequest, "encrypt_passphrase is required when encrypt is enabled")
		return
	}
	var encPassphrase *string
	if req.Encrypt {
		enc, err := crypto.Encrypt(s.cfg.EncryptionKey, *req.EncryptPassphrase)
		if err != nil {
			log.Printf("handleCreateJob encrypt passphrase: %v", err)
			respondError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encPassphrase = &enc
	}
	var job models.BackupJob
	err := s.db.QueryRowx(`
		INSERT INTO backup_jobs
		  (agent_id, name, enabled, type, source, storage_destination_id, schedule_cron, retention_days, compression, encrypt, encrypt_passphrase, retry_max, retry_backoff_seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id, agent_id, name, enabled, type, source, storage_destination_id,
		          schedule_cron, retention_days, compression, encrypt,
		          retry_max, retry_backoff_seconds, created_at, updated_at`,
		req.AgentID, req.Name, req.Enabled, req.Type, req.Source,
		req.StorageDestinationID, req.ScheduleCron, req.RetentionDays,
		req.Compression, req.Encrypt, encPassphrase, retryMax, retryBackoff,
	).StructScan(&job)
	if err != nil {
		log.Printf("handleCreateJob: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if s.sched != nil {
		if err := s.sched.AddJob(job); err != nil {
			log.Printf("scheduler add job: %v", err)
		}
	}
	respond(w, http.StatusCreated, job)
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := scheduler.ValidateCron(req.ScheduleCron); err != nil {
		respondError(w, http.StatusBadRequest, "invalid cron expression: "+err.Error())
		return
	}
	if req.RetentionDays <= 0 {
		req.RetentionDays = 30
	}
	if err := validateRetryPolicy(&req); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A nil passphrase means "keep the stored one". Enabling encryption on a job
	// that has none stored must be rejected, otherwise the job would dispatch with
	// an empty passphrase and produce plaintext archives named ".enc".
	var storedPassphrase *string
	if err := s.db.Get(&storedPassphrase,
		`SELECT encrypt_passphrase FROM backup_jobs WHERE id=$1`, id); err != nil {
		log.Printf("handleUpdateJob load job %s: %v", id, err)
		respondError(w, http.StatusNotFound, "job not found")
		return
	}
	if req.Encrypt && (req.EncryptPassphrase == nil || *req.EncryptPassphrase == "") && storedPassphrase == nil {
		respondError(w, http.StatusBadRequest, "encrypt_passphrase is required when enabling encryption")
		return
	}

	var encPassphrase *string
	if req.Encrypt && req.EncryptPassphrase != nil && *req.EncryptPassphrase != "" {
		enc, err := crypto.Encrypt(s.cfg.EncryptionKey, *req.EncryptPassphrase)
		if err != nil {
			log.Printf("handleUpdateJob encrypt passphrase: %v", err)
			respondError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encPassphrase = &enc
	}
	// COALESCE keeps the stored passphrase when the request omits it; disabling
	// encryption clears it outright. The retry columns use the same trick: the UI
	// does not send them yet, and an edit through the UI must not silently wipe a
	// policy configured through the API.
	result, err := s.db.Exec(`
		UPDATE backup_jobs SET
		  agent_id=$1, name=$2, enabled=$3, type=$4, source=$5,
		  storage_destination_id=$6, schedule_cron=$7, retention_days=$8,
		  compression=$9, encrypt=$10,
		  encrypt_passphrase=CASE WHEN $10 THEN COALESCE($11, encrypt_passphrase) ELSE NULL END,
		  retry_max=COALESCE($12, retry_max),
		  retry_backoff_seconds=COALESCE($13, retry_backoff_seconds),
		  updated_at=$14
		WHERE id=$15`,
		req.AgentID, req.Name, req.Enabled, req.Type, req.Source,
		req.StorageDestinationID, req.ScheduleCron, req.RetentionDays,
		req.Compression, req.Encrypt, encPassphrase,
		req.RetryMax, req.RetryBackoffSeconds, time.Now(), id,
	)
	if err != nil {
		log.Printf("handleUpdateJob: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}
	if s.sched != nil {
		var updatedJob models.BackupJob
		if err := s.db.Get(&updatedJob, `SELECT * FROM backup_jobs WHERE id=$1`, id); err == nil {
			if err := s.sched.AddJob(updatedJob); err != nil {
				log.Printf("scheduler update job: %v", err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	result, err := s.db.Exec(`DELETE FROM backup_jobs WHERE id=$1`, id)
	if err != nil {
		log.Printf("handleDeleteJob: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	if s.sched != nil {
		s.sched.RemoveJob(id.String())
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleToggleJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	result, err := s.db.Exec(
		`UPDATE backup_jobs SET enabled=$1, updated_at=$2 WHERE id=$3`,
		body.Enabled, time.Now(), id,
	)
	if err != nil {
		log.Printf("handleToggleJob: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}
	// Update scheduler: reload job so cron reflects new enabled state
	if s.sched != nil {
		var job models.BackupJob
		if err := s.db.Get(&job, `SELECT * FROM backup_jobs WHERE id=$1`, id); err == nil {
			_ = s.sched.AddJob(job)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTriggerJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var job models.BackupJob
	if err := s.db.Get(&job, `SELECT * FROM backup_jobs WHERE id=$1`, id); err != nil {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}
	if s.hub == nil {
		respondError(w, http.StatusServiceUnavailable, "dispatcher unavailable")
		return
	}
	// Everything that can fail is resolved before the run row exists, so a failed
	// trigger cannot leave a row stuck in 'running' with nothing to close it.
	if !s.hub.IsConnected(job.AgentID.String()) {
		respondError(w, http.StatusConflict, "agent is not online")
		return
	}

	var destType, encryptedConfig string
	if err := s.db.QueryRow(
		`SELECT type, config FROM storage_destinations WHERE id = $1`,
		job.StorageDestinationID,
	).Scan(&destType, &encryptedConfig); err != nil {
		log.Printf("handleTriggerJob: fetch storage: %v", err)
		respondError(w, http.StatusInternalServerError, "storage destination not found")
		return
	}
	decryptedConfig, err := crypto.Decrypt(s.cfg.EncryptionKey, encryptedConfig)
	if err != nil {
		log.Printf("handleTriggerJob: decrypt storage: %v", err)
		respondError(w, http.StatusInternalServerError, "storage config decrypt failed")
		return
	}

	// An encrypted job whose passphrase cannot be recovered must not run: it would
	// produce a plaintext archive named ".enc" and report success.
	var passphrase string
	if job.Encrypt {
		if job.EncryptPassphrase == nil {
			respondError(w, http.StatusConflict, "job is encrypted but has no stored passphrase")
			return
		}
		passphrase, err = crypto.Decrypt(s.cfg.EncryptionKey, *job.EncryptPassphrase)
		if err != nil || passphrase == "" {
			log.Printf("handleTriggerJob: decrypt passphrase for job %s: %v", id, err)
			respondError(w, http.StatusInternalServerError, "backup passphrase decrypt failed")
			return
		}
	}

	// Overlap guard. "Run now" while the cron has already fired (or while a previous
	// run is simply taking longer than its interval) used to start a second backup of
	// the same job writing to the same destination. scheduler.BeginRun claims the
	// job's single running slot atomically, so exactly one of the racing callers
	// wins — including across the cron path and this one.
	run, err := scheduler.BeginRun(s.db, id, scheduler.AttemptFirst)
	if errors.Is(err, scheduler.ErrRunOverlap) {
		respondError(w, http.StatusConflict, scheduler.ErrRunOverlap.Error())
		return
	}
	if err != nil {
		log.Printf("handleTriggerJob: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := s.hub.DispatchJob(
		job.AgentID.String(), run.ID.String(), job, destType, json.RawMessage(decryptedConfig), passphrase,
	); err != nil {
		log.Printf("handleTriggerJob dispatch: %v", err)
		if _, dbErr := s.db.Exec(
			`UPDATE backup_runs SET status='failed', finished_at=NOW(), error_message=$2 WHERE id=$1`,
			run.ID, err.Error(),
		); dbErr != nil {
			log.Printf("handleTriggerJob: fail run %s: %v", run.ID, dbErr)
		}
		respondError(w, http.StatusConflict, "could not dispatch job: "+err.Error())
		return
	}
	respond(w, http.StatusCreated, run)
}
