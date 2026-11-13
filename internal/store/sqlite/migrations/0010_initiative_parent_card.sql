-- The Jira key of the card the initiative's card is attached to; empty means none.
ALTER TABLE initiatives ADD COLUMN parent_card TEXT NOT NULL DEFAULT '';
