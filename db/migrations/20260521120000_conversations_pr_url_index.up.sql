-- FindConversationByPRURL filters `WHERE org_id = $1 AND pr_url = $2`
-- on the conversations table. Without an index it sequentially scans
-- the whole table on every inbound GitHub PR comment webhook. Skip
-- empty pr_url values (the awaiting-repo / first-turn state) so the
-- index stays compact even for orgs with lots of pre-PR conversations.
CREATE INDEX IF NOT EXISTS conversations_org_pr_url_idx
    ON conversations (org_id, pr_url)
    WHERE pr_url <> '';
