-- Dropping the constraint cannot restore the rows the up-migration collapsed, and
-- must not: they were duplicates of the same channel.
DROP INDEX IF EXISTS idx_notification_settings_type;

-- The old single-row handler cannot cope with more than one channel, so leave the
-- table in the shape it expects.
DELETE FROM notification_settings WHERE type <> 'email';
