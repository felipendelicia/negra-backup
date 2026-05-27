// internal/api/jobs.go
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
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
	EncryptPassphrase    string          `json:"encrypt_passphrase,omitempty"`
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs := []models.BackupJob{}
	if err := s.db.Select(&jobs, `
		SELECT id, agent_id, name, enabled, type, source, storage_destination_id,
		       schedule_cron, retention_days, compression, encrypt, created_at, updated_at
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
	if req.Compression == "" {
		req.Compression = models.CompressionZstd
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = 30
	}
	var encPassphrase *string
	if req.Encrypt && req.EncryptPassphrase != "" {
		enc, err := crypto.Encrypt(s.cfg.EncryptionKey, req.EncryptPassphrase)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encPassphrase = &enc
	}
	var job models.BackupJob
	err := s.db.QueryRowx(`
		INSERT INTO backup_jobs
		  (agent_id, name, enabled, type, source, storage_destination_id, schedule_cron, retention_days, compression, encrypt, encrypt_passphrase)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, agent_id, name, enabled, type, source, storage_destination_id,
		          schedule_cron, retention_days, compression, encrypt, created_at, updated_at`,
		req.AgentID, req.Name, req.Enabled, req.Type, req.Source,
		req.StorageDestinationID, req.ScheduleCron, req.RetentionDays,
		req.Compression, req.Encrypt, encPassphrase,
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
	var encPassphrase *string
	if req.Encrypt && req.EncryptPassphrase != "" {
		enc, err := crypto.Encrypt(s.cfg.EncryptionKey, req.EncryptPassphrase)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encPassphrase = &enc
	}
	result, err := s.db.Exec(`
		UPDATE backup_jobs SET
		  agent_id=$1, name=$2, enabled=$3, type=$4, source=$5,
		  storage_destination_id=$6, schedule_cron=$7, retention_days=$8,
		  compression=$9, encrypt=$10, encrypt_passphrase=$11, updated_at=$12
		WHERE id=$13`,
		req.AgentID, req.Name, req.Enabled, req.Type, req.Source,
		req.StorageDestinationID, req.ScheduleCron, req.RetentionDays,
		req.Compression, req.Encrypt, encPassphrase, time.Now(), id,
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
	var run models.BackupRun
	if err := s.db.QueryRowx(
		`INSERT INTO backup_runs (job_id, status) VALUES ($1, 'running') RETURNING id, job_id, started_at, status`,
		id,
	).StructScan(&run); err != nil {
		log.Printf("handleTriggerJob: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if s.hub != nil {
		// Fetch storage destination
		var destType string
		var encryptedConfig string
		if err := s.db.QueryRow(
			`SELECT type, config FROM storage_destinations WHERE id = $1`,
			job.StorageDestinationID,
		).Scan(&destType, &encryptedConfig); err != nil {
			log.Printf("handleTriggerJob: fetch storage: %v", err)
			respondError(w, http.StatusInternalServerError, "storage destination not found")
			return
		}
		decryptedConfig, decErr := crypto.Decrypt(s.cfg.EncryptionKey, encryptedConfig)
		if decErr != nil {
			log.Printf("handleTriggerJob: decrypt storage: %v", decErr)
			respondError(w, http.StatusInternalServerError, "storage config decrypt failed")
			return
		}
		storageType := destType
		storageConfig := json.RawMessage(decryptedConfig)

		var passphrase string
		if job.Encrypt && job.EncryptPassphrase != nil {
			decPass, decErr := crypto.Decrypt(s.cfg.EncryptionKey, *job.EncryptPassphrase)
			if decErr == nil {
				passphrase = decPass
			}
		}
		s.hub.DispatchJob(job.AgentID.String(), run.ID.String(), job, storageType, storageConfig, passphrase)
	}
	respond(w, http.StatusCreated, run)
}
