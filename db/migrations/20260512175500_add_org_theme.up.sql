-- Add theme preference to org_configs. Valid values: 'light', 'dark', 'system'.
-- Defaults to 'system' so existing users get the current behavior.
ALTER TABLE org_configs ADD COLUMN theme TEXT NOT NULL DEFAULT 'system';
