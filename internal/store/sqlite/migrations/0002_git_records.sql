-- Git state recorded by the loop. Empty string means "not recorded".
-- commit_hash avoids the COMMIT keyword.
ALTER TABLE milestones ADD COLUMN branch      TEXT NOT NULL DEFAULT '';
ALTER TABLE milestones ADD COLUMN base_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE milestones ADD COLUMN head_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks      ADD COLUMN commit_hash TEXT NOT NULL DEFAULT '';
