DROP INDEX IF EXISTS idx_agents_api_key_id;
ALTER TABLE agents DROP COLUMN IF EXISTS api_key_id;

DROP INDEX IF EXISTS idx_backup_runs_job_id_started_at;
DROP INDEX IF EXISTS idx_backup_jobs_storage_destination_id;
DROP INDEX IF EXISTS idx_backup_jobs_agent_id;

DROP TABLE IF EXISTS run_logs;
