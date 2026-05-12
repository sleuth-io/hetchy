DROP INDEX IF EXISTS org_configs_theme_pref_idx;
ALTER TABLE org_configs DROP COLUMN IF EXISTS theme_preference;
