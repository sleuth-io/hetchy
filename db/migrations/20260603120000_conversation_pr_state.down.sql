ALTER TABLE conversations
    DROP CONSTRAINT IF EXISTS conversations_pr_state_known,
    DROP COLUMN IF EXISTS pr_state_checked_at,
    DROP COLUMN IF EXISTS pr_closed_at,
    DROP COLUMN IF EXISTS pr_merged_at,
    DROP COLUMN IF EXISTS pr_merged,
    DROP COLUMN IF EXISTS pr_state;
