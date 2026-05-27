# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
make build            # server + agent binaries → bin/
make build-full       # build-ui then build-server + build-agent
make build-agent-windows  # cross-compile agent for Windows

# Test
make test             # all tests, verbose
make test-short       # skip integration/external tests (-short flag)
go test ./internal/scheduler/... -v   # single package

# Dev infra
make dev-up           # start Postgres via Docker Compose
make dev-down         # stop Postgres

# UI (web/)
cd web && npm install
cd web && npm run dev     # dev server with HMR (proxies API to :8080)
cd web && npm run build   # produces web/dist → embedded into server binary
```

Server reads config from env. Copy `.env.example` and source it or export vars directly:
```
DATABASE_URL, JWT_SECRET, ENCRYPTION_KEY (64-char hex), ADMIN_PASSWORD, PORT
```

## Architecture

Two binaries + one SPA:

```
cmd/server  →  internal/api        HTTP + WebSocket server
            →  internal/scheduler  cron-based job dispatcher
            →  internal/ws         WebSocket hub (agent ↔ server)
            →  internal/notify     email + retention cleanup

cmd/agent   →  internal/backup     file/DB dump + compress + encrypt
            →  internal/storage    upload backend (local/S3/SFTP)
            →  internal/ws         message types shared with server
```

### Server → Agent flow

1. Agent dials `/ws/agent`, sends `hello` message containing its `api_key`.
2. `ws.AgentHandler` authenticates the key against `agents` table, registers the connection in `ws.Hub`.
3. `scheduler.Scheduler` (robfig/cron) fires jobs from DB on their cron schedule; calls `hub.DispatchJob(agentID, runID, job, storageType, decryptedStorageConfig, decryptedPassphrase)`.
4. Hub pushes a `run_job` WS message to the target agent.
5. Agent executes backup (`backup.BackupFiles` or `backup.NewDBDumper`), streams `job_progress` messages back.
6. Agent uploads the resulting archive to the configured storage backend, sends `job_done` or `job_failed`.
7. `ws.AgentHandler` handles those messages: updates `backup_runs` table, broadcasts log lines via `hub.BroadcastRunLog`.

### Encryption at rest

`internal/crypto` provides AES-256-GCM via a 64-char hex key (`ENCRYPTION_KEY`). Storage destination configs and backup passphrases are encrypted before DB insert and decrypted only when dispatching a job. The decrypted values travel in-process and over the WebSocket only (never stored decrypted).

### Storage backends

`internal/storage.NewBackend` dispatches on type string: `local`, `s3`, `sftp`. The `local` backend writes directly to a filesystem path on the **agent's** host. When the server is the storage target, the agent calls `POST /api/upload/{run_id}` (authenticated with its API key) and `internal/api/upload.go` handles the multipart upload.

### Database

PostgreSQL with migrations in `migrations/`. Migrations run automatically at server startup (`db.RunMigrations`). Files are embedded via `migrations/embed.go`. Migration 002 changed `storage_destinations.config` from JSONB to TEXT to support AES ciphertext.

### Web UI

React + TypeScript (Vite, shadcn/ui, React Router v6, next-themes). Pages: Dashboard, Agents, Jobs, Runs, Storage, Console, Settings. Auth: JWT stored in `localStorage`, injected as `Authorization: Bearer` header by `src/lib/api.ts`. Theme (light/dark/system) persisted via `next-themes` with `.dark` class on `<html>`. The built `web/dist` is embedded into the server binary via `internal/api/static/` and served as a SPA with fallback to `index.html`.

### Real-time run logs

`GET /api/runs/{id}/logs/ws` upgrades to WebSocket. The hub maintains a pub/sub map (`logSubs`) keyed by `run_id`; `AgentHandler` calls `hub.BroadcastRunLog` as progress messages arrive. Completed runs replay stored logs from DB.

### Server console stream

`GET /ws/console?token=<jwt>` streams server `log` output in real time to the Console page. `internal/api.ConsoleHub` implements `io.Writer`; in `cmd/server/main.go` it's wired via `io.MultiWriter(os.Stderr, srv.GetConsoleHub())`. Auth uses a JWT query param (browsers cannot set headers on WebSocket connections).

### Database schema

Full schema documented in `docs/schema.md`. **Keep that file updated whenever migrations are added or columns change.**
