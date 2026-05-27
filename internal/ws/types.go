// internal/ws/types.go
package ws

import (
	"encoding/json"

	"github.com/felipendelicia/nat-backup/internal/models"
)

const (
	MsgTypeHello       = "hello"
	MsgTypeHeartbeat   = "heartbeat"
	MsgTypeJobProgress = "job_progress"
	MsgTypeJobDone     = "job_done"
	MsgTypeJobFailed   = "job_failed"
	MsgTypeRunJob      = "run_job"
	MsgTypeCancelJob   = "cancel_job"
)

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
	StoragePath string `json:"storage_path,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ServerMessage is sent from server to agent.
type ServerMessage struct {
	Type          string            `json:"type,omitempty"`
	RunID         string            `json:"run_id,omitempty"`
	Job           *models.BackupJob `json:"job,omitempty"`
	StorageType   string            `json:"storage_type,omitempty"`
	StorageConfig json.RawMessage   `json:"storage_config,omitempty"`
	Passphrase    string            `json:"passphrase,omitempty"` // decrypted, in-flight only
}
