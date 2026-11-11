-- The pull request opened for a milestone, as provider-neutral coordinates; the URL is derived, not stored.
ALTER TABLE milestones ADD COLUMN pr_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE milestones ADD COLUMN pr_namespace TEXT NOT NULL DEFAULT '';
ALTER TABLE milestones ADD COLUMN pr_repository TEXT NOT NULL DEFAULT '';
ALTER TABLE milestones ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0;
