-- ralph:rebuild
-- AUTOINCREMENT keeps IDs from being reused after the highest row is deleted.
CREATE TABLE initiatives_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT NOT NULL UNIQUE,
    title      TEXT NOT NULL,
    brief      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO initiatives_new (id, slug, title, brief, created_at, updated_at)
    SELECT id, slug, title, brief, created_at, updated_at FROM initiatives;
DROP TABLE initiatives;
ALTER TABLE initiatives_new RENAME TO initiatives;

CREATE TABLE milestones_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    initiative_id INTEGER NOT NULL REFERENCES initiatives (id) ON DELETE CASCADE,
    slug          TEXT NOT NULL,
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    position      INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    base_commit   TEXT NOT NULL DEFAULT '',
    head_commit   TEXT NOT NULL DEFAULT '',
    UNIQUE (initiative_id, slug)
);
INSERT INTO milestones_new (id, initiative_id, slug, title, description, position, created_at, updated_at, branch, base_commit, head_commit)
    SELECT id, initiative_id, slug, title, description, position, created_at, updated_at, branch, base_commit, head_commit FROM milestones;
DROP TABLE milestones;
ALTER TABLE milestones_new RENAME TO milestones;

CREATE TABLE tasks_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    milestone_id        INTEGER NOT NULL REFERENCES milestones (id) ON DELETE CASCADE,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL CHECK (status IN ('PROPOSED', 'TODO', 'IN_PROGRESS', 'BLOCKED', 'CANCELLED', 'DONE')),
    position            INTEGER NOT NULL,
    failure_count       INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    blocked_reason      TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    commit_hash         TEXT NOT NULL DEFAULT ''
);
INSERT INTO tasks_new (id, milestone_id, title, description, acceptance_criteria, status, position, failure_count, blocked_reason, created_at, updated_at, commit_hash)
    SELECT id, milestone_id, title, description, acceptance_criteria, status, position, failure_count, blocked_reason, created_at, updated_at, commit_hash FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
CREATE INDEX tasks_milestone_position ON tasks (milestone_id, position);
CREATE INDEX tasks_status ON tasks (status);

CREATE TABLE task_notes_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('NOTE', 'KICKBACK', 'ITERATION')),
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
INSERT INTO task_notes_new (id, task_id, kind, body, created_at)
    SELECT id, task_id, kind, body, created_at FROM task_notes;
DROP TABLE task_notes;
ALTER TABLE task_notes_new RENAME TO task_notes;
CREATE INDEX task_notes_task ON task_notes (task_id, id);

CREATE TABLE documents_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    initiative_id INTEGER REFERENCES initiatives (id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('brief', 'architecture', 'design', 'decision', 'reference', 'note')),
    slug          TEXT NOT NULL,
    title         TEXT NOT NULL,
    body          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
INSERT INTO documents_new (id, initiative_id, kind, slug, title, body, created_at, updated_at)
    SELECT id, initiative_id, kind, slug, title, body, created_at, updated_at FROM documents;
DROP TABLE documents;
ALTER TABLE documents_new RENAME TO documents;
CREATE UNIQUE INDEX documents_scope_slug ON documents (IFNULL(initiative_id, 0), slug);

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
