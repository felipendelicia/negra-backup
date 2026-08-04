-- The dead-man's switch needs to remember which alerts are already open.
--
-- Without that memory an agent that has been down for five days produces one email
-- per check interval — 1440 of them at a five-minute tick — which trains everyone to
-- filter the alerts out, and that is exactly the failure this feature exists to
-- prevent. The state has to survive a restart for two independent reasons:
--
--   1. A server that restarts (deploy, crash loop, OOM) would otherwise re-send every
--      open alert on boot. A crash loop turns into a mail loop.
--   2. Recovery notices are only possible if we still know an alert was open. With
--      in-memory state, a server restarted while an agent was down would never send
--      "the agent came back" — it would have forgotten it was ever gone.
--
-- Hence a table and not a map guarded by a mutex.
--
-- There is no foreign key: subject_id points at either agents(id) or backup_jobs(id)
-- depending on subject_type, which no single FK can express. The checker deletes rows
-- whose subject no longer exists on every pass, so a deleted agent does not leave an
-- open alert behind that would later "recover" out of nowhere.
CREATE TABLE alert_state (
    subject_type TEXT NOT NULL,
    subject_id   UUID NOT NULL,
    kind         TEXT NOT NULL,
    -- When the condition was first observed. Kept across re-notifications so the
    -- email can say how long the thing has been broken.
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- NULL means "the condition is open but nobody has been told yet", which happens
    -- when SMTP itself is down. The checker retries on the next tick instead of
    -- marking the alert delivered and going quiet.
    last_notified_at TIMESTAMPTZ,
    -- 0 until the first successful send. A row that resolves with notify_count = 0
    -- is dropped silently: sending "recovered" for an alert nobody received is noise.
    notify_count INTEGER NOT NULL DEFAULT 0,
    detail TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (subject_type, subject_id, kind)
);

CREATE INDEX idx_alert_state_kind ON alert_state(kind);
