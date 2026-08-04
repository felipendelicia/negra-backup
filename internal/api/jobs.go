// internal/api/jobs.go
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/notify"
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

// jobLastRun is the summary of a job's most recent run carried on the jobs list.
type jobLastRun struct {
	ID           uuid.UUID  `json:"id"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	ErrorMessage *string    `json:"error_message"`
}

// jobListItem is a job plus the answer to "is it up to date".
//
// The extra fields are additive, so a client that predates them (the current web UI)
// keeps working unchanged. models.BackupJob is embedded rather than duplicated, and
// the extra fields live here rather than on the model because they are not columns
// of backup_jobs — they are derived per request.
type jobListItem struct {
	models.BackupJob
	LastRun       *jobLastRun `json:"last_run"`
	LastSuccessAt *time.Time  `json:"last_success_at"`
	NextRun       *time.Time  `json:"next_run"`
	Health        string      `json:"health"`
	HealthDetail  string      `json:"health_detail"`
	// The agent dimension of the same question: a perfectly configured job on a
	// machine that has been offline for two days is not backed up.
	AgentOnline   bool       `json:"agent_online"`
	AgentLastSeen *time.Time `json:"agent_last_seen"`
}

// healthThresholds are the same numbers the dead-man's switch runs on, so the badge
// in the UI and the email in the inbox can never disagree.
//
// Read once per process rather than per request: they come from the environment and
// cannot change while the server runs.
var (
	healthCfgOnce sync.Once
	healthCfg     notify.DeadManConfig
)

func jobHealthConfig() notify.DeadManConfig {
	healthCfgOnce.Do(func() { healthCfg = notify.DeadManConfigFromEnv() })
	return healthCfg
}

// handleListJobs returns every job together with its last run, its next run and a
// derived health verdict.
//
// Extending this endpoint rather than adding a second one is deliberate: there is
// exactly one consumer, it renders one table, and every row of that table wants the
// health. A separate endpoint would mean a second round trip and a join done in the
// browser — with the two halves able to disagree while the page is half-loaded.
//
// It is three queries for the whole list, never one per job: the jobs, one
// LATERAL-joined health query (notify.LoadJobHealth), and an in-memory snapshot of
// the cron entries. If the health query fails the jobs are still returned, with
// health "unknown": a broken health badge must not take the Jobs page down with it.
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

	cfg := jobHealthConfig()
	now := time.Now()

	health := map[uuid.UUID]notify.JobHealth{}
	rows, err := notify.LoadJobHealth(s.db, now, cfg.JobGrace)
	if err != nil {
		log.Printf("handleListJobs health: %v", err)
	}
	for _, h := range rows {
		health[h.JobID] = h
	}

	// next_run comes from the scheduler's own cron entries, not from re-parsing
	// schedule_cron. The entry is what the server will actually do: a job whose
	// expression failed to load has no entry and therefore no next run, while
	// recomputing the string would invent a timestamp nobody is going to honour.
	var nextRuns map[string]time.Time
	if s.sched != nil {
		nextRuns = s.sched.NextRuns()
	}

	out := make([]jobListItem, 0, len(jobs))
	for _, job := range jobs {
		item := jobListItem{BackupJob: job, Health: string(notify.HealthUnknown)}
		if h, ok := health[job.ID]; ok {
			item.Health = string(h.Status)
			item.HealthDetail = h.Detail
			item.LastSuccessAt = h.LastSuccessAt
			item.AgentLastSeen = h.AgentLastSeen
			item.AgentOnline = h.AgentLastSeen != nil &&
				now.Sub(*h.AgentLastSeen) <= cfg.AgentOfflineAfter
			if h.LastRunID != nil && h.LastRunStartedAt != nil && h.LastRunStatus != nil {
				item.LastRun = &jobLastRun{
					ID:           *h.LastRunID,
					Status:       *h.LastRunStatus,
					StartedAt:    *h.LastRunStartedAt,
					FinishedAt:   h.LastRunFinishedAt,
					ErrorMessage: h.LastRunError,
				}
			}
		}
		if next, ok := nextRuns[job.ID.String()]; ok {
			n := next
			item.NextRun = &n
		}
		out = append(out, item)
	}
	respond(w, http.StatusOK, out)
}

// handleHealthSummary answers "is everything backed up and up to date?" in one call.
//
// It is the aggregate the Jobs list cannot give: per-agent state (which lives on
// agents, not on jobs) plus the alerts the dead-man's switch currently holds open,
// which is the only place the switch's own state is visible from outside an inbox.
func (s *Server) handleHealthSummary(w http.ResponseWriter, r *http.Request) {
	cfg := jobHealthConfig()
	now := time.Now()

	jobs, err := notify.LoadJobHealth(s.db, now, cfg.JobGrace)
	if err != nil {
		log.Printf("handleHealthSummary jobs: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	agents, err := notify.LoadAgentHealth(s.db, now, cfg.AgentOfflineAfter)
	if err != nil {
		log.Printf("handleHealthSummary agents: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	alerts, err := notify.LoadOpenAlerts(s.db)
	if err != nil {
		log.Printf("handleHealthSummary alerts: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if alerts == nil {
		alerts = []notify.OpenAlert{}
	}

	type agentSummary struct {
		ID             uuid.UUID  `json:"id"`
		Name           string     `json:"name"`
		LastSeen       *time.Time `json:"last_seen"`
		Online         bool       `json:"online"`
		SilentForSecs  int64      `json:"silent_for_seconds"`
		EnabledJobs    int        `json:"enabled_jobs"`
		TotalJobs      int        `json:"total_jobs"`
		HasBackupsHere bool       `json:"has_backups_here"`
	}

	jobCounts := map[string]int{}
	for _, j := range jobs {
		jobCounts[string(j.Status)]++
	}

	agentList := make([]agentSummary, 0, len(agents))
	online := 0
	for _, a := range agents {
		if a.Online {
			online++
		}
		agentList = append(agentList, agentSummary{
			ID:             a.AgentID,
			Name:           a.Name,
			LastSeen:       a.LastSeen,
			Online:         a.Online,
			SilentForSecs:  int64(a.SilentFor.Seconds()),
			EnabledJobs:    a.EnabledJobs,
			TotalJobs:      a.TotalJobs,
			HasBackupsHere: a.EnabledJobs > 0,
		})
	}

	respond(w, http.StatusOK, map[string]any{
		"generated_at": now,
		"thresholds": map[string]string{
			"agent_offline_after": cfg.AgentOfflineAfter.String(),
			"job_grace":           cfg.JobGrace.String(),
			"check_interval":      cfg.CheckInterval.String(),
			"repeat_interval":     cfg.RepeatInterval.String(),
			"deadman_enabled":     strconv.FormatBool(cfg.Enabled),
		},
		"jobs": map[string]any{
			"total":     len(jobs),
			"by_health": jobCounts,
		},
		"agents": map[string]any{
			"total":   len(agents),
			"online":  online,
			"offline": len(agents) - online,
			"items":   agentList,
		},
		"open_alerts": alerts,
	})
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
