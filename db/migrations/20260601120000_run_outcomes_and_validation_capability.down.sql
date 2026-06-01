ALTER TABLE repo_setup_specs
    DROP COLUMN IF EXISTS validation_capability;

ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS quality_score,
    DROP COLUMN IF EXISTS outcome_detail,
    DROP COLUMN IF EXISTS outcome;
