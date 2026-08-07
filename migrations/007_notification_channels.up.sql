-- Notifications were a single row: the settings handler did DELETE-then-INSERT over
-- the whole table, so configuring one channel destroyed the other. That was fine
-- while email was the only channel and is exactly wrong now that a webhook can be
-- configured alongside it.
--
-- One row per type, enforced by the database rather than by the handler remembering
-- to. Duplicates cannot exist today (the DELETE saw to that), but a table that has
-- only ever been written by "delete everything, insert one" has no guarantee to lean
-- on, so collapse first and keep the newest row per type.
DELETE FROM notification_settings a
USING notification_settings b
WHERE a.type = b.type
  AND a.id < b.id;

CREATE UNIQUE INDEX idx_notification_settings_type ON notification_settings(type);

-- Values written to `type`: 'email' and 'webhook' (internal/models).
