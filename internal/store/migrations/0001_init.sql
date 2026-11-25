-- Books in the owner's library, as last seen by the reconciler.
CREATE TABLE books (
    asin          TEXT PRIMARY KEY,
    title         TEXT NOT NULL,
    thumbnail     TEXT NOT NULL DEFAULT '',
    series_id     INTEGER REFERENCES series(id) ON DELETE SET NULL,
    series_index  INTEGER,
    first_seen_at TEXT NOT NULL,
    removed_at    TEXT
);
CREATE INDEX books_series ON books(series_id, series_index);

CREATE TABLE series (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    -- NULL follows the global default; 0 hidden, 1 visible.
    default_visible INTEGER
);

-- Family members and the owner. role is 'family' or 'admin'.
CREATE TABLE members (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL,
    email           TEXT NOT NULL UNIQUE COLLATE NOCASE,
    role            TEXT NOT NULL CHECK (role IN ('family', 'admin')),
    kids_profile_id TEXT UNIQUE,
    line_user_id    TEXT
);

-- Per-book visibility, shared by every family member. A missing row falls
-- back to the series default, then to the global default.
CREATE TABLE visibility (
    asin    TEXT PRIMARY KEY REFERENCES books(asin) ON DELETE CASCADE,
    visible INTEGER NOT NULL
);

-- What each member put on their shelf: the desired state.
CREATE TABLE selections (
    member_id   INTEGER NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    asin        TEXT NOT NULL REFERENCES books(asin) ON DELETE CASCADE,
    selected_at TEXT NOT NULL,
    PRIMARY KEY (member_id, asin)
);

-- What Amazon actually shares, keyed by kids profile so shares of titles
-- that are not in the library (e.g. Kindle Unlimited) are kept too.
CREATE TABLE shared_state (
    kids_profile_id TEXT NOT NULL,
    asin            TEXT NOT NULL,
    observed_at     TEXT NOT NULL,
    PRIMARY KEY (kids_profile_id, asin)
);

CREATE TABLE jobs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL,
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    result      TEXT,
    detail      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO settings (key, value) VALUES ('default_visible', '0');
