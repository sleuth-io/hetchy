CREATE INDEX IF NOT EXISTS conversations_pr_state_url_lookup_idx
    ON conversations (org_id, lower(github_owner), lower(github_repo), pr_url)
    WHERE pr_url <> '';
