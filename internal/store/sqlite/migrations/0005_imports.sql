-- Import records: one per project-relative path, plus the documents each produced.
CREATE TABLE imports (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    path           TEXT NOT NULL UNIQUE,
    status         TEXT NOT NULL CHECK (status IN ('pending','done','skipped','deferred')),
    content        TEXT NOT NULL,
    content_hash   TEXT NOT NULL,
    size           INTEGER NOT NULL,
    last_done_hash TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    queued_at      TEXT NOT NULL,
    processed_at   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE import_documents (
    import_id   INTEGER NOT NULL REFERENCES imports (id) ON DELETE CASCADE,
    document_id INTEGER NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    relation    TEXT NOT NULL CHECK (relation IN ('created','updated','decision')),
    created_at  TEXT NOT NULL,
    PRIMARY KEY (import_id, document_id, relation)
);
