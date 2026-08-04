-- Migration 002 widened these columns to TEXT so they could hold AES-256-GCM
-- ciphertext, which is not JSON. A plain `config::jsonb` therefore aborts with
-- "invalid input syntax for type json" on any real installation and leaves the
-- schema dirty — reproduced against a row holding actual ciphertext.
--
-- Values that are still JSON convert directly. Anything else is wrapped in a JSON
-- object so the rollback neither fails nor destroys data; re-applying 002 restores
-- the original string from the wrapper.
ALTER TABLE storage_destinations
    ALTER COLUMN config TYPE JSONB
    USING CASE
        WHEN config ~ '^\s*[{\[]' THEN config::jsonb
        ELSE jsonb_build_object('_ciphertext', config)
    END;

ALTER TABLE notification_settings
    ALTER COLUMN config TYPE JSONB
    USING CASE
        WHEN config ~ '^\s*[{\[]' THEN config::jsonb
        ELSE jsonb_build_object('_ciphertext', config)
    END;
