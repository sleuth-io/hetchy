BEGIN;

CREATE TABLE github_mention_threads (
    org_id TEXT NOT NULL,
    owner TEXT NOT NULL,
    repo TEXT NOT NULL,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('issue', 'pull_request')),
    subject_number INTEGER NOT NULL CHECK (subject_number > 0),
    thread_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, owner, repo, subject_type, subject_number)
);

CREATE TABLE github_mention_deliveries (
    org_id TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, delivery_id)
);

CREATE UNIQUE INDEX github_mention_deliveries_request_idx
    ON github_mention_deliveries (org_id, request_id);

COMMIT;
