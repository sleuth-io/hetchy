ALTER TABLE agent_runs
    ADD COLUMN outcome TEXT NOT NULL DEFAULT '',
    ADD COLUMN outcome_detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN quality_score INT;

ALTER TABLE repo_setup_specs
    ADD COLUMN validation_capability JSONB NOT NULL DEFAULT '{}'::jsonb;
