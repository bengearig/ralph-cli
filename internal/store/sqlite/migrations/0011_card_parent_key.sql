-- The parent card key last sent to the task manager for this card; empty means none.
ALTER TABLE cards ADD COLUMN parent_key TEXT NOT NULL DEFAULT '';
