CREATE TABLE IF NOT EXISTS billing_accounts (
    org_id                 TEXT PRIMARY KEY,
    stripe_customer_id     TEXT NOT NULL DEFAULT '',
    stripe_subscription_id TEXT NOT NULL DEFAULT '',
    plan_code              TEXT NOT NULL DEFAULT 'free',
    status                 TEXT NOT NULL DEFAULT 'free',
    current_period_start   TIMESTAMPTZ,
    current_period_end     TIMESTAMPTZ,
    included_credits       INTEGER NOT NULL DEFAULT 10 CHECK (included_credits >= 0),
    included_credits_used  INTEGER NOT NULL DEFAULT 0 CHECK (included_credits_used >= 0),
    topup_credits          INTEGER NOT NULL DEFAULT 0 CHECK (topup_credits >= 0),
    max_flavor             TEXT NOT NULL DEFAULT 'standard'
        CHECK (max_flavor IN ('standard', 'pro', 'max', 'enterprise')),
    per_run_max_credits    INTEGER NOT NULL DEFAULT 4 CHECK (per_run_max_credits >= 1),
    billing_exempt         BOOLEAN NOT NULL DEFAULT FALSE,
    last_payment_error     TEXT NOT NULL DEFAULT '',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS billing_accounts_stripe_customer_idx
    ON billing_accounts (stripe_customer_id)
    WHERE stripe_customer_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS billing_accounts_stripe_subscription_idx
    ON billing_accounts (stripe_subscription_id)
    WHERE stripe_subscription_id <> '';

CREATE TABLE IF NOT EXISTS billing_topup_settings (
    org_id                TEXT PRIMARY KEY REFERENCES billing_accounts(org_id) ON DELETE CASCADE,
    auto_topup_enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    trigger_threshold     INTEGER NOT NULL DEFAULT 2 CHECK (trigger_threshold >= 0),
    target_balance        INTEGER NOT NULL DEFAULT 10 CHECK (target_balance >= 0),
    monthly_max_units     INTEGER NOT NULL DEFAULT 0 CHECK (monthly_max_units >= 0),
    monthly_units_used    INTEGER NOT NULL DEFAULT 0 CHECK (monthly_units_used >= 0),
    monthly_anchor_month  TEXT NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM'),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS billing_credit_reservations (
    run_id                TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
    org_id                TEXT NOT NULL,
    reserved_credits      INTEGER NOT NULL DEFAULT 0 CHECK (reserved_credits >= 0),
    from_included_credits INTEGER NOT NULL DEFAULT 0 CHECK (from_included_credits >= 0),
    from_topup_credits    INTEGER NOT NULL DEFAULT 0 CHECK (from_topup_credits >= 0),
    captured_credits      INTEGER NOT NULL DEFAULT 0 CHECK (captured_credits >= 0),
    released_credits      INTEGER NOT NULL DEFAULT 0 CHECK (released_credits >= 0),
    status                TEXT NOT NULL DEFAULT 'reserved'
        CHECK (status IN ('reserved', 'captured', 'released', 'comped')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS billing_credit_reservations_org_idx
    ON billing_credit_reservations (org_id, created_at DESC);

CREATE TABLE IF NOT EXISTS billing_run_meters (
    run_id           TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
    org_id           TEXT NOT NULL,
    flavor           TEXT NOT NULL DEFAULT 'standard',
    multiplier       INTEGER NOT NULL DEFAULT 1 CHECK (multiplier >= 1),
    sandbox_vcpu     INTEGER NOT NULL DEFAULT 2 CHECK (sandbox_vcpu >= 1),
    sandbox_memory_gib INTEGER NOT NULL DEFAULT 3 CHECK (sandbox_memory_gib >= 1),
    sandbox_disk_gib INTEGER NOT NULL DEFAULT 4 CHECK (sandbox_disk_gib >= 1),
    started_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at         TIMESTAMPTZ,
    billable_minutes INTEGER NOT NULL DEFAULT 0 CHECK (billable_minutes >= 0),
    captured_credits INTEGER NOT NULL DEFAULT 0 CHECK (captured_credits >= 0),
    terminal_state   TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS billing_run_meters_org_started_idx
    ON billing_run_meters (org_id, started_at DESC);

CREATE TABLE IF NOT EXISTS repo_billing_settings (
    org_id        TEXT NOT NULL,
    github_owner  TEXT NOT NULL,
    github_repo   TEXT NOT NULL,
    flavor        TEXT NOT NULL
        CHECK (flavor IN ('standard', 'pro', 'max', 'enterprise')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, github_owner, github_repo)
);

CREATE INDEX IF NOT EXISTS repo_billing_settings_org_idx
    ON repo_billing_settings (org_id);

CREATE TABLE IF NOT EXISTS billing_stripe_events (
    event_id    TEXT PRIMARY KEY,
    event_type  TEXT NOT NULL,
    org_id      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
