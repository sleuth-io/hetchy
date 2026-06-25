-- Add the model column used to execute a job. Existing jobs default to
-- Opus so their behaviour is unchanged after the column lands.
ALTER TABLE agent_jobs
    ADD COLUMN model TEXT NOT NULL DEFAULT 'opus'
        CHECK (length(btrim(model)) > 0);
