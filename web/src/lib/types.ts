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
