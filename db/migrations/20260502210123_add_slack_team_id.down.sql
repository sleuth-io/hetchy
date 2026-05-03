DROP INDEX IF EXISTS org_configs_slack_team_id_idx;
ALTER TABLE org_configs DROP COLUMN IF EXISTS slack_team_id;
