ALTER TABLE storage_destinations ALTER COLUMN config TYPE JSONB USING config::jsonb;
ALTER TABLE notification_settings ALTER COLUMN config TYPE JSONB USING config::jsonb;
