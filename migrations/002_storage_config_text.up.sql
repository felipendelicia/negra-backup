-- Change storage config column from JSONB to TEXT to store AES-encrypted ciphertext.
--
-- The `_ciphertext` case unwraps values produced by this migration's own down step,
-- which has to wrap non-JSON ciphertext to convert back to JSONB at all. Without it,
-- a down-then-up cycle would leave the ciphertext buried inside a JSON object and
-- every decrypt would fail.
ALTER TABLE storage_destinations
    ALTER COLUMN config TYPE TEXT
    USING CASE
        WHEN jsonb_typeof(config) = 'object' AND config ? '_ciphertext'
            THEN config ->> '_ciphertext'
        ELSE config::text
    END;

ALTER TABLE notification_settings
    ALTER COLUMN config TYPE TEXT
    USING CASE
        WHEN jsonb_typeof(config) = 'object' AND config ? '_ciphertext'
            THEN config ->> '_ciphertext'
        ELSE config::text
    END;
