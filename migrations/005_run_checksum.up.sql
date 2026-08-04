-- A truncated or corrupted upload was reported as a successful backup and only
-- discovered when someone tried to restore it — the worst possible moment. The
-- agent now records the SHA-256 of the archive it produced, so the stored artifact
-- can be verified against what was actually backed up.
ALTER TABLE backup_runs ADD COLUMN checksum TEXT;
