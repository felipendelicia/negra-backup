// internal/models/models.go
package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	AgentStatusOnline  = "online"
	AgentStatusOffline = "offline"

	StorageTypeLocal = "local"
	StorageTypeS3    = "s3"
	StorageTypeSFTP  = "sftp"

	JobTypeFiles    = "files"
	JobTypePostgres = "postgres"
	JobTypeMySQL    = "mysql"
	JobTypeSQLite   = "sqlite"
	JobTypeMongoDB  = "mongodb"

	RunStatusRunning = "running"
	RunStatusSuccess = "success"
	RunStatusFailed  = "failed"

	CompressionGzip = "gzip"
	CompressionZstd = "zstd"
)

type Agent struct {
	ID        uuid.UUID  `db:"id"         json:"id"`
	Name      string     `db:"name"       json:"name"`
	APIKey    string     `db:"api_key"    json:"-"`
	OS        string     `db:"os"         json:"os"`
	Arch      string     `db:"arch"       json:"arch"`
	Version   string     `db:"version"    json:"version"`
	LastSeen  *time.Time `db:"last_seen"  json:"last_seen"`
	Status    string     `db:"status"     json:"status"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
}

type StorageDestination struct {
	ID        uuid.UUID       `db:"id"         json:"id"`
	Name      string          `db:"name"       json:"name"`
	Type      string          `db:"type"       json:"type"`
	Config    json.RawMessage `db:"config"     json:"config"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

type BackupJob struct {
	ID                   uuid.UUID       `db:"id"                     json:"id"`
	AgentID              uuid.UUID       `db:"agent_id"               json:"agent_id"`
	Name                 string          `db:"name"                   json:"name"`
	Enabled              bool            `db:"enabled"                json:"enabled"`
	Type                 string          `db:"type"                   json:"type"`
	Source               json.RawMessage `db:"source"                 json:"source"`
	StorageDestinationID uuid.UUID       `db:"storage_destination_id" json:"storage_destination_id"`
	ScheduleCron         string          `db:"schedule_cron"          json:"schedule_cron"`
	RetentionDays        int             `db:"retention_days"         json:"retention_days"`
	Compression          string          `db:"compression"            json:"compression"`
	Encrypt              bool            `db:"encrypt"                json:"encrypt"`
	EncryptPassphrase    *string         `db:"encrypt_passphrase"     json:"-"`
	CreatedAt            time.Time       `db:"created_at"             json:"created_at"`
	UpdatedAt            time.Time       `db:"updated_at"             json:"updated_at"`
}

type BackupRun struct {
	ID           uuid.UUID  `db:"id"            json:"id"`
	JobID        uuid.UUID  `db:"job_id"        json:"job_id"`
	StartedAt    time.Time  `db:"started_at"    json:"started_at"`
	FinishedAt   *time.Time `db:"finished_at"   json:"finished_at"`
	Status       string     `db:"status"        json:"status"`
	SizeBytes    *int64     `db:"size_bytes"    json:"size_bytes"`
	FileCount    *int       `db:"file_count"    json:"file_count"`
	ErrorMessage *string    `db:"error_message" json:"error_message"`
	StoragePath  *string    `db:"storage_path"  json:"storage_path"`
}

type NotificationSettings struct {
	ID     uuid.UUID       `db:"id"     json:"id"`
	Type   string          `db:"type"   json:"type"`
	Config json.RawMessage `db:"config" json:"config"`
}

type AdminUser struct {
	ID           uuid.UUID `db:"id"            json:"id"`
	Username     string    `db:"username"      json:"username"`
	PasswordHash string    `db:"password_hash" json:"-"`
	CreatedAt    time.Time `db:"created_at"    json:"created_at"`
}

// Source configs (stored as JSONB)
type FilesSource struct {
	Paths []string `json:"paths"`
}

type DBSource struct {
	ConnectionString string `json:"conn_string"`
}

// Storage configs (stored as JSONB)
type LocalStorageConfig struct {
	Path string `json:"path"`
}

type S3StorageConfig struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

type SFTPStorageConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Path       string `json:"path"`
}

// Notification config
type EmailNotificationConfig struct {
	SMTPHost string   `json:"smtp_host"`
	SMTPPort int      `json:"smtp_port"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	TLS      bool     `json:"tls"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
}
