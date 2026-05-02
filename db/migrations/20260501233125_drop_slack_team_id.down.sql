ALTER TABLE org_configs ADD COLUMN slack_team_id TEXT;

CREATE UNIQUE INDEX org_configs_slack_team_id_idx
    ON org_configs (slack_team_id)
    WHERE slack_team_id IS NOT NULL;
