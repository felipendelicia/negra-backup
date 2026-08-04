DROP INDEX IF EXISTS idx_backup_runs_failed_finished_at;
DROP INDEX IF EXISTS idx_backup_runs_one_running_per_job;

-- Dropping a column drops its CHECK constraint too; the explicit statements keep the
-- rollback readable and are harmless thanks to IF EXISTS.
ALTER TABLE backup_runs DROP CONSTRAINT IF EXISTS backup_runs_attempt_range;
ALTER TABLE backup_runs DROP COLUMN IF EXISTS attempt;

ALTER TABLE backup_jobs DROP CONSTRAINT IF EXISTS backup_jobs_retry_backoff_range;
ALTER TABLE backup_jobs DROP CONSTRAINT IF EXISTS backup_jobs_retry_max_range;
ALTER TABLE backup_jobs DROP COLUMN IF EXISTS retry_backoff_seconds;
ALTER TABLE backup_jobs DROP COLUMN IF EXISTS retry_max;

-- The rows this migration's UPDATE closed are deliberately not reopened: they were
-- runs whose agent connection was already gone, and resurrecting them as 'running'
-- would recreate the stuck-run bug the guard exists to prevent.
