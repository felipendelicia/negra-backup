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

export interface BackupRun {
  id: string
  job_id: string
  started_at: string
  finished_at: string | null
  status: 'running' | 'success' | 'failed'
  size_bytes: number | null
  file_count: number | null
  error_message: string | null
  storage_path: string | null
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
  config: Record<string, unknown>
}
