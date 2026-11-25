-- Where a book's series came from: 'manual' (set on the admin page),
-- 'rule' (a series rule), or NULL (the built-in guess). Reclassifying
-- leaves manual ones alone.
ALTER TABLE books ADD COLUMN series_source TEXT;
UPDATE books SET series_source = 'manual' WHERE series_index = -1;

-- Groupings from the owner's earlier Notion setup: series whose titles do
-- not carry a shared name. They can be deleted on the admin page.
INSERT INTO series_rules (pattern, series, created_at) VALUES
    ('^(続・)?.物語(\(.\))?', '物語シリーズ', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    ('クビキリサイクル|クビシメロマンチスト|クビツリハイスクール|サイコロジカル|ヒトクイマジカル|ネコソギラジカル|キドナプキディング', '戯言シリーズ', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    ('^零崎..の人間', '人間シリーズ', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
