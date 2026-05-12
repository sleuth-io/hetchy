ALTER TABLE org_configs ADD COLUMN theme_preference TEXT NOT NULL DEFAULT 'system';

CREATE INDEX org_configs_theme_pref_idx ON org_configs (theme_preference);
