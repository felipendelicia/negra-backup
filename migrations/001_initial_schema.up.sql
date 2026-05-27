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
