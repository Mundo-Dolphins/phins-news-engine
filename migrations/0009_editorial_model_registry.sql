ALTER TABLE model_registry ADD COLUMN algorithm TEXT NOT NULL DEFAULT '';
ALTER TABLE model_registry ADD COLUMN feature_schema_version TEXT NOT NULL DEFAULT '';
ALTER TABLE model_registry ADD COLUMN dataset_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE model_registry ADD COLUMN thresholds TEXT NOT NULL DEFAULT '{}';
ALTER TABLE model_registry ADD COLUMN config TEXT NOT NULL DEFAULT '{}';
ALTER TABLE model_registry ADD COLUMN artifact_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE model_registry ADD COLUMN status TEXT NOT NULL DEFAULT 'candidate'
    CHECK (status IN ('candidate','validated','promoted','retired'));

UPDATE model_registry SET status = 'promoted' WHERE version = 'placeholder-v0';
