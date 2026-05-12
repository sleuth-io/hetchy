ALTER TABLE org_configs
ADD COLUMN theme TEXT NOT NULL DEFAULT 'system';

COMMENT ON COLUMN org_configs.theme IS 'UI theme preference: "system", "light", or "dark"';
