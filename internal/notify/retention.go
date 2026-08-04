// internal/notify/retention.go
package notify

import (
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/storage"
	"github.com/jmoiron/sqlx"
)

// errLocalDestinationUnreachable marks artifacts stored on an agent's own
// filesystem, which the server cannot delete over the network.
var errLocalDestinationUnreachable = errors.New("local destination lives on the agent host; delete not reachable from the server")

type RetentionCleaner struct {
	db     *sqlx.DB
	sender *EmailSender
	encKey string
}

func NewRetentionCleaner(db *sqlx.DB, sender *EmailSender, encKey string) *RetentionCleaner {
	return &RetentionCleaner{db: db, sender: sender, encKey: encKey}
}

// expiredRun is one row eligible for cleanup, joined with everything needed to
// reach the stored artifact.
type expiredRun struct {
	ID          string  `db:"id"`
	StoragePath *string `db:"storage_path"`
	DestType    *string `db:"dest_type"`
	DestConfig  *string `db:"dest_config"`
}

// Run deletes runs whose retention window has passed, removing the stored artifact
// before the row.
//
// This used to log "would delete storage: %s" and then DELETE the row regardless.
// The file was never removed and, with the row gone, its storage_path was lost — so
// every pass turned real backups into unreachable garbage. A run is now only deleted
// once its artifact is gone; if the delete fails the row stays so the next pass can
// retry, and so the operator can still find the file.
func (rc *RetentionCleaner) Run() {
	if rc.db == nil {
		return
	}
	log.Println("retention: starting cleanup")

	// retention_days <= 0 would make the interval zero or negative and sweep away
	// every run, including the ones taken minutes ago.
	var runs []expiredRun
	err := rc.db.Select(&runs, `
		SELECT br.id, br.storage_path, sd.type AS dest_type, sd.config AS dest_config
		FROM backup_runs br
		JOIN backup_jobs bj ON br.job_id = bj.id
		LEFT JOIN storage_destinations sd ON bj.storage_destination_id = sd.id
		WHERE bj.retention_days > 0
		  AND br.started_at < NOW() - (bj.retention_days || ' days')::INTERVAL
		  AND br.status IN ('success', 'failed', 'cancelled')
	`)
	if err != nil {
		log.Printf("retention query: %v", err)
		return
	}

	var deleted, kept int
	for _, run := range runs {
		if err := rc.deleteArtifact(run); err != nil {
			log.Printf("retention: keeping run %s, could not delete artifact: %v", run.ID, err)
			kept++
			continue
		}
		if _, err := rc.db.Exec(`DELETE FROM backup_runs WHERE id=$1`, run.ID); err != nil {
			log.Printf("retention delete run %s: %v", run.ID, err)
			kept++
			continue
		}
		deleted++
	}
	log.Printf("retention: deleted %d runs, kept %d pending retry", deleted, kept)
}

// deleteArtifact removes the stored file for a run. A run with no storage_path
// (a failure that never uploaded anything) has nothing to delete.
func (rc *RetentionCleaner) deleteArtifact(run expiredRun) error {
	if run.StoragePath == nil || *run.StoragePath == "" {
		return nil
	}
	if run.DestType == nil || run.DestConfig == nil {
		// The destination was removed; the row is all that is left to clean up.
		log.Printf("retention: run %s has no destination, dropping row only", run.ID)
		return nil
	}
	// Local destinations live on the agent's filesystem, out of the server's reach.
	// Removing the row would strand the file, so keep it and say so.
	if *run.DestType == models.StorageTypeLocal {
		return errLocalDestinationUnreachable
	}

	rawConfig, err := crypto.Decrypt(rc.encKey, *run.DestConfig)
	if err != nil {
		return err
	}
	backend, err := storage.NewBackend(*run.DestType, json.RawMessage(rawConfig), "", "", "")
	if err != nil {
		return err
	}
	return backend.Delete(*run.StoragePath)
}

func (rc *RetentionCleaner) StartDailySchedule() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("retention scheduler panic: %v", r)
			}
		}()
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			time.Sleep(time.Until(next))
			rc.Run()
		}
	}()
}
