-- A document promoted to project scope remembers the initiative it came from. SET NULL keeps it if that initiative is deleted.
ALTER TABLE documents ADD COLUMN origin_initiative_id INTEGER REFERENCES initiatives (id) ON DELETE SET NULL;
