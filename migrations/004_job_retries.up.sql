-- Retry policy lives on the job, the attempt counter on the run.
--
-- The defaults reproduce today's behaviour exactly: retry_max = 0 means "one
-- attempt, no retries", so every existing job keeps running as it does now until
-- someone opts in. retry_backoff_seconds is only read when retry_max > 0.
ALTER TABLE backup_jobs
    ADD COLUMN retry_max INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN retry_backoff_seconds INTEGER NOT NULL DEFAULT 300;

-- Bounds, not decoration: an unbounded retry_max combined with exponential backoff
-- turns a permanently broken job into a queue of retries days into the future.
ALTER TABLE backup_jobs
    ADD CONSTRAINT backup_jobs_retry_max_range
        CHECK (retry_max >= 0 AND retry_max <= 10),
    ADD CONSTRAINT backup_jobs_retry_backoff_range
        CHECK (retry_backoff_seconds >= 10 AND retry_backoff_seconds <= 86400);

-- attempt = 1 is a first execution, 2 the first retry, and so on. The default of 1
-- is correct for every row that already exists: they were all first attempts.
--
-- attempt = 0 is reserved for rows recorded WITHOUT dispatching anything — an
-- execution skipped because the job was still running. Those must never be retried,
-- which is exactly what the scheduler's `attempt >= 1` filter keys off.
ALTER TABLE backup_runs
    ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1;

ALTER TABLE backup_runs
    ADD CONSTRAINT backup_runs_attempt_range CHECK (attempt >= 0);

-- Rows left over from before the overlap guard existed would make the index below
-- fail to build. Close them first, keeping the most recent 'running' row per job.
-- (started_at, id) rather than started_at alone so ties resolve deterministically.
UPDATE backup_runs r
SET status        = 'failed',
    finished_at   = COALESCE(r.finished_at, NOW()),
    error_message = COALESCE(r.error_message, 'Superseded by a newer run of the same job')
WHERE r.status = 'running'
  AND EXISTS (
      SELECT 1 FROM backup_runs newer
      WHERE newer.job_id = r.job_id
        AND newer.status = 'running'
        AND (newer.started_at, newer.id) > (r.started_at, r.id)
  );

-- THE overlap guard. A partial UNIQUE index is the only construct available here
-- that is atomic under real concurrency.
--
-- `INSERT ... SELECT ... WHERE NOT EXISTS (...)` is not enough on its own: under
-- READ COMMITTED the subquery sees the snapshot taken when the statement began, so
-- two concurrent inserts — the cron firing while someone clicks "Run now", or two
-- server processes against the same database — can both observe "nothing running"
-- and both succeed. This index is enforced by the storage engine at insert time: the
-- second writer blocks until the first commits and then fails with 23505, whatever
-- process it came from.
--
-- Keeping the truth in the database instead of a mutex in the scheduler also means
-- it survives a server restart, which a mutex does not.
CREATE UNIQUE INDEX idx_backup_runs_one_running_per_job
    ON backup_runs (job_id)
    WHERE status = 'running';

-- The retry scanner polls for failed runs whose backoff has elapsed. Without this it
-- would walk every historical failure on every tick.
CREATE INDEX idx_backup_runs_failed_finished_at
    ON backup_runs (finished_at)
    WHERE status = 'failed';
