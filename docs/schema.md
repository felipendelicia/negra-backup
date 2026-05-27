# Database Schema

PostgreSQL. Migrations in `migrations/`, run automatically at server startup via `db.RunMigrations`.

---

## `agents`

| Column      | Type        | Notes                              |
|-------------|-------------|------------------------------------|
| `id`        | UUID PK     | `uuid_generate_v4()`               |
| `name`      | TEXT        |                                    |
| `api_key`   | TEXT UNIQUE | bcrypt-hashed                      |
| `os`        | TEXT        | e.g. `linux`, `windows`            |
| `arch`      | TEXT        | e.g. `amd64`, `arm64`              |
| `version`   | TEXT        | agent binary version               |
| `last_seen` | TIMESTAMPTZ | nullable, updated on heartbeat     |
| `status`    | TEXT        | `online` \| `offline`              |
| `created_at`| TIMESTAMPTZ |                                    |

---

## `storage_destinations`

| Column      | Type        | Notes                                                      |
|-------------|-------------|------------------------------------------------------------|
| `id`        | UUID PK     |                                                            |
| `name`      | TEXT        | human-readable label                                       |
| `type`      | TEXT        | `local` \| `s3` \| `sftp`                                 |
| `config`    | TEXT        | AES-256-GCM encrypted JSON (see models for shape per type) |
| `created_at`| TIMESTAMPTZ |                                                            |

> **Migration 002** changed `config` from JSONB → TEXT to support ciphertext storage.

Config shapes (plaintext, before encryption):

```jsonc
// local
{ "path": "/backups" }

// s3
{ "endpoint": "...", "bucket": "...", "region": "...", "access_key": "...", "secret_key": "..." }

// sftp
{ "host": "...", "port": 22, "user": "...", "password": "...", "private_key": "...", "path": "..." }
```

---

## `backup_jobs`

| Column                   | Type        | Notes                                                  |
|--------------------------|-------------|--------------------------------------------------------|
| `id`                     | UUID PK     |                                                        |
| `agent_id`               | UUID FK     | → `agents.id` ON DELETE CASCADE                       |
| `name`                   | TEXT        |                                                        |
| `enabled`                | BOOLEAN     | default `true`                                         |
| `type`                   | TEXT        | `files` \| `postgres` \| `mysql` \| `sqlite` \| `mongodb` |
| `source`                 | JSONB       | see source shapes below                                |
| `storage_destination_id` | UUID FK     | → `storage_destinations.id`                           |
| `schedule_cron`          | TEXT        | standard cron or `@hourly` / `@daily` / `@weekly`      |
| `retention_days`         | INTEGER     | default `30`                                           |
| `compression`            | TEXT        | `zstd` \| `gzip`                                       |
| `encrypt`                | BOOLEAN     | default `false`                                        |
| `encrypt_passphrase`     | TEXT        | nullable, AES-256-GCM encrypted                        |
| `created_at`             | TIMESTAMPTZ |                                                        |
| `updated_at`             | TIMESTAMPTZ |                                                        |

Source shapes (JSONB, stored plaintext):

```jsonc
// files
{ "paths": ["/home/user/docs", "/var/data"] }

// postgres / mysql / mongodb / sqlite
{ "conn_string": "postgresql://user:pass@host/db" }
```

---

## `backup_runs`

| Column          | Type        | Notes                              |
|-----------------|-------------|------------------------------------|
| `id`            | UUID PK     |                                    |
| `job_id`        | UUID FK     | → `backup_jobs.id` ON DELETE CASCADE |
| `started_at`    | TIMESTAMPTZ | default `NOW()`                    |
| `finished_at`   | TIMESTAMPTZ | nullable                           |
| `status`        | TEXT        | `running` \| `success` \| `failed`|
| `size_bytes`    | BIGINT      | nullable                           |
| `file_count`    | INTEGER     | nullable                           |
| `error_message` | TEXT        | nullable                           |
| `storage_path`  | TEXT        | filename uploaded to storage       |

---

## `notification_settings`

| Column   | Type    | Notes                                      |
|----------|---------|--------------------------------------------|
| `id`     | UUID PK |                                            |
| `type`   | TEXT    | `email`                                    |
| `config` | JSONB   | `EmailNotificationConfig` — see models     |

Email config shape:
```jsonc
{
  "smtp_host": "smtp.gmail.com",
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

| Column          | Type        | Notes              |
|-----------------|-------------|--------------------|
| `id`            | UUID PK     |                    |
| `username`      | TEXT UNIQUE |                    |
| `password_hash` | TEXT        | bcrypt             |
| `created_at`    | TIMESTAMPTZ |                    |

---

## Indexes

```sql
CREATE INDEX idx_agents_status           ON agents(status);
CREATE INDEX idx_backup_runs_job_id      ON backup_runs(job_id);
CREATE INDEX idx_backup_runs_status      ON backup_runs(status);
CREATE INDEX idx_backup_runs_started_at  ON backup_runs(started_at);
```
