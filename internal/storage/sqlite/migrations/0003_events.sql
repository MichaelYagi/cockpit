-- Persisted events (Phase 3). seq is assigned by the in-memory log and is
-- the cursor key for both memory and storage. Times are Unix ms, UTC.
CREATE TABLE events (
    seq      INTEGER PRIMARY KEY,
    id       TEXT    NOT NULL UNIQUE,
    time     INTEGER NOT NULL,
    type     TEXT    NOT NULL,
    severity TEXT    NOT NULL,
    subject  TEXT    NOT NULL,
    message  TEXT    NOT NULL
) STRICT;

CREATE INDEX events_time ON events (time);
