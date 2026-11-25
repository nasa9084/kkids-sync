-- Rules the owner writes to put titles into a series, tried before the
-- built-in guess (titles.ParseSeries). pattern is a Go regexp matched
-- against the NFKC-normalized title; a group named vol gives the volume.
CREATE TABLE series_rules (
    id         INTEGER PRIMARY KEY,
    pattern    TEXT NOT NULL,
    series     TEXT NOT NULL,
    created_at TEXT NOT NULL
);
