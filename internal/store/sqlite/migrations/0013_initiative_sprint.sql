-- The name of the sprint the initiative's milestone cards are added to; empty means none.
ALTER TABLE initiatives ADD COLUMN sprint TEXT NOT NULL DEFAULT '';
