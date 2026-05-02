-- The slack_team_id column was unused: with per-org sockets, each
-- connection already knows which org it serves, so no team_id-to-org
-- lookup is needed. Dropping the column also scrubs any value users
-- pasted there during early dev (e.g., a misplaced GitHub token).
DROP INDEX IF EXISTS org_configs_slack_team_id_idx;
ALTER TABLE org_configs DROP COLUMN IF EXISTS slack_team_id;
