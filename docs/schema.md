# Database Schema

PostgreSQL. Migrations live in `migrations/`, are embedded via `migrations/embed.go`
and run automatically at server startup (`db.RunMigrations`, called from
`cmd/server/main.go`).

Applied migrations:

| File | Effect |
|------|--------|
| `001_initial_schema.up.sql` | Creates the `uuid-ossp` extension, all six tables and four indexes |
| `002_storage_config_text.up.sql` | `storage_destinations.config` **and** `notification_settings.config` JSONB → TEXT |

This document mirrors the DDL. Every column below is taken from the migration
files; the "written values" notes cite the Go code that produces them.

> **No CHECK constraints exist anywhere in the schema.** Columns such as
> `agents.status`, `backup_runs.status`, `backup_jobs.type` and
> `backup_jobs.compression` are plain `TEXT`. The value sets listed below are what
> the application actually writes, enforced only in Go — not by the database.

---

## `agents`

| Column | Type | Null | Default | Notes |
|--------|------|------|---------|-------|
| `id` | UUID | NOT NULL | `uuid_generate_v4()` | PRIMARY KEY |
| `name` | TEXT | NOT NULL | — | |
| `api_key` | TEXT | NOT NULL | — | UNIQUE. Stores the **bcrypt hash**, never the plaintext key (`internal/api/agents.go`, `generateAPIKey`) |
| `os` | TEXT | NOT NULL | `''` | Reported by the agent in its `hello` message |
| `arch` | TEXT | NOT NULL | `''` | Reported by the agent in its `hello` message |
| `version` | TEXT | NOT NULL | `''` | Agent binary version |
| `last_seen` | TIMESTAMPTZ | NULL | — | Updated on `hello` and on every heartbeat (30 s) |
| `status` | TEXT | NOT NULL | `'offline'` | |
| `created_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | |

Values written to `status`: `online` (`internal/ws/handler.go`, on `hello`) and
`offline` (`internal/ws/hub.go` on unregister/reconcile, and `cmd/server/main.go`
which resets every agent to `offline` at startup).

---

## `storage_destinations`

| Column | Type | Null | Default | Notes |
|--------|------|------|---------|-------|
| `id` | UUID | NOT NULL | `uuid_generate_v4()` | PRIMARY KEY |
| `name` | TEXT | NOT NULL | — | Human-readable label |
| `type` | TEXT | NOT NULL | — | `local`, `s3` or `sftp` (`internal/models`) |
| `config` | TEXT | NOT NULL | `'{}'` | AES-256-GCM ciphertext, base64-encoded (`internal/crypto`). JSONB in 001, TEXT after 002 |
| `created_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | |

`config` is encrypted by `internal/api/storage.go` before insert and decrypted only
when a job is dispatched (`internal/scheduler`) or when retention needs to delete an
artifact (`internal/notify/retention.go`). `GET /api/storage-destinations` never
returns the column.

Config shapes (plaintext, before encryption — field names are the json tags in
`internal/models/models.go`; the API rejects unknown keys):

```jsonc
// local — path on the AGENT's filesystem
{ "path": "/backups" }

// s3 — endpoint and prefix optional; prefix defaults to "nat-backup"
{ "bucket": "...", "region": "...", "access_key": "...", "secret_key": "...",
  "endpoint": "...", "prefix": "..." }

// sftp — port is a NUMBER; password or private_key required (at least one);
// host_key optional, pins the server identity (falls back to the agent's
// ~/.ssh/known_hosts when empty)
{ "host": "...", "port": 22, "user": "...", "path": "...",
  "password": "...", "private_key": "...", "host_key": "ssh-ed25519 AAAA..." }
```

---

## `backup_jobs`

| Column | Type | Null | Default | Notes |
|--------|------|------|---------|-------|
| `id` | UUID | NOT NULL | `uuid_generate_v4()` | PRIMARY KEY |
| `agent_id` | UUID | NOT NULL | — | FK → `agents(id)` **ON DELETE CASCADE** |
| `name` | TEXT | NOT NULL | — | Also used to build the archive filename |
| `enabled` | BOOLEAN | NOT NULL | `true` | |
| `type` | TEXT | NOT NULL | — | `files`, `postgres`, `mysql`, `sqlite`, `mongodb` |
| `source` | JSONB | NOT NULL | `'{}'` | Stored in plaintext — see shapes below |
| `storage_destination_id` | UUID | NOT NULL | — | FK → `storage_destinations(id)`, **no ON DELETE clause** (defaults to `NO ACTION`) |
| `schedule_cron` | TEXT | NOT NULL | — | Validated by `scheduler.ValidateCron` |
| `retention_days` | INTEGER | NOT NULL | `30` | `<= 0` disables retention for the job (`internal/notify/retention.go`) |
| `compression` | TEXT | NOT NULL | `'zstd'` | `zstd` or `gzip` |
| `encrypt` | BOOLEAN | NOT NULL | `false` | |
| `encrypt_passphrase` | TEXT | NULL | — | AES-256-GCM ciphertext when set |
| `created_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | |
| `updated_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | Set explicitly by the API on update; **no trigger** |

`schedule_cron` accepts standard 5-field cron (minute, hour, day-of-month, month,
day-of-week) plus descriptors (`@hourly`, `@daily`, `@weekly`, `@monthly`,
`@yearly`, `@every <duration>`). Seconds are **not** enabled
(`scheduler.ValidateCron` builds the parser without `cron.Second`).

Source shapes (JSONB, plaintext):

```jsonc
// files
{ "paths": ["/home/user/docs", "/var/data"] }

// postgres / mysql / sqlite / mongodb
{ "conn_string": "postgresql://user:pass@host/db" }
```

> `source` holds database connection strings **in plaintext**, unlike
> `storage_destinations.config`.

---

## `backup_runs`

| Column | Type | Null | Default | Notes |
|--------|------|------|---------|-------|
| `id` | UUID | NOT NULL | `uuid_generate_v4()` | PRIMARY KEY |
| `job_id` | UUID | NOT NULL | — | FK → `backup_jobs(id)` **ON DELETE CASCADE** |
| `started_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | Also the retention reference point |
| `finished_at` | TIMESTAMPTZ | NULL | — | Set on every terminal transition |
| `status` | TEXT | NOT NULL | `'running'` | See below |
| `size_bytes` | BIGINT | NULL | — | Uncompressed bytes read by the archiver |
| `file_count` | INTEGER | NULL | — | |
| `error_message` | TEXT | NULL | — | |
| `storage_path` | TEXT | NULL | — | Archive filename as written by the agent |

Values written to `status`:

| Value | Written by |
|-------|-----------|
| `running` | `internal/scheduler/scheduler.go` and `internal/api/jobs.go` on INSERT |
| `success` | `internal/ws/handler.go` on `job_done` |
| `failed` | `internal/ws/handler.go` on `job_failed`, `internal/scheduler` (dispatch/setup failures) and `cmd/server/main.go` (stale runs at startup) |
| `cancelled` | `internal/api/runs.go` on `POST /api/runs/{id}/cancel` |

`cancelled` is terminal and sticky: `handleAgentMessage` skips the `failed` update
when the row is already `cancelled`.

---

## `notification_settings`

| Column | Type | Null | Default | Notes |
|--------|------|------|---------|-------|
| `id` | UUID | NOT NULL | `uuid_generate_v4()` | PRIMARY KEY |
| `type` | TEXT | NOT NULL | `'email'` | Only `email` is implemented |
| `config` | TEXT | NOT NULL | `'{}'` | JSONB in 001, TEXT after 002. **Stored in plaintext** |

`config` is **not** encrypted: `internal/api/settings.go` inserts the raw JSON and
`internal/notify/provider.go` unmarshals it directly, so the SMTP password sits in
the database in the clear.

There is no UNIQUE constraint on `type`, but at most one row exists in practice:
`PUT /api/settings/notifications` runs `DELETE FROM notification_settings` followed
by a single INSERT inside one transaction.

Email config shape (`models.EmailNotificationConfig`):

```jsonc
{
  "smtp_host": "smtp.example.com",
  "smtp_port": 587,
  "from": "backup@example.com",
  "to": ["admin@example.com"],
  "tls": true,
  "username": "...",
  "password": "..."
}
```

---

## `admin_users`

| Column | Type | Null | Default | Notes |
|--------|------|------|---------|-------|
| `id` | UUID | NOT NULL | `uuid_generate_v4()` | PRIMARY KEY |
| `username` | TEXT | NOT NULL | — | UNIQUE |
| `password_hash` | TEXT | NOT NULL | — | bcrypt |
| `created_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | |

The server upserts a single `admin` row at startup from `ADMIN_PASSWORD`
(`INSERT ... ON CONFLICT (username) DO NOTHING`), so changing `ADMIN_PASSWORD`
after the first boot has no effect.

---

## Foreign keys

| Child | Column | Parent | ON DELETE |
|-------|--------|--------|-----------|
| `backup_jobs` | `agent_id` | `agents(id)` | CASCADE |
| `backup_jobs` | `storage_destination_id` | `storage_destinations(id)` | *(none — `NO ACTION`)* |
| `backup_runs` | `job_id` | `backup_jobs(id)` | CASCADE |

Deleting an agent therefore deletes its jobs and, transitively, their runs.
Deleting a storage destination that is still referenced by a job fails with a
foreign-key violation, which `handleDeleteStorage` surfaces as a generic 500.

---

## Indexes

Created by `001_initial_schema.up.sql`:

```sql
CREATE INDEX idx_agents_status          ON agents(status);
CREATE INDEX idx_backup_runs_job_id     ON backup_runs(job_id);
CREATE INDEX idx_backup_runs_status     ON backup_runs(status);
CREATE INDEX idx_backup_runs_started_at ON backup_runs(started_at);
```

Implicit indexes from UNIQUE constraints: `agents(api_key)` and
`admin_users(username)`.

### Recommended indexes (not present)

| Index | Why |
|-------|-----|
| `backup_jobs(agent_id)` | FK with `ON DELETE CASCADE` and no index. Every agent delete does a sequential scan of `backup_jobs`, and `scheduler`/`notifyFailure` join on it |
| `backup_jobs(storage_destination_id)` | FK with no index. Every storage-destination delete scans `backup_jobs` to check the constraint |
| `backup_runs(job_id, started_at DESC)` | `handleListRuns` filters by `job_id` and always sorts `ORDER BY started_at DESC LIMIT 100`. The two single-column indexes cannot serve both steps at once |

`agents(api_key)` already has a UNIQUE index, but it cannot help authentication:
`AgentHandler.authenticateAPIKey` selects **every** row and bcrypt-compares the
presented key against each hash, so agent login is O(n) full scans plus one bcrypt
per agent.

---

## Known discrepancies

Recorded here because they need a migration or a code change, which is out of
scope for this document.

1. **`backup_runs.status` and every other enum-like column are unconstrained.**
   The database accepts any string. `models` only declares `RunStatusRunning`,
   `RunStatusSuccess` and `RunStatusFailed`; there is no `RunStatusCancelled`
   constant even though `internal/api/runs.go` writes the literal `'cancelled'`
   and `web/src/lib/types.ts` renders it. A future migration adding a CHECK
   constraint must include `cancelled`, or cancelling a run will start failing.
2. **`notification_settings.config` is TEXT but plaintext.** It shares a column
   type with `storage_destinations.config`, which is ciphertext, so the two look
   identical in the database while only one is protected.
3. **There is no `run_logs` table.** Run log lines are broadcast in memory only
   (`Hub.logSubs`); nothing persists them. `GET /api/runs/{id}/logs` returns just
   `{run_id, status, message}` from the run row, and
   `GET /api/runs/{id}/logs/ws` closes immediately unless the run is still
   `running`. Historical logs cannot be replayed.
4. **`backup_jobs.source` and `notification_settings.config` hold secrets in
   plaintext** (database connection strings and the SMTP password respectively).
