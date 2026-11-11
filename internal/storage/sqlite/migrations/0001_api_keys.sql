-- API keys (D5). Only the SHA-256 verifier is stored; never the key.
-- Times are Unix milliseconds, UTC.
CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT    NOT NULL,
    hash         BLOB    NOT NULL,
    scopes       TEXT    NOT NULL, -- space-separated, sorted
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    last_used_at INTEGER,
    revoked_at   INTEGER,
    rotated_from TEXT REFERENCES api_keys(id)
) STRICT;

CREATE INDEX api_keys_active ON api_keys (revoked_at, expires_at);
