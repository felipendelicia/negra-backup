-- Change storage config column from JSONB to TEXT to store AES-encrypted ciphertext
ALTER TABLE storage_destinations ALTER COLUMN config TYPE TEXT USING config::text;
ALTER TABLE notification_settings ALTER COLUMN config TYPE TEXT USING config::text;
