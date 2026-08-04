// internal/api/restore.go
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// restoreContext is everything needed to ask an agent to read a stored archive:
// which agent, which destination, the archive name, and the passphrase it was
// sealed with.
type restoreContext struct {
	AgentID     string
	StorageType string
	Config      json.RawMessage
	Passphrase  string
	Filename    string
}

// resolveRestoreContext gathers and decrypts what a restore needs, starting from a
// finished run. Restores are anchored to a run rather than to a job because the job
// may have been edited, or its destination changed, since the archive was written —
// the run is what records where the file actually went.
func (s *Server) resolveRestoreContext(runID uuid.UUID) (restoreContext, int, error) {
	var row struct {
		AgentID     string  `db:"agent_id"`
		StoragePath *string `db:"storage_path"`
		Status      string  `db:"status"`
		Encrypt     bool    `db:"encrypt"`
		Passphrase  *string `db:"encrypt_passphrase"`
		DestType    string  `db:"dest_type"`
		DestConfig  string  `db:"dest_config"`
	}
	err := s.db.Get(&row, `
		SELECT bj.agent_id, br.storage_path, br.status, bj.encrypt, bj.encrypt_passphrase,
		       sd.type AS dest_type, sd.config AS dest_config
		FROM backup_runs br
		JOIN backup_jobs bj ON br.job_id = bj.id
		JOIN storage_destinations sd ON bj.storage_destination_id = sd.id
		WHERE br.id = $1`, runID)
	if err != nil {
		return restoreContext{}, http.StatusNotFound, errRestore("run not found, or its job or destination has been deleted")
	}

	if row.Status != models.RunStatusSuccess {
		return restoreContext{}, http.StatusConflict, errRestore("only a successful run can be restored")
	}
	if row.StoragePath == nil || *row.StoragePath == "" {
		return restoreContext{}, http.StatusConflict, errRestore("this run has no stored archive")
	}
	if s.hub == nil || !s.hub.IsConnected(row.AgentID) {
		return restoreContext{}, http.StatusConflict, errRestore("the agent for this run is not online")
	}

	cfg, err := crypto.Decrypt(s.cfg.EncryptionKey, row.DestConfig)
	if err != nil {
		log.Printf("resolveRestoreContext decrypt storage for run %s: %v", runID, err)
		return restoreContext{}, http.StatusInternalServerError, errRestore("storage config could not be decrypted")
	}

	// An encrypted archive without a recoverable passphrase cannot be restored, and
	// saying so plainly beats letting the agent fail with a decryption error.
	var passphrase string
	if row.Encrypt {
		if row.Passphrase == nil {
			return restoreContext{}, http.StatusConflict,
				errRestore("this backup is encrypted but its passphrase is no longer stored")
		}
		passphrase, err = crypto.Decrypt(s.cfg.EncryptionKey, *row.Passphrase)
		if err != nil {
			log.Printf("resolveRestoreContext decrypt passphrase for run %s: %v", runID, err)
			return restoreContext{}, http.StatusInternalServerError, errRestore("backup passphrase could not be decrypted")
		}
	}

	return restoreContext{
		AgentID:     row.AgentID,
		StorageType: row.DestType,
		Config:      json.RawMessage(cfg),
		Passphrase:  passphrase,
		Filename:    *row.StoragePath,
	}, http.StatusOK, nil
}

type restoreError struct{ msg string }

func (e restoreError) Error() string { return e.msg }
func errRestore(msg string) error    { return restoreError{msg} }

// handleListArchive returns what is inside a run's archive without restoring it, so
// an operator can pick a single file instead of unpacking everything.
func (s *Server) handleListArchive(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	rc, status, err := s.resolveRestoreContext(runID)
	if err != nil {
		respondError(w, status, err.Error())
		return
	}

	entries, err := s.hub.ListArchive(rc.AgentID, uuid.NewString(), ws.ServerMessage{
		StorageType:   rc.StorageType,
		StorageConfig: rc.Config,
		Passphrase:    rc.Passphrase,
		Filename:      rc.Filename,
	})
	if err != nil {
		respondError(w, http.StatusBadGateway, "could not list the archive: "+err.Error())
		return
	}
	if entries == nil {
		entries = []ws.ArchiveEntry{}
	}
	respond(w, http.StatusOK, map[string]any{"entries": entries})
}

type restoreRequest struct {
	// DestDir is where the archive lands on the agent host. Restoring over the
	// original location is never implicit: the operator names the destination.
	DestDir string `json:"dest_dir"`
	// Include restricts what is unpacked. Empty restores the whole archive.
	Include []string `json:"include,omitempty"`
}

// handleStartRestore asks the agent to restore a run's archive.
//
// Progress is reported on the run's own log stream, so the UI can follow it with the
// socket it already uses for that run rather than needing a second channel.
func (s *Server) handleStartRestore(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req restoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.DestDir == "" {
		respondError(w, http.StatusBadRequest, "dest_dir is required")
		return
	}

	rc, status, err := s.resolveRestoreContext(runID)
	if err != nil {
		respondError(w, status, err.Error())
		return
	}

	if err := s.hub.StartRestore(rc.AgentID, ws.ServerMessage{
		RestoreID:     runID.String(),
		StorageType:   rc.StorageType,
		StorageConfig: rc.Config,
		Passphrase:    rc.Passphrase,
		Filename:      rc.Filename,
		DestDir:       req.DestDir,
		Include:       req.Include,
	}); err != nil {
		respondError(w, http.StatusConflict, "could not start the restore: "+err.Error())
		return
	}

	log.Printf("restore of run %s dispatched to agent %s into %s", runID, rc.AgentID, req.DestDir)
	respond(w, http.StatusAccepted, map[string]any{
		"run_id":   runID,
		"dest_dir": req.DestDir,
		"status":   "dispatched",
	})
}
