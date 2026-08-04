package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/felipendelicia/nat-backup/internal/storage"
	"github.com/felipendelicia/nat-backup/internal/ws"
)

// The restore path runs on the agent because it is the only side that can reach both
// the storage destination and the filesystem the data has to land on. The server
// never holds the archive or the passphrase beyond the message that carries them.

// handleListArchive answers a list_archive request: download the archive, list what
// is inside it, and send the listing back. Nothing is written to disk.
func (a *Agent) handleListArchive(msg ws.ServerMessage) {
	entries, err := a.listArchive(msg)
	reply := ws.AgentMessage{
		Type:      ws.MsgTypeArchiveListing,
		RequestID: msg.RequestID,
	}
	if err != nil {
		reply.Error = err.Error()
	} else {
		reply.ArchiveEntries = entries
	}
	if err := a.writeJSON(reply); err != nil {
		log.Printf("send archive listing: %v", err)
	}
}

func (a *Agent) listArchive(msg ws.ServerMessage) ([]ws.ArchiveEntry, error) {
	rc, err := a.openArchive(msg)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	found, err := backup.ListArchive(rc, msg.Passphrase)
	if err != nil {
		return nil, err
	}

	entries := make([]ws.ArchiveEntry, 0, len(found))
	for _, e := range found {
		entries = append(entries, ws.ArchiveEntry{
			Name:  e.Name,
			Size:  e.Size,
			Mode:  e.Mode.String(),
			IsDir: e.Mode.IsDir(),
		})
	}
	return entries, nil
}

// handleRestore performs a restore requested by the server and reports the outcome.
func (a *Agent) handleRestore(msg ws.ServerMessage) {
	log.Printf("restoring %s into %s (restore %s)", msg.Filename, msg.DestDir, msg.RestoreID)

	res, err := a.restore(msg)
	if err != nil {
		log.Printf("restore %s failed: %v", msg.RestoreID, err)
		if sendErr := a.writeJSON(ws.AgentMessage{
			Type:      ws.MsgTypeRestoreFailed,
			RestoreID: msg.RestoreID,
			Error:     err.Error(),
		}); sendErr != nil {
			log.Printf("send restore_failed: %v", sendErr)
		}
		return
	}

	log.Printf("restore %s completed: %d files, %d bytes", msg.RestoreID, res.FileCount, res.TotalBytes)
	if err := a.writeJSON(ws.AgentMessage{
		Type:          ws.MsgTypeRestoreDone,
		RestoreID:     msg.RestoreID,
		RestoredFiles: res.FileCount,
		RestoredBytes: res.TotalBytes,
	}); err != nil {
		log.Printf("send restore_done: %v", err)
	}
}

func (a *Agent) restore(msg ws.ServerMessage) (backup.RestoreResult, error) {
	if msg.DestDir == "" {
		return backup.RestoreResult{}, fmt.Errorf("no destination directory given")
	}
	// An absolute destination is required so a restore can never be interpreted
	// relative to whatever directory the agent happens to be running from.
	if !filepath.IsAbs(msg.DestDir) {
		return backup.RestoreResult{}, fmt.Errorf("destination %q must be an absolute path", msg.DestDir)
	}
	if err := os.MkdirAll(msg.DestDir, 0o750); err != nil {
		return backup.RestoreResult{}, fmt.Errorf("create destination: %w", err)
	}

	rc, err := a.openArchive(msg)
	if err != nil {
		return backup.RestoreResult{}, err
	}
	defer rc.Close()

	return backup.RestoreWithOptions(rc, msg.DestDir, msg.Passphrase, backup.RestoreOptions{
		Include: msg.Include,
		OnEntry: func(e backup.RestoreEntry) {
			// Percent is not knowable without reading the archive twice, so the
			// progress messages carry the current file only; the UI shows activity
			// rather than a bar.
			a.writeJSON(ws.AgentMessage{ //nolint:errcheck
				Type:        ws.MsgTypeRestoreProgress,
				RestoreID:   msg.RestoreID,
				CurrentFile: e.Name,
			})
		},
		Logf: func(format string, args ...any) {
			log.Printf("restore %s: "+format, append([]any{msg.RestoreID}, args...)...)
		},
	})
}

// openArchive resolves the storage backend from the dispatched config and opens the
// archive for reading.
func (a *Agent) openArchive(msg ws.ServerMessage) (io.ReadCloser, error) {
	if msg.Filename == "" {
		return nil, fmt.Errorf("no archive filename given")
	}
	backend, err := storage.NewBackend(msg.StorageType, msg.StorageConfig, "", a.cfg.ServerURL, a.cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("storage backend: %w", err)
	}
	rc, err := backend.Download(msg.Filename)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", msg.Filename, err)
	}
	return rc, nil
}
