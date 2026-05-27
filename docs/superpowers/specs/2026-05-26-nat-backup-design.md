# nat-backup — Design Spec

**Date:** 2026-05-26  
**Status:** Approved

---

## Overview

Distributed backup system for file servers. Central server orchestrates backup jobs on remote agents installed on target machines. Multiplatform (Linux + Windows). Web UI for configuration.

---

## Architecture

```
┌─────────────────────────────────────────────┐
│                BACKUP SERVER                 │
│                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │ REST API │  │WebSocket │  │Scheduler │  │
│  │  (HTTPS) │  │ Handler  │  │  (cron)  │  │
│  └──────────┘  └──────────┘  └──────────┘  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │React UI  │  │ Storage  │  │  Email   │  │
│  │(embedded)│  │ Manager  │  │ Notifier │  │
│  └──────────┘  └──────────┘  └──────────┘  │
│  ┌──────────────────────────────────────┐   │
│  │            PostgreSQL                │   │
│  └──────────────────────────────────────┘   │
└─────────────────────────────────────────────┘
         ▲ WebSocket (agent initiates)
         │ HTTPS (data upload)
┌────────┴────────┐
│   BACKUP AGENT  │  (Linux / Windows)
│                 │
│  ┌───────────┐  │
│  │WS Client  │  │
│  ├───────────┤  │
│  │File Backup│  │
│  ├───────────┤  │
│  │DB Dumper  │  │
│  └───────────┘  │
└─────────────────┘
         │ writes directly
         ▼
┌─────────────────┐
│ Storage Backend │
│ Local / S3 /    │
│ SFTP            │
└─────────────────┘
```

### Flow

1. Agent starts → WebSocket connection to server with API key
2. Server authenticates, registers agent as "online"
3. Scheduler triggers job → server sends command to agent via WS
4. Agent executes backup (files or DB dump) → compresses → uploads to storage destination
5. Agent reports result to server via WS
6. On failure → server sends email notification

### Storage Note

Agents upload directly to the storage destination (S3, SFTP, or server-local via HTTP endpoint). Server does not proxy data — only orchestrates. Avoids bandwidth bottleneck.

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Server | Go |
| Agent | Go |
| Web UI | React + TypeScript |
| Database | PostgreSQL 16 |
| UI Components | shadcn/ui + Tailwind CSS |
| Data fetching | TanStack Query |
| Charts | Recharts |
| Routing | React Router |

---

## Binaries

- `nat-backup-server` — single Go binary (API + WS + Scheduler + embedded React UI)
- `nat-backup-agent` — Go binary for target machines (Linux/Windows)

---

## Repository Structure (Monorepo)

```
nat-backup/
├── cmd/
│   ├── server/        # server entrypoint
│   └── agent/         # agent entrypoint
├── internal/
│   ├── api/           # REST handlers
│   ├── ws/            # WebSocket hub + message types
│   ├── scheduler/     # cron engine
│   ├── storage/       # backends: local, s3, sftp
│   ├── backup/        # file backup + db dump logic
│   ├── notify/        # email notifications
│   └── models/        # DB models + migrations
├── web/               # React UI (builds to internal/api/static/)
├── docker-compose.yml # server + postgres
└── Makefile
```

---

## Data Models

```sql
-- Registered agent machines
agents (
  id UUID PRIMARY KEY,
  name TEXT NOT NULL,
  api_key TEXT UNIQUE NOT NULL,  -- hashed
  os TEXT,         -- linux | windows
  arch TEXT,       -- amd64 | arm64
  version TEXT,
  last_seen TIMESTAMPTZ,
  status TEXT      -- online | offline
)

-- Configurable storage destinations
storage_destinations (
  id UUID PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL,    -- local | s3 | sftp
  config JSONB NOT NULL  -- encrypted: bucket, keys, host, path, etc.
)

-- Backup job configuration
backup_jobs (
  id UUID PRIMARY KEY,
  agent_id UUID REFERENCES agents(id),
  name TEXT NOT NULL,
  enabled BOOLEAN DEFAULT true,
  type TEXT NOT NULL,              -- files | postgres | mysql | sqlite | mongodb
  source JSONB NOT NULL,           -- paths[] for files; encrypted conn_string for DB
  storage_destination_id UUID REFERENCES storage_destinations(id),
  schedule_cron TEXT NOT NULL,     -- e.g. "0 2 * * *"
  retention_days INTEGER NOT NULL,
  compression TEXT DEFAULT 'zstd', -- gzip | zstd
  encrypt BOOLEAN DEFAULT false,
  encrypt_passphrase TEXT          -- encrypted at rest via AES-256-GCM using ENCRYPTION_KEY env var
)

-- Execution history
backup_runs (
  id UUID PRIMARY KEY,
  job_id UUID REFERENCES backup_jobs(id),
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  status TEXT,        -- running | success | failed
  size_bytes BIGINT,
  file_count INTEGER,
  error_message TEXT,
  storage_path TEXT   -- where the backup was stored
)

-- Notification configuration
notification_settings (
  id UUID PRIMARY KEY,
  type TEXT NOT NULL,  -- email
  config JSONB NOT NULL -- smtp_host, port, from, to[], tls
)
```

---

## REST API

```
POST   /api/auth/login
GET    /api/agents
DELETE /api/agents/:id

GET    /api/storage-destinations
POST   /api/storage-destinations
PUT    /api/storage-destinations/:id
DELETE /api/storage-destinations/:id

GET    /api/jobs
POST   /api/jobs
PUT    /api/jobs/:id
DELETE /api/jobs/:id
POST   /api/jobs/:id/run          # manual trigger

GET    /api/runs                  # filterable by job/agent/status/date
GET    /api/runs/:id/logs

GET    /api/settings/notifications
PUT    /api/settings/notifications

POST   /api/upload/:run_id        # agent uploads backup (chunked multipart)
```

---

## WebSocket Protocol

Messages are JSON. Agent initiates connection; server sends commands.

```jsonc
// Agent → Server
{ "type": "hello", "api_key": "xxx", "os": "linux", "arch": "amd64", "version": "1.0.0" }
{ "type": "heartbeat" }
{ "type": "job_progress", "run_id": "uuid", "percent": 45, "current_file": "/etc/..." }
{ "type": "job_done",   "run_id": "uuid", "status": "success", "size_bytes": 1234567, "storage_path": "s3://..." }
{ "type": "job_failed", "run_id": "uuid", "error": "pg_dump: connection refused" }

// Server → Agent
{ "type": "run_job", "run_id": "uuid", "job": { /* full job config */ } }
```

---

## Authentication

| Caller | Method |
|--------|--------|
| UI → Server | JWT (login with user/password), expiry configurable |
| Agent → Server (WS) | `api_key` in `hello` message |
| Agent → Server (upload) | `Authorization: Bearer <api_key>` header |

TLS: self-signed cert auto-generated on first run, or bring your own cert via config.

---

## Backup Execution

### Files
- Streaming tar + zstd/gzip
- Optional AES-256-GCM encryption with per-job passphrase
- Chunked multipart upload to storage destination

### Databases

| DB | Method |
|----|--------|
| PostgreSQL | `pg_dump` subprocess |
| MySQL/MariaDB | `mysqldump` subprocess |
| SQLite | File copy + WAL checkpoint before copy |
| MongoDB | `mongodump` subprocess |

---

## Storage Backends

| Type | Config Fields |
|------|--------------|
| Local (server) | `path` — directory on server disk; agent uploads via HTTP |
| S3-compatible | `endpoint`, `bucket`, `region`, `access_key`, `secret_key` |
| SFTP | `host`, `port`, `user`, `password` or `private_key`, `path` |

---

## Retention

Server runs a daily cleanup job:
1. Query runs older than `retention_days` for each job
2. Delete files from storage destination
3. Delete `backup_runs` record

---

## Notifications

Email on backup failure (and optionally on success). Config: SMTP host/port/TLS/from/to[]. Test email button in UI.

---

## Web UI Screens

```
Dashboard
├── Summary: agents online/offline, active jobs, last run status
├── Chart: runs last 7 days (success/failed)
└── Recent runs list

Agents
├── List: name, OS, last seen, status pill
└── Detail: agent jobs, run history

Jobs
├── List: name, agent, schedule, last run, next run
├── Create/Edit:
│   ├── Type: files | postgres | mysql | sqlite | mongodb
│   ├── Source: paths (files) or connection string (DB)
│   ├── Destination: select storage destination
│   ├── Schedule: cron input + human preview ("Every day at 2am")
│   ├── Retention: N days
│   ├── Compression: gzip / zstd
│   └── Encryption: on/off + passphrase
└── "Run now" button

Run History
├── Filterable table: job, agent, status, date, size
└── Run detail: real-time logs via WebSocket

Storage Destinations
├── List of configured destinations
└── Create: type (Local | S3 | SFTP) → dynamic form

Settings
└── Email notifications: SMTP config + recipients + test email
```

---

## Deployment

### Server (Docker)

```yaml
services:
  server:
    image: nat-backup-server
    ports: ["443:443"]
    volumes: ["./data:/data"]  # TLS certs + local storage
    environment:
      - DATABASE_URL=postgres://...
      - JWT_SECRET=...
      - ENCRYPTION_KEY=...   # for encrypting stored secrets
  postgres:
    image: postgres:16
    volumes: ["./pgdata:/var/lib/postgresql/data"]
```

### Agent Installation

**Linux:**
```bash
curl -L https://server/download/agent-linux-amd64 -o nat-backup-agent
chmod +x nat-backup-agent
./nat-backup-agent install   # installs systemd service
```

**Windows:**
```powershell
.\nat-backup-agent.exe install   # registers as Windows Service
```

**Config (`agent.yaml`):**
```yaml
server_url: https://my-server.com
api_key: abc123
```

---

## Out of Scope (v1)

- VM / container snapshots
- Bare metal recovery
- Multi-user / RBAC (single admin user)
- Backup deduplication
- Incremental backups (full backups only in v1)
