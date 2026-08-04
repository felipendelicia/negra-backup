-- Run logs were never persisted: the hub broadcast them to connected browsers and
-- dropped them. A finished run therefore had nothing to show, and the UI fabricated
-- a few cosmetic lines. Store them so a failure can be diagnosed after the fact.
CREATE TABLE run_logs (
    id BIGSERIAL PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES backup_runs(id) ON DELETE CASCADE,
    logged_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    line TEXT NOT NULL
);

-- Reading a run's log is always "everything for this run, in order".
CREATE INDEX idx_run_logs_run_id_id ON run_logs(run_id, id);

-- Foreign keys without an index: every cascade delete and every lookup by parent
-- was a sequential scan.
CREATE INDEX idx_backup_jobs_agent_id ON backup_jobs(agent_id);
CREATE INDEX idx_backup_jobs_storage_destination_id ON backup_jobs(storage_destination_id);

-- handleListRuns filters by job and orders by started_at DESC; the two separate
-- single-column indexes could not serve that as well as one composite.
CREATE INDEX idx_backup_runs_job_id_started_at ON backup_runs(job_id, started_at DESC);

-- Agent authentication compared the presented key against a bcrypt hash for EVERY
-- row, so one handshake cost N bcrypt operations — a CPU exhaustion vector that got
-- worse with each agent added. New keys carry a public, indexed identifier so the
-- row can be found first and exactly one hash is verified. Existing keys have no
-- identifier and keep working through the legacy scan until they are regenerated.
ALTER TABLE agents ADD COLUMN api_key_id TEXT;
CREATE UNIQUE INDEX idx_agents_api_key_id ON agents(api_key_id) WHERE api_key_id IS NOT NULL;
