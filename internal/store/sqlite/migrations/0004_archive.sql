-- Archive state. Empty string means "not archived"; otherwise the RFC 3339 time of archiving.
ALTER TABLE initiatives ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
ALTER TABLE documents   ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
