ALTER TABLE conversations
    ADD COLUMN pr_state TEXT NOT NULL DEFAULT '',
    ADD COLUMN pr_merged BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN pr_merged_at TIMESTAMPTZ,
    ADD COLUMN pr_closed_at TIMESTAMPTZ,
    ADD COLUMN pr_state_checked_at TIMESTAMPTZ,
    ADD CONSTRAINT conversations_pr_state_known
        CHECK (pr_state IN ('', 'open', 'closed'));
