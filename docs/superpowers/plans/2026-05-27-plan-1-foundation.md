# Foundation & Database Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Set up Go monorepo with all dependencies, config loader, PostgreSQL connection, all DB model types, and schema migrations.

**Architecture:** Single Go module `github.com/felipendelicia/nat-backup`. Packages under `internal/`. Migrations embedded via `embed.FS` + `golang-migrate`. Config from env vars.

**Tech Stack:** Go 1.23, PostgreSQL 16, `sqlx`, `lib/pq`, `golang-migrate/v4`, `google/uuid`, `stretchr/testify`

---

## File Map

- `go.mod` — module + all dependencies
- `Makefile` — build/test/migrate targets
- `docker-compose.yml` — postgres for local dev
- `.env.example` — env var template
- `cmd/server/main.go` — stub
- `cmd/agent/main.go` — stub
- `cmd/migrate/main.go` — migration CLI
- `internal/config/config.go` + `config_test.go`
- `internal/db/db.go` + `db_test.go`
- `internal/db/migrate.go` + `migrate_test.go`
- `internal/models/models.go` + `models_test.go`
- `migrations/embed.go` — embed FS
- `migrations/001_initial_schema.up.sql`
- `migrations/001_initial_schema.down.sql`

---

### Task 1: Go Module + Directory Structure

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `docker-compose.yml`
- Create: `.env.example`
- Create: `cmd/server/main.go`
- Create: `cmd/agent/main.go`

- [ ] **Step 1: Create directory structure**

```bash
mkdir -p cmd/server cmd/agent cmd/migrate
mkdir -p internal/config internal/db internal/models
mkdir -p internal/api internal/ws internal/scheduler
mkdir -p internal/backup internal/storage internal/notify
mkdir -p migrations
mkdir -p web
mkdir -p bin
```

- [ ] **Step 2: Initialize Go module**

```bash
go mod init github.com/felipendelicia/nat-backup
```

Expected: creates `go.mod` with `module github.com/felipendelicia/nat-backup`

- [ ] **Step 3: Add all dependencies**

```bash
go get github.com/go-chi/chi/v5@v5.1.0
go get github.com/golang-jwt/jwt/v5@v5.2.1
go get github.com/google/uuid@v1.6.0
go get github.com/jmoiron/sqlx@v1.4.0
go get github.com/lib/pq@v1.10.9
go get github.com/golang-migrate/migrate/v4@v4.17.1
go get github.com/golang-migrate/migrate/v4/database/postgres@v4.17.1
go get github.com/golang-migrate/migrate/v4/source/iofs@v4.17.1
go get github.com/gorilla/websocket@v1.5.3
go get github.com/robfig/cron/v3@v3.0.1
go get github.com/klauspost/compress@v1.17.9
go get github.com/aws/aws-sdk-go-v2@v1.30.1
go get github.com/aws/aws-sdk-go-v2/config@v1.27.23
go get github.com/aws/aws-sdk-go-v2/credentials@v1.17.23
go get github.com/aws/aws-sdk-go-v2/service/s3@v1.57.1
go get github.com/pkg/sftp@v1.13.6
go get golang.org/x/crypto@v0.25.0
go get gopkg.in/yaml.v3@v3.0.1
go get github.com/stretchr/testify@v1.9.0
go mod tidy
```

- [ ] **Step 4: Create cmd/server/main.go stub**

```go
package main

import "fmt"

func main() {
	fmt.Println("nat-backup-server starting...")
}
```

- [ ] **Step 5: Create cmd/agent/main.go stub**

```go
package main

import "fmt"

func main() {
	fmt.Println("nat-backup-agent starting...")
}
```

- [ ] **Step 6: Create docker-compose.yml**

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: nat_backup
      POSTGRES_USER: nat_backup
      POSTGRES_PASSWORD: nat_backup_dev
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U nat_backup"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  pgdata:
```

- [ ] **Step 7: Create .env.example**

```
DATABASE_URL=postgres://nat_backup:nat_backup_dev@localhost:5432/nat_backup?sslmode=disable
JWT_SECRET=change-me-in-production-min-32-chars
ENCRYPTION_KEY=0000000000000000000000000000000000000000000000000000000000000000
ADMIN_PASSWORD=change-me-in-production
PORT=8080
TLS_ENABLED=false
TLS_CERT_FILE=
TLS_KEY_FILE=
```

- [ ] **Step 8: Create Makefile**

```makefile
.PHONY: build build-server build-agent test test-short migrate-up dev-up dev-down

SERVER_BIN=bin/nat-backup-server
AGENT_BIN=bin/nat-backup-agent

build: build-server build-agent

build-server:
	go build -o $(SERVER_BIN) ./cmd/server

build-agent:
	go build -o $(AGENT_BIN) ./cmd/agent

build-agent-windows:
	GOOS=windows GOARCH=amd64 go build -o bin/nat-backup-agent.exe ./cmd/agent

test:
	go test ./... -v

test-short:
	go test ./... -short -v

dev-up:
	docker compose up -d postgres

dev-down:
	docker compose down
```

- [ ] **Step 9: Verify compilation**

```bash
go build ./cmd/server && go build ./cmd/agent
```

Expected: no errors

- [ ] **Step 10: Commit**

```bash
git add .
git commit -m "feat: initialize Go monorepo with dependencies and structure"
```

---

### Task 2: Configuration Loader

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/config/config_test.go
package config_test

import (
	"os"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setEnv(t *testing.T, pairs ...string) func() {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		os.Setenv(pairs[i], pairs[i+1])
	}
	return func() {
		for i := 0; i < len(pairs); i += 2 {
			os.Unsetenv(pairs[i])
		}
	}
}

func TestLoad_AllRequiredPresent(t *testing.T) {
	cleanup := setEnv(t,
		"DATABASE_URL", "postgres://test",
		"JWT_SECRET", "secret",
		"ENCRYPTION_KEY", "aabbccdd",
		"ADMIN_PASSWORD", "admin",
	)
	defer cleanup()

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "postgres://test", cfg.DatabaseURL)
	assert.Equal(t, "secret", cfg.JWTSecret)
	assert.Equal(t, "aabbccdd", cfg.EncryptionKey)
	assert.Equal(t, "8080", cfg.Port)
	assert.False(t, cfg.TLSEnabled)
}

func TestLoad_MissingRequired(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("JWT_SECRET")
	os.Unsetenv("ENCRYPTION_KEY")
	os.Unsetenv("ADMIN_PASSWORD")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestLoad_TLSEnabled(t *testing.T) {
	cleanup := setEnv(t,
		"DATABASE_URL", "postgres://test",
		"JWT_SECRET", "secret",
		"ENCRYPTION_KEY", "key",
		"ADMIN_PASSWORD", "admin",
		"TLS_ENABLED", "true",
		"TLS_CERT_FILE", "/certs/cert.pem",
		"TLS_KEY_FILE", "/certs/key.pem",
	)
	defer cleanup()

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.True(t, cfg.TLSEnabled)
	assert.Equal(t, "/certs/cert.pem", cfg.TLSCertFile)
	assert.Equal(t, "/certs/key.pem", cfg.TLSKeyFile)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/config/... -v
```

Expected: FAIL "no Go files in internal/config"

- [ ] **Step 3: Implement config.go**

```go
// internal/config/config.go
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL   string
	JWTSecret     string
	EncryptionKey string
	AdminPassword string
	Port          string
	TLSEnabled    bool
	TLSCertFile   string
	TLSKeyFile    string
}

func Load() (Config, error) {
	var missing []string

	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	getOr := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		DatabaseURL:   get("DATABASE_URL"),
		JWTSecret:     get("JWT_SECRET"),
		EncryptionKey: get("ENCRYPTION_KEY"),
		AdminPassword: get("ADMIN_PASSWORD"),
		Port:          getOr("PORT", "8080"),
		TLSCertFile:   os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:    os.Getenv("TLS_KEY_FILE"),
	}

	if v := os.Getenv("TLS_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid TLS_ENABLED: %w", err)
		}
		cfg.TLSEnabled = b
	}

	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}

	return cfg, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/config/... -v
```

Expected: PASS all 3 tests

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat: add environment config loader"
```

---

### Task 3: PostgreSQL Connection

**Files:**
- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/db/db_test.go
package db_test

import (
	"os"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/stretchr/testify/require"
)

func testDSN() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://nat_backup:nat_backup_dev@localhost:5432/nat_backup?sslmode=disable"
}

func TestConnect_Valid(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	pool, err := db.Connect(testDSN())
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping())
}

func TestConnect_Invalid(t *testing.T) {
	_, err := db.Connect("postgres://bad:bad@localhost:19999/none?sslmode=disable&connect_timeout=1")
	require.Error(t, err)
}
```

- [ ] **Step 2: Run short to verify failure**

```bash
go test ./internal/db/... -v -short
```

Expected: FAIL "no Go files"

- [ ] **Step 3: Implement db.go**

```go
// internal/db/db.go
package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// Connect opens and verifies a PostgreSQL connection pool.
func Connect(databaseURL string) (*sqlx.DB, error) {
	pool, err := sqlx.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	pool.SetMaxOpenConns(25)
	pool.SetMaxIdleConns(5)
	if err := pool.Ping(); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}
```

- [ ] **Step 4: Run short tests**

```bash
go test ./internal/db/... -v -short
```

Expected: PASS TestConnect_Invalid, SKIP TestConnect_Valid

- [ ] **Step 5: Run integration test**

```bash
docker compose up -d postgres && sleep 3
go test ./internal/db/... -v
```

Expected: PASS both tests

- [ ] **Step 6: Commit**

```bash
git add internal/db/db.go internal/db/db_test.go
git commit -m "feat: add PostgreSQL connection pool"
```

---

### Task 4: Database Models

**Files:**
- Create: `internal/models/models.go`
- Create: `internal/models/models_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/models/models_test.go
package models_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgent_APIKeyHiddenFromJSON(t *testing.T) {
	now := time.Now()
	a := models.Agent{
		ID:       uuid.New(),
		Name:     "test-agent",
		APIKey:   "super-secret",
		OS:       "linux",
		Arch:     "amd64",
		Status:   models.AgentStatusOnline,
		LastSeen: &now,
	}
	data, err := json.Marshal(a)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "super-secret")
	assert.Contains(t, string(data), "test-agent")
}

func TestFilesSource_RoundTrip(t *testing.T) {
	src := models.FilesSource{Paths: []string{"/etc", "/home"}}
	data, err := json.Marshal(src)
	require.NoError(t, err)
	var got models.FilesSource
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, src.Paths, got.Paths)
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "online", models.AgentStatusOnline)
	assert.Equal(t, "offline", models.AgentStatusOffline)
	assert.Equal(t, "local", models.StorageTypeLocal)
	assert.Equal(t, "s3", models.StorageTypeS3)
	assert.Equal(t, "sftp", models.StorageTypeSFTP)
	assert.Equal(t, "files", models.JobTypeFiles)
	assert.Equal(t, "postgres", models.JobTypePostgres)
	assert.Equal(t, "mysql", models.JobTypeMySQL)
	assert.Equal(t, "sqlite", models.JobTypeSQLite)
	assert.Equal(t, "mongodb", models.JobTypeMongoDB)
	assert.Equal(t, "running", models.RunStatusRunning)
	assert.Equal(t, "success", models.RunStatusSuccess)
	assert.Equal(t, "failed", models.RunStatusFailed)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/models/... -v
```

Expected: FAIL "no Go files"

- [ ] **Step 3: Implement models.go**

```go
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
	ConnectionString string `json:"conn_string"` // encrypted at rest
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
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/models/... -v
```

Expected: PASS all 3 tests

- [ ] **Step 5: Commit**

```bash
git add internal/models/
git commit -m "feat: add all database model types and constants"
```

---

### Task 5: Database Migrations

**Files:**
- Create: `migrations/embed.go`
- Create: `migrations/001_initial_schema.up.sql`
- Create: `migrations/001_initial_schema.down.sql`
- Create: `internal/db/migrate.go`
- Create: `internal/db/migrate_test.go`
- Create: `cmd/migrate/main.go`

- [ ] **Step 1: Create migrations/embed.go**

```go
// migrations/embed.go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 2: Create up migration**

`migrations/001_initial_schema.up.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE agents (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    api_key TEXT UNIQUE NOT NULL,
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    last_seen TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'offline',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE storage_destinations (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE backup_jobs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    type TEXT NOT NULL,
    source JSONB NOT NULL DEFAULT '{}',
    storage_destination_id UUID NOT NULL REFERENCES storage_destinations(id),
    schedule_cron TEXT NOT NULL,
    retention_days INTEGER NOT NULL DEFAULT 30,
    compression TEXT NOT NULL DEFAULT 'zstd',
    encrypt BOOLEAN NOT NULL DEFAULT false,
    encrypt_passphrase TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE backup_runs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'running',
    size_bytes BIGINT,
    file_count INTEGER,
    error_message TEXT,
    storage_path TEXT
);

CREATE TABLE notification_settings (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    type TEXT NOT NULL DEFAULT 'email',
    config JSONB NOT NULL DEFAULT '{}'
);

CREATE TABLE admin_users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agents_status ON agents(status);
CREATE INDEX idx_backup_runs_job_id ON backup_runs(job_id);
CREATE INDEX idx_backup_runs_status ON backup_runs(status);
CREATE INDEX idx_backup_runs_started_at ON backup_runs(started_at);
```

- [ ] **Step 3: Create down migration**

`migrations/001_initial_schema.down.sql`:
```sql
DROP TABLE IF EXISTS backup_runs;
DROP TABLE IF EXISTS backup_jobs;
DROP TABLE IF EXISTS storage_destinations;
DROP TABLE IF EXISTS notification_settings;
DROP TABLE IF EXISTS admin_users;
DROP TABLE IF EXISTS agents;
```

- [ ] **Step 4: Write failing migration test**

```go
// internal/db/migrate_test.go
package db_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/stretchr/testify/require"
)

func TestRunMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	dsn := testDSN()

	err := db.RunMigrations(dsn)
	require.NoError(t, err)

	pool, err := db.Connect(dsn)
	require.NoError(t, err)
	defer pool.Close()

	tables := []string{"agents", "storage_destinations", "backup_jobs", "backup_runs", "notification_settings", "admin_users"}
	for _, tbl := range tables {
		var exists bool
		err = pool.QueryRow(
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)`, tbl,
		).Scan(&exists)
		require.NoError(t, err)
		require.True(t, exists, "table %s must exist", tbl)
	}

	// Idempotent: running again must not error
	require.NoError(t, db.RunMigrations(dsn))
}
```

- [ ] **Step 5: Run to verify failure**

```bash
go test ./internal/db/... -run TestRunMigrations -v -short
```

Expected: SKIP

- [ ] **Step 6: Implement migrate.go**

```go
// internal/db/migrate.go
package db

import (
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	natmigrations "github.com/felipendelicia/nat-backup/migrations"
)

// RunMigrations applies all pending up migrations. Idempotent.
func RunMigrations(databaseURL string) error {
	pool, err := Connect(databaseURL)
	if err != nil {
		return fmt.Errorf("connect for migration: %w", err)
	}
	defer pool.Close()

	driver, err := postgres.WithInstance(pool.DB, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("postgres driver: %w", err)
	}

	src, err := iofs.New(natmigrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}
```

- [ ] **Step 7: Create migrate CLI**

```go
// cmd/migrate/main.go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/felipendelicia/nat-backup/internal/db"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL not set")
	}
	if len(os.Args) < 2 || os.Args[1] != "up" {
		fmt.Fprintln(os.Stderr, "Usage: migrate up")
		os.Exit(1)
	}
	if err := db.RunMigrations(url); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
	fmt.Println("migrations applied")
}
```

- [ ] **Step 8: Run integration migration test**

```bash
docker compose up -d postgres && sleep 3
DATABASE_URL=postgres://nat_backup:nat_backup_dev@localhost:5432/nat_backup?sslmode=disable go test ./internal/db/... -v
```

Expected: PASS all tests

- [ ] **Step 9: Verify migrate CLI builds**

```bash
go build ./cmd/migrate
```

Expected: no errors

- [ ] **Step 10: Commit**

```bash
git add migrations/ internal/db/migrate.go internal/db/migrate_test.go cmd/migrate/
git commit -m "feat: add SQL migrations and migrate CLI"
```
