-- Task manager card records, keyed by the polymorphic entity. No foreign key is possible, so the store's delete
-- methods remove an entity's cards. Empty strings mean unset.
CREATE TABLE cards (
    kind         TEXT    NOT NULL CHECK (kind IN ('initiative', 'milestone', 'task')),
    entity_id    INTEGER NOT NULL,
    provider     TEXT    NOT NULL,
    card_key     TEXT    NOT NULL DEFAULT '',
    url          TEXT    NOT NULL DEFAULT '',
    content_hash TEXT    NOT NULL DEFAULT '',
    last_error   TEXT    NOT NULL DEFAULT '',
    attempted_at TEXT    NOT NULL DEFAULT '',
    synced_at    TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (kind, entity_id, provider)
);
