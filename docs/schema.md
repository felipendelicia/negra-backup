# Database Schema

PostgreSQL. Migrations live in `migrations/`, are embedded via `migrations/embed.go`
and run automatically at server startup (`db.RunMigrations`, called from
`cmd/server/main.go`).

Applied migrations:

| File | Effect |
|------|--------|
| `001_initial_schema.up.sql` | Creates the `uuid-ossp` extension, all six tables and four indexes |
| `002_storage_config_text.up.sql` | `storage_destinations.config` **and** `notification_settings.config` JSONB → TEXT |
| `004_job_retries.up.sql` | Retry policy on `backup_jobs`, attempt counter on `backup_runs`, and the partial unique index that allows one running run per job |

This document mirrors the DDL. Every column below is taken from the migration
files; the "written values" notes cite the Go code that produces them.

> **The enum-like `TEXT` columns have no CHECK constraints.** `agents.status`,
> `backup_runs.status`, `backup_jobs.type` and `backup_jobs.compression` are plain
> `TEXT`; the value sets listed below are what the application writes, enforced only
> in Go. Migration 004 added the first CHECK constraints in the schema, but they
> cover numeric ranges (`retry_max`, `retry_backoff_seconds`, `attempt`), not these
> columns.

---

## `agents`

| Column | Type | Null | Default | Notes |
|--------|------|------|---------|-------|
| `id` | UUID | NOT NULL | `uuid_generate_v4()` | PRIMARY KEY |
| `name` | TEXT | NOT NULL | — | |
| `api_key` | TEXT | NOT NULL | — | UNIQUE. Stores the **bcrypt hash** of the secret half, never the plaintext key (`internal/api/agents.go`, `generateAPIKey`) |
| `os` | TEXT | NOT NULL | `''` | Reported by the agent in its `hello` message |
| `arch` | TEXT | NOT NULL | `''` | Reported by the agent in its `hello` message |
| `version` | TEXT | NOT NULL | `''` | Agent binary version |
| `last_seen` | TIMESTAMPTZ | NULL | — | Updated on `hello` and on every heartbeat (30 s) |
| `api_key_id` | TEXT | NULL | — | Public identifier half of `natb_<key_id>_<secret>` keys (migration 003). NULL for keys issued earlier, which authenticate through the legacy scan |
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
| `retry_max` | INTEGER | NOT NULL | `0` | Migration 004. How many **retries** a failed run gets, so total attempts = `retry_max + 1`. `0` is the pre-004 behaviour: one attempt, no retry. `CHECK (retry_max BETWEEN 0 AND 10)` |
| `retry_backoff_seconds` | INTEGER | NOT NULL | `300` | Migration 004. Base delay before the first retry; doubles per attempt (`base × 2^(attempt-1)`), with the exponent capped at 6 in `internal/scheduler`. Only read when `retry_max > 0`. `CHECK (retry_backoff_seconds BETWEEN 10 AND 86400)` |
| `created_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | |
| `updated_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | Set explicitly by the API on update; **no trigger** |

Retry policy notes:

* `POST /api/jobs` applies the conservative defaults (`0` / `300`) when the fields
  are absent, so a job created without them behaves exactly as jobs did before 004.
* `PUT /api/jobs/{id}` uses `COALESCE($n, retry_max)`: an absent field keeps the
  stored value. The web UI does not send these fields, so editing a job through the
  UI cannot silently wipe a policy configured through the API.
* Both handlers reject out-of-range values with `400` before the database sees them
  (`validateRetryPolicy` in `internal/api/jobs.go` mirrors the CHECK constraints).

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
| `attempt` | INTEGER | NOT NULL | `1` | Migration 004. `1` = first execution, `2` = first retry, and so on. `0` is reserved (see below). `CHECK (attempt >= 0)` |

Values written to `status`:

| Value | Written by |
|-------|-----------|
| `running` | `scheduler.BeginRun`, called from `internal/scheduler/scheduler.go` (cron and retries) and `internal/api/jobs.go` (`Run now`) |
| `success` | `internal/ws/handler.go` on `job_done` |
| `failed` | `internal/ws/handler.go` on `job_failed`, `internal/scheduler` (dispatch/setup failures and overlap skips) and `cmd/server/main.go` (stale runs at startup) |
| `cancelled` | `internal/api/runs.go` on `POST /api/runs/{id}/cancel` |

`cancelled` is terminal and sticky: `handleAgentMessage` skips the `failed` update
when the row is already `cancelled`.

Values written to `attempt`:

| Value | Meaning |
|-------|---------|
| `0` | The execution was **skipped**, not attempted: a previous run of the job was still in progress. The row is stored `failed` with `error_message` starting `Skipped:` purely so the miss is visible; the retry scanner ignores it (`attempt >= 1`), because the run that caused the overlap is still going and will be retried on its own if it fails |
| `1` | First execution — cron, `Run now`, or any row written before migration 004 |
| `>= 2` | A retry. Capped by the job's `retry_max`: the scanner only picks rows where `attempt <= retry_max` |

### Retry scheduling

`internal/scheduler` polls for retryable runs every 30 s (`retryLoop` /
`retryCandidateQuery`) instead of arming a `time.AfterFunc` per failure. The pending
state **is** the failed row: one goroutine for the whole scheduler regardless of
failure volume, and after a server restart the same query finds the same rows, so
retries resume by themselves rather than dying with the process.

A failed run is a candidate when all of the following hold:

* `status = 'failed'` and `attempt BETWEEN 1 AND backup_jobs.retry_max`
* the job is `enabled` and has `retry_max > 0`
* `NOW() >= finished_at + retry_backoff_seconds × 2^min(attempt-1, 6)` seconds
* that delay elapsed **less than one hour ago** — past this grace window the failure
  stands, so a server that was down for two days does not come back and dispatch
  every retry that came due while it was off
* no newer run of the same job exists, ordered by `(started_at, id)`. This is what
  makes repeated scans idempotent: once a retry row is created, the run it came from
  stops being a candidate

Retries are dispatched through the same `triggerJob` → `BeginRun` path as everything
else, so they are subject to the overlap guard below and can never start a second
concurrent run.

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

## `run_logs`

Added by migration 003. Persists a run's log lines so a finished run can be
inspected after the fact; previously the lines were broadcast to connected browsers
and dropped.

| Column | Type | Nullable | Default | Notes |
|--------|------|----------|---------|-------|
| `id` | `BIGSERIAL` | not null | sequence | Primary key; also the ordering key |
| `run_id` | `UUID` | not null | — | FK to `backup_runs(id)` `ON DELETE CASCADE` |
| `logged_at` | `TIMESTAMPTZ` | not null | `NOW()` | |
| `line` | `TEXT` | not null | — | |

Only terminal events are stored (`completed: ...`, `failed: ...`). Progress ticks
are broadcast but not persisted: they are high-frequency and meaningless once the
run is over.

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

### Indexes added by migration 003

| Index | Why |
|-------|-----|
| `backup_jobs(agent_id)` | FK with `ON DELETE CASCADE` and no index. Every agent delete did a sequential scan of `backup_jobs`, and `scheduler`/`notifyFailure` join on it |
| `backup_jobs(storage_destination_id)` | FK with no index. Every storage-destination delete scanned `backup_jobs` to check the constraint |
| `backup_runs(job_id, started_at DESC)` | `handleListRuns` filters by `job_id` and sorts `ORDER BY started_at DESC`. The two single-column indexes could not serve both steps at once |
| `run_logs(run_id, id)` | Reading a run's log is always "every line for this run, in order" |
| `agents(api_key_id)` UNIQUE, partial `WHERE api_key_id IS NOT NULL` | Lets authentication find the one candidate row instead of scanning |

`agents(api_key)` has a UNIQUE index that never helped authentication: the presented
key is compared against a bcrypt hash, which is not something an index can look up.
Authentication used to select **every** row and bcrypt-compare against each hash, so
one unauthenticated handshake cost N bcrypt operations and got slower with every
agent added.

Keys issued since migration 003 have the form `natb_<key_id>_<secret>`. The
identifier is public and indexed, so the row is found directly and exactly one hash
is verified. Keys issued earlier have `api_key_id IS NULL` and still authenticate
through the scan, which is now restricted to those rows; regenerating an agent's key
moves it onto the indexed path.

### Indexes added by migration 004

| Index | Why |
|-------|-----|
| `backup_runs(job_id)` UNIQUE, partial `WHERE status = 'running'` | The **overlap guard**: at most one run of a job may be `running` at any moment |
| `backup_runs(finished_at)` partial `WHERE status = 'failed'` | The retry scanner runs every 30 s and filters on exactly this; without it each tick walked every historical failure |

#### Why the overlap guard is an index and not a query

Nothing previously stopped two runs of the same job from executing at once — a daily
backup that takes more than a day, or `Run now` clicked while the cron had already
fired, produced two agents writing to the same destination.

`scheduler.BeginRun` inserts with `INSERT ... SELECT ... WHERE NOT EXISTS (... status
= 'running')`, but **that clause is not the guard**. Under `READ COMMITTED` each
statement reads the snapshot taken when it began, so every concurrent inserter that
starts before the winner commits still sees "nothing running" and inserts too.
Measured against this database with the index dropped, 16 simultaneous callers let
2–5 runs through in 198 of 200 rounds. The `WHERE NOT EXISTS` clause is kept only
because it makes the uncontended case cheap and returns a clean `sql.ErrNoRows`
instead of an error to classify.

The partial unique index is what actually enforces the invariant: it is checked by
the storage engine at insert time, so the second writer blocks until the first commits
and then fails with `23505`, whichever process or server it came from. `BeginRun`
maps both outcomes to `scheduler.ErrRunOverlap`, which `internal/api/jobs.go` surfaces
as **409** on `POST /api/jobs/{id}/trigger`.

A mutex in the scheduler was rejected for the same reason: it dies with the process,
so a run still in flight after a restart would no longer be protected, and two servers
against one database never would be.

Migration 004 closes any pre-existing duplicate `running` rows (keeping the most
recent per job, ordered by `(started_at, id)`) before creating the index, since
otherwise the index would fail to build on a live database. `004_job_retries.down.sql`
deliberately does **not** reopen them.

---

## Known discrepancies

Recorded here because they need a migration or a code change, which is out of
scope for this document.

1. **`backup_runs.status` and every other enum-like column are unconstrained.**
   The database accepts any string. `models` now declares `RunStatusCancelled`
   alongside the other three, so the literal written by `internal/api/runs.go` has
   a constant. A future migration adding a CHECK constraint must include
   `cancelled`, or cancelling a run will start failing.
2. ~~**`notification_settings.config` is TEXT but plaintext.**~~ Resolved: the
   column now holds AES-256-GCM ciphertext, like `storage_destinations.config`.
   Rows written before that change are still plaintext and are read transparently;
   saving the settings again encrypts them.
3. ~~**There is no `run_logs` table.**~~ Resolved by migration 003. Terminal
   events are persisted and `GET /api/runs/{id}/logs` returns them in `lines`.
   Progress ticks are deliberately not stored: they are high-frequency and
   worthless once the run has finished, so they stay broadcast-only.
4. **`backup_jobs.source` holds secrets in plaintext** — database connection
   strings, including their passwords. Unlike storage configs and backup
   passphrases, it is neither encrypted nor redacted by the API.
5. **The retry policy has no UI.** `retry_max` and `retry_backoff_seconds` are
   readable and writable through `GET`/`POST`/`PUT /api/jobs`, but the web UI does
   not render or send them, so every job stays at the default of 0 retries until
   someone sets the policy over the API. The `COALESCE` on update exists precisely
   so a UI edit does not reset a policy set that way.
6. **Overlap skips are recorded as `failed` runs.** A skipped execution writes a
   terminal row with `attempt = 0` and an error message starting `Skipped:`, which is
   what makes an unachievable schedule visible in the Runs list. A job whose runs
   routinely outlast their interval will therefore accumulate one such row per
   missed tick until retention sweeps them. A dedicated `skipped` status would read
   better, but the UI renders `backup_runs.status` directly and does not know that
   value.
