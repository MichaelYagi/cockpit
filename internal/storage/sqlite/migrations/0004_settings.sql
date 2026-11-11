-- Runtime settings overrides (D19). updated_at is Unix ms, UTC.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      BLOB    NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
