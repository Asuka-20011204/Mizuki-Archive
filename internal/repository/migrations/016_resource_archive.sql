ALTER TABLE resources ADD COLUMN archived_at DATETIME(6) NULL;
ALTER TABLE external_resources ADD COLUMN archived_at DATETIME(6) NULL;
