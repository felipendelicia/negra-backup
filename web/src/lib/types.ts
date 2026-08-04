export interface Agent {
  id: string
  name: string
  os: string
  arch: string
  version: string
  last_seen: string | null
  status: 'online' | 'offline'
  created_at: string
}

export interface StorageDestination {
  id: string
  name: string
  type: 'local' | 's3' | 'sftp'
  created_at: string
}

// Field names and types below must match the json tags of the Go structs in
// internal/models/models.go exactly. Go silently discards unknown keys, so a
// mismatch here produces a destination that saves fine and fails at upload time.
export interface LocalStorageConfig {
  path: string
}

export interface S3StorageConfig {
  bucket: string
  region: string
  access_key: string
  secret_key: string
  endpoint?: string
  prefix?: string
}

export interface SFTPStorageConfig {
  host: string
  port: number
  user: string
  path: string
  password?: string
  private_key?: string
  host_key?: string
}

export type StorageConfig = LocalStorageConfig | S3StorageConfig | SFTPStorageConfig

export interface BackupJob {
  id: string
  agent_id: string
  name: string
  enabled: boolean
  type: 'files' | 'postgres' | 'mysql' | 'sqlite' | 'mongodb'
  source: Record<string, unknown>
  storage_destination_id: string
  schedule_cron: string
  retention_days: number
  compression: 'zstd' | 'gzip'
  encrypt: boolean
  created_at: string
  updated_at: string
}

/**
 * The `source` of a `files` job. `exclude` is a subset of .gitignore syntax
 * **without negations** — see internal/backup/exclude.go, which is the authority
 * on what each pattern means.
 */
export interface FilesSource {
  paths: string[]
  exclude?: string[]
}

/**
 * The verdict `GET /api/jobs` attaches to every job, mirroring
 * `notify.HealthStatus` (internal/notify/deadman.go). It is a closed set: a value
 * outside it means the server grew a state this UI does not render yet.
 *
 *  - `ok`         ran when it was supposed to, and the last run succeeded
 *  - `running`    a run is in flight; no verdict yet
 *  - `late`       silent: a scheduled tick passed with nothing to show for it
 *  - `failing`    it did run, recently, and the run failed
 *  - `never_run`  enabled, scheduled, and has never produced a single run
 *  - `disabled`   not scheduled, so there is nothing to judge
 *  - `unknown`    the cron expression cannot be parsed, or health could not load
 */
export type JobHealth =
  | 'ok' | 'running' | 'late' | 'failing' | 'never_run' | 'disabled' | 'unknown'

/** The summary of a job's most recent run, carried on the jobs list. */
export interface JobLastRun {
  id: string
  status: RunStatus
  started_at: string
  finished_at: string | null
  error_message: string | null
}

/**
 * One row of `GET /api/jobs`: the job plus the answer to "is it up to date".
 *
 * The health fields are derived per request rather than stored, so they are not
 * part of `BackupJob` and are never sent back on create/update.
 */
export interface JobListItem extends BackupJob {
  last_run: JobLastRun | null
  last_success_at: string | null
  next_run: string | null
  health: JobHealth
  health_detail: string
  agent_online: boolean
  agent_last_seen: string | null
}

/** One agent in the `GET /api/health` aggregate. */
export interface HealthAgent {
  id: string
  name: string
  last_seen: string | null
  online: boolean
  silent_for_seconds: number
  enabled_jobs: number
  total_jobs: number
  has_backups_here: boolean
}

/**
 * A condition the dead-man's switch is currently holding open. `kind` is
 * `agent_offline` or `job_late` (notify.AlertAgentOffline / AlertJobLate); the
 * strings are load-bearing on the server, so they are matched, not guessed.
 */
export interface OpenAlert {
  subject_type: 'agent' | 'job'
  subject_id: string
  kind: string
  first_seen_at: string
  last_notified_at: string | null
  notify_count: number
  detail: string
}

/** `GET /api/health` — "is everything backed up and up to date?" in one call. */
export interface HealthSummary {
  generated_at: string
  thresholds: {
    agent_offline_after: string
    job_grace: string
    check_interval: string
    repeat_interval: string
    deadman_enabled: string
  }
  jobs: {
    total: number
    by_health: Partial<Record<JobHealth, number>>
  }
  agents: {
    total: number
    online: number
    offline: number
    items: HealthAgent[]
  }
  open_alerts: OpenAlert[]
}

export type RunStatus = 'running' | 'success' | 'failed' | 'cancelled'

export interface BackupRun {
  id: string
  job_id: string
  started_at: string
  finished_at: string | null
  status: RunStatus
  size_bytes: number | null
  file_count: number | null
  error_message: string | null
  storage_path: string | null
  checksum?: string | null
}

/**
 * One member of a stored archive, as listed by the agent
 * (`GET /api/runs/{id}/archive`). `name` is the path *inside* the archive —
 * relative to the parent of the backed-up root, so a backup of `/srv/data`
 * lists as `data/…` — and it is exactly what `include` expects back.
 */
export interface ArchiveEntry {
  name: string
  size: number
  mode: string
  is_dir: boolean
}

export interface ArchiveListing {
  entries: ArchiveEntry[]
}

/**
 * `POST /api/runs/{id}/restore`. `dest_dir` is a path **on the agent host** and
 * must be absolute — the agent rejects anything else. An empty `include`
 * restores the whole archive; a directory name restores everything under it.
 */
export interface RestoreRequest {
  dest_dir: string
  include?: string[]
}

export interface RestoreAccepted {
  run_id: string
  dest_dir: string
  status: string
}

/**
 * GET /api/runs. The endpoint used to return a bare `BackupRun[]` capped at 100
 * rows; it now pages, and `total` counts every row matching the filters — not
 * just the ones in this page.
 */
export interface RunsPage {
  runs: BackupRun[]
  total: number
  limit: number
  offset: number
}

export interface RunsQuery {
  job_id?: string
  status?: RunStatus
  limit?: number
  offset?: number
}

/** One persisted log line. Only terminal events are stored; progress ticks are
 *  broadcast over the WebSocket and never written to the database. */
export interface RunLogLine {
  logged_at: string
  line: string
}

export interface RunLogs {
  run_id: string
  status: RunStatus
  message: string
  lines: RunLogLine[]
}

/** One calendar day of GET /api/runs/stats. `date` is a server-side
 *  `YYYY-MM-DD` string, not a timestamp — parse it as a local day. */
export interface RunStatsDay {
  date: string
  success: number
  failed: number
}

export interface RunStats {
  totals: {
    total: number
    success: number
    failed: number
    running: number
    cancelled: number
  }
  daily: RunStatsDay[]
  days: number
}

export interface EmailNotificationConfig {
  smtp_host: string
  smtp_port: number
  from: string
  to: string[]
  tls: boolean
  username?: string
  password?: string
}

export interface NotificationSettings {
  id: string
  type: 'email'
  config: EmailNotificationConfig
}

export interface CreateJobRequest {
  agent_id: string
  name: string
  enabled: boolean
  type: BackupJob['type']
  source: Record<string, unknown>
  storage_destination_id: string
  schedule_cron: string
  retention_days: number
  compression: 'zstd' | 'gzip'
  encrypt: boolean
  encrypt_passphrase?: string
}

export interface CreateStorageRequest {
  name: string
  type: StorageDestination['type']
  config: Partial<LocalStorageConfig & S3StorageConfig & SFTPStorageConfig>
}
