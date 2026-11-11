-- Metric history (D18). Times are Unix seconds, UTC; rollup rows are
-- keyed by bucket start.
CREATE TABLE history_series (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
) STRICT;

CREATE TABLE history_raw (
    series INTEGER NOT NULL REFERENCES history_series(id) ON DELETE CASCADE,
    ts     INTEGER NOT NULL,
    value  REAL    NOT NULL,
    PRIMARY KEY (series, ts)
) STRICT, WITHOUT ROWID;

CREATE TABLE history_1m (
    series INTEGER NOT NULL REFERENCES history_series(id) ON DELETE CASCADE,
    ts     INTEGER NOT NULL,
    min    REAL    NOT NULL,
    avg    REAL    NOT NULL,
    max    REAL    NOT NULL,
    n      INTEGER NOT NULL,
    PRIMARY KEY (series, ts)
) STRICT, WITHOUT ROWID;

CREATE TABLE history_1h (
    series INTEGER NOT NULL REFERENCES history_series(id) ON DELETE CASCADE,
    ts     INTEGER NOT NULL,
    min    REAL    NOT NULL,
    avg    REAL    NOT NULL,
    max    REAL    NOT NULL,
    n      INTEGER NOT NULL,
    PRIMARY KEY (series, ts)
) STRICT, WITHOUT ROWID;
