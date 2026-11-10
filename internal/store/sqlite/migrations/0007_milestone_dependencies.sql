-- Milestone dependencies: a milestone waits on others, possibly in other initiatives.
CREATE TABLE milestone_dependencies (
    milestone_id  INTEGER NOT NULL REFERENCES milestones (id) ON DELETE CASCADE,
    depends_on_id INTEGER NOT NULL REFERENCES milestones (id) ON DELETE CASCADE,
    created_at    TEXT NOT NULL,
    PRIMARY KEY (milestone_id, depends_on_id),
    CHECK (milestone_id <> depends_on_id)
);

CREATE INDEX milestone_dependencies_depends_on ON milestone_dependencies (depends_on_id);
