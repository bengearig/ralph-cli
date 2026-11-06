CREATE TABLE initiatives (
    id         INTEGER PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    title      TEXT NOT NULL,
    brief      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE milestones (
    id            INTEGER PRIMARY KEY,
    initiative_id INTEGER NOT NULL REFERENCES initiatives (id) ON DELETE CASCADE,
    slug          TEXT NOT NULL,
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    position      INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE (initiative_id, slug)
);

CREATE TABLE tasks (
    id                  INTEGER PRIMARY KEY,
    milestone_id        INTEGER NOT NULL REFERENCES milestones (id) ON DELETE CASCADE,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL CHECK (status IN ('PROPOSED', 'TODO', 'IN_PROGRESS', 'BLOCKED', 'CANCELLED', 'DONE')),
    position            INTEGER NOT NULL,
    failure_count       INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    blocked_reason      TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);
CREATE INDEX tasks_milestone_position ON tasks (milestone_id, position);
CREATE INDEX tasks_status ON tasks (status);

CREATE TABLE task_notes (
    id         INTEGER PRIMARY KEY,
    task_id    INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('NOTE', 'KICKBACK', 'ITERATION')),
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX task_notes_task ON task_notes (task_id, id);

CREATE TABLE documents (
    id            INTEGER PRIMARY KEY,
    initiative_id INTEGER REFERENCES initiatives (id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('brief', 'architecture', 'design', 'decision', 'reference', 'note')),
    slug          TEXT NOT NULL,
    title         TEXT NOT NULL,
    body          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
-- Project-scoped documents have a NULL initiative; IFNULL makes their slugs unique too.
CREATE UNIQUE INDEX documents_scope_slug ON documents (IFNULL(initiative_id, 0), slug);

CREATE TABLE chunks (
    id            INTEGER PRIMARY KEY,
    owner_kind    TEXT NOT NULL CHECK (owner_kind IN ('initiative', 'milestone', 'task', 'document')),
    owner_id      INTEGER NOT NULL,
    initiative_id INTEGER,
    seq           INTEGER NOT NULL,
    text          TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    model         TEXT NOT NULL DEFAULT '',
    dims          INTEGER NOT NULL DEFAULT 0,
    embedding     BLOB,
    UNIQUE (owner_kind, owner_id, seq)
);
CREATE INDEX chunks_model ON chunks (model);
CREATE INDEX chunks_initiative ON chunks (initiative_id);

CREATE VIRTUAL TABLE chunks_fts USING fts5 (
    text,
    content = 'chunks',
    content_rowid = 'id',
    tokenize = 'porter unicode61'
);

CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts (rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER chunks_au AFTER UPDATE OF text ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO chunks_fts (rowid, text) VALUES (new.id, new.text);
END;

-- Chunks reference owners polymorphically, so foreign keys cannot clean them up.
CREATE TRIGGER initiatives_ad AFTER DELETE ON initiatives BEGIN
    DELETE FROM chunks WHERE owner_kind = 'initiative' AND owner_id = old.id;
END;
CREATE TRIGGER milestones_ad AFTER DELETE ON milestones BEGIN
    DELETE FROM chunks WHERE owner_kind = 'milestone' AND owner_id = old.id;
END;
CREATE TRIGGER tasks_ad AFTER DELETE ON tasks BEGIN
    DELETE FROM chunks WHERE owner_kind = 'task' AND owner_id = old.id;
END;
CREATE TRIGGER documents_ad AFTER DELETE ON documents BEGIN
    DELETE FROM chunks WHERE owner_kind = 'document' AND owner_id = old.id;
END;
