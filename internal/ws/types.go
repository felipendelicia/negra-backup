// internal/ws/types.go
package ws

import (
	"encoding/json"

	"github.com/felipendelicia/nat-backup/internal/models"
)

const (
	MsgTypeHello        = "hello"
	MsgTypeHeartbeat    = "heartbeat"
	MsgTypeJobProgress  = "job_progress"
	MsgTypeJobDone      = "job_done"
	MsgTypeJobFailed    = "job_failed"
	MsgTypeRunJob       = "run_job"
	MsgTypeCancelJob    = "cancel_job"
	MsgTypeUpdateAgent  = "update_agent"
	MsgTypeAgentLog     = "agent_log"
	MsgTypeBrowsePath   = "browse_path"
	MsgTypeBrowseResult = "browse_result"

	// Restore. The server asks the agent to pull an archive back from its storage
	// destination and unpack it, because the agent is the only side that can reach
	// both the destination and the filesystem the data has to land on.
	MsgTypeListArchive     = "list_archive"
	MsgTypeArchiveListing  = "archive_listing"
	MsgTypeRestore         = "restore"
	MsgTypeRestoreProgress = "restore_progress"
	MsgTypeRestoreDone     = "restore_done"
	MsgTypeRestoreFailed   = "restore_failed"
)

// ArchiveEntry is one member of a backup archive, as listed by the agent.
type ArchiveEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
	IsDir bool   `json:"is_dir"`
}

// BrowseEntry represents a filesystem entry returned by the agent.
type BrowseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// AgentMessage is sent from agent to server.
type AgentMessage struct {
	Type        string `json:"type,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	OS          string `json:"os,omitempty"`
	Arch        string `json:"arch,omitempty"`
	Version     string `json:"version,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	Percent     int    `json:"percent,omitempty"`
	CurrentFile string `json:"current_file,omitempty"`
	Status      string `json:"status,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	// Checksum is the SHA-256 of the archive the agent uploaded, so a truncated
	// transfer can be told apart from a good one instead of being recorded as a
	// successful backup.
	Checksum    string `json:"checksum,omitempty"`
	StoragePath string `json:"storage_path,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`
	// Skipped reports entries the backup could not read. A backup that quietly
	// leaves files out is worse than one that fails, so this travels with the
	// completion message and ends up in the run log.
	SkippedCount      int      `json:"skipped_count,omitempty"`
	SkippedSuspicious int      `json:"skipped_suspicious,omitempty"`
	SkippedSample     []string `json:"skipped_sample,omitempty"`
	Error             string   `json:"error,omitempty"`
	Log               string   `json:"log,omitempty"`
	// Browse
	RequestID   string        `json:"request_id,omitempty"`
	Entries     []BrowseEntry `json:"entries,omitempty"`
	BrowseError string        `json:"browse_error,omitempty"`
	// Restore
	RestoreID      string         `json:"restore_id,omitempty"`
	ArchiveEntries []ArchiveEntry `json:"archive_entries,omitempty"`
	RestoredFiles  int            `json:"restored_files,omitempty"`
	RestoredBytes  int64          `json:"restored_bytes,omitempty"`
}

// ServerMessage is sent from server to agent.
type ServerMessage struct {
	Type          string            `json:"type,omitempty"`
	RunID         string            `json:"run_id,omitempty"`
	Job           *models.BackupJob `json:"job,omitempty"`
	StorageType   string            `json:"storage_type,omitempty"`
	StorageConfig json.RawMessage   `json:"storage_config,omitempty"`
	Passphrase    string            `json:"passphrase,omitempty"` // decrypted, in-flight only
	// Browse
	RequestID string `json:"request_id,omitempty"`
	Path      string `json:"path,omitempty"`
	// Restore. Filename is the archive to fetch from the destination; DestDir is
	// where it lands on the agent host; Include restricts what is unpacked so one
	// deleted file can be recovered without writing the whole archive back.
	RestoreID string   `json:"restore_id,omitempty"`
	Filename  string   `json:"filename,omitempty"`
	DestDir   string   `json:"dest_dir,omitempty"`
	Include   []string `json:"include,omitempty"`
}
