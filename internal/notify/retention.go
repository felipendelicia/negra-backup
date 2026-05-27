// internal/notify/retention.go
package notify

import (
	"log"
	"time"

	"github.com/jmoiron/sqlx"
)

type RetentionCleaner struct {
	db     *sqlx.DB
	sender *EmailSender
}

func NewRetentionCleaner(db *sqlx.DB, sender *EmailSender) *RetentionCleaner {
	return &RetentionCleaner{db: db, sender: sender}
}

func (rc *RetentionCleaner) Run() {
	if rc.db == nil {
		return
	}
	log.Println("retention: starting cleanup")

	rows, err := rc.db.Query(`
		SELECT br.id, br.storage_path
		FROM backup_runs br
		JOIN backup_jobs bj ON br.job_id = bj.id
		WHERE br.started_at < NOW() - (bj.retention_days || ' days')::INTERVAL
		  AND br.status IN ('success', 'failed')
	`)
	if err != nil {
		log.Printf("retention query: %v", err)
		return
	}
	defer rows.Close()

	var deleted int
	for rows.Next() {
		var id string
		var storagePath *string
		if err := rows.Scan(&id, &storagePath); err != nil {
			continue
		}
		if storagePath != nil {
			log.Printf("retention: would delete storage: %s", *storagePath)
		}
		if _, err := rc.db.Exec(`DELETE FROM backup_runs WHERE id=$1`, id); err != nil {
			log.Printf("retention delete run %s: %v", id, err)
			continue
		}
		deleted++
	}
	if err := rows.Err(); err != nil {
		log.Printf("retention rows error: %v", err)
	}
	log.Printf("retention: deleted %d old runs", deleted)
}

func (rc *RetentionCleaner) StartDailySchedule() {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			time.Sleep(time.Until(next))
			rc.Run()
		}
	}()
}
