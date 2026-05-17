-- name: EnsureBillingAccount :one
INSERT INTO billing_accounts (org_id)
VALUES ($1)
ON CONFLICT (org_id) DO UPDATE SET org_id = EXCLUDED.org_id
RETURNING org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
          current_period_start, current_period_end,
          included_credits, included_credits_used, topup_credits,
          max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
          created_at, updated_at;

-- name: GetBillingAccount :one
SELECT org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
       current_period_start, current_period_end,
       included_credits, included_credits_used, topup_credits,
       max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
       created_at, updated_at
FROM billing_accounts
WHERE org_id = $1;

-- name: GetBillingAccountByStripeCustomer :one
SELECT org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
       current_period_start, current_period_end,
       included_credits, included_credits_used, topup_credits,
       max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
       created_at, updated_at
FROM billing_accounts
WHERE stripe_customer_id = $1;

-- name: LockBillingAccountForUpdate :one
SELECT org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
       current_period_start, current_period_end,
       included_credits, included_credits_used, topup_credits,
       max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
       created_at, updated_at
FROM billing_accounts
WHERE org_id = $1
FOR UPDATE;

-- name: UpsertBillingAccountMirror :one
INSERT INTO billing_accounts (
    org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
    current_period_start, current_period_end,
    included_credits, max_flavor, per_run_max_credits, billing_exempt,
    last_payment_error
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
ON CONFLICT (org_id) DO UPDATE SET
    stripe_customer_id     = EXCLUDED.stripe_customer_id,
    stripe_subscription_id = EXCLUDED.stripe_subscription_id,
    plan_code              = EXCLUDED.plan_code,
    status                 = EXCLUDED.status,
    current_period_start   = EXCLUDED.current_period_start,
    current_period_end     = EXCLUDED.current_period_end,
    included_credits       = EXCLUDED.included_credits,
    included_credits_used  = CASE
        WHEN billing_accounts.current_period_start IS DISTINCT FROM EXCLUDED.current_period_start
          OR billing_accounts.current_period_end IS DISTINCT FROM EXCLUDED.current_period_end
        THEN 0
        ELSE LEAST(billing_accounts.included_credits_used, EXCLUDED.included_credits)
    END,
    max_flavor             = EXCLUDED.max_flavor,
    per_run_max_credits    = EXCLUDED.per_run_max_credits,
    billing_exempt         = EXCLUDED.billing_exempt,
    last_payment_error     = EXCLUDED.last_payment_error,
    updated_at             = NOW()
RETURNING org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
          current_period_start, current_period_end,
          included_credits, included_credits_used, topup_credits,
          max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
          created_at, updated_at;

-- name: UpdateBillingStripeCustomer :one
UPDATE billing_accounts
   SET stripe_customer_id = $2,
       updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
          current_period_start, current_period_end,
          included_credits, included_credits_used, topup_credits,
          max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
          created_at, updated_at;

-- name: GrantBillingTopupCredits :one
UPDATE billing_accounts
   SET topup_credits = topup_credits + $2,
       last_payment_error = '',
       updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
          current_period_start, current_period_end,
          included_credits, included_credits_used, topup_credits,
          max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
          created_at, updated_at;

-- name: SetBillingLastPaymentError :exec
UPDATE billing_accounts
   SET last_payment_error = $2,
       updated_at = NOW()
WHERE org_id = $1;

-- name: UpdateBillingReservedBalances :one
UPDATE billing_accounts
   SET included_credits_used = included_credits_used + $2,
       topup_credits = topup_credits - $3,
       updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
          current_period_start, current_period_end,
          included_credits, included_credits_used, topup_credits,
          max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
          created_at, updated_at;

-- name: UpdateBillingCapturedBalances :one
UPDATE billing_accounts
   SET included_credits_used = GREATEST(included_credits_used + $2, 0),
       topup_credits = GREATEST(topup_credits + $3, 0),
       updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, stripe_customer_id, stripe_subscription_id, plan_code, status,
          current_period_start, current_period_end,
          included_credits, included_credits_used, topup_credits,
          max_flavor, per_run_max_credits, billing_exempt, last_payment_error,
          created_at, updated_at;

-- name: EnsureBillingTopupSettings :one
INSERT INTO billing_topup_settings (org_id)
VALUES ($1)
ON CONFLICT (org_id) DO UPDATE SET org_id = EXCLUDED.org_id
RETURNING org_id, auto_topup_enabled, trigger_threshold, target_balance,
          monthly_max_units, monthly_units_used, monthly_anchor_month,
          created_at, updated_at;

-- name: GetBillingTopupSettings :one
SELECT org_id, auto_topup_enabled, trigger_threshold, target_balance,
       monthly_max_units, monthly_units_used, monthly_anchor_month,
       created_at, updated_at
FROM billing_topup_settings
WHERE org_id = $1;

-- name: LockBillingTopupSettingsForUpdate :one
SELECT org_id, auto_topup_enabled, trigger_threshold, target_balance,
       monthly_max_units, monthly_units_used, monthly_anchor_month,
       created_at, updated_at
FROM billing_topup_settings
WHERE org_id = $1
FOR UPDATE;

-- name: UpdateBillingTopupSettings :one
UPDATE billing_topup_settings
   SET auto_topup_enabled = $2,
       trigger_threshold = $3,
       target_balance = $4,
       monthly_max_units = $5,
       updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, auto_topup_enabled, trigger_threshold, target_balance,
          monthly_max_units, monthly_units_used, monthly_anchor_month,
          created_at, updated_at;

-- name: IncrementBillingTopupMonthlyUnits :one
UPDATE billing_topup_settings
   SET monthly_units_used = monthly_units_used + $2,
       monthly_anchor_month = $3,
       updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, auto_topup_enabled, trigger_threshold, target_balance,
          monthly_max_units, monthly_units_used, monthly_anchor_month,
          created_at, updated_at;

-- name: ResetBillingTopupMonthlyUsage :one
UPDATE billing_topup_settings
   SET monthly_units_used = 0,
       monthly_anchor_month = $2,
       updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, auto_topup_enabled, trigger_threshold, target_balance,
          monthly_max_units, monthly_units_used, monthly_anchor_month,
          created_at, updated_at;

-- name: GetBillingCreditReservationForUpdate :one
SELECT run_id, org_id, reserved_credits, from_included_credits, from_topup_credits,
       captured_credits, released_credits, status, created_at, updated_at
FROM billing_credit_reservations
WHERE run_id = $1
FOR UPDATE;

-- name: GetBillingCreditReservation :one
SELECT run_id, org_id, reserved_credits, from_included_credits, from_topup_credits,
       captured_credits, released_credits, status, created_at, updated_at
FROM billing_credit_reservations
WHERE run_id = $1;

-- name: InsertBillingCreditReservation :one
INSERT INTO billing_credit_reservations (
    run_id, org_id, reserved_credits, from_included_credits, from_topup_credits, status
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING run_id, org_id, reserved_credits, from_included_credits, from_topup_credits,
          captured_credits, released_credits, status, created_at, updated_at;

-- name: UpdateBillingCreditReservationCaptured :one
UPDATE billing_credit_reservations
   SET captured_credits = $2,
       released_credits = $3,
       status = $4,
       updated_at = NOW()
WHERE run_id = $1
RETURNING run_id, org_id, reserved_credits, from_included_credits, from_topup_credits,
          captured_credits, released_credits, status, created_at, updated_at;

-- name: UpsertBillingRunMeterStart :one
INSERT INTO billing_run_meters (
    run_id, org_id, flavor, multiplier, sandbox_vcpu, sandbox_memory_gib,
    sandbox_disk_gib, started_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (run_id) DO UPDATE SET
    flavor = EXCLUDED.flavor,
    multiplier = EXCLUDED.multiplier,
    sandbox_vcpu = EXCLUDED.sandbox_vcpu,
    sandbox_memory_gib = EXCLUDED.sandbox_memory_gib,
    sandbox_disk_gib = EXCLUDED.sandbox_disk_gib,
    updated_at = NOW()
RETURNING run_id, org_id, flavor, multiplier, sandbox_vcpu, sandbox_memory_gib,
          sandbox_disk_gib, started_at, ended_at, billable_minutes,
          captured_credits, terminal_state, created_at, updated_at;

-- name: GetBillingRunMeterForUpdate :one
SELECT run_id, org_id, flavor, multiplier, sandbox_vcpu, sandbox_memory_gib,
       sandbox_disk_gib, started_at, ended_at, billable_minutes,
       captured_credits, terminal_state, created_at, updated_at
FROM billing_run_meters
WHERE run_id = $1
FOR UPDATE;

-- name: FinalizeBillingRunMeter :one
UPDATE billing_run_meters
   SET ended_at = $2,
       billable_minutes = $3,
       captured_credits = $4,
       terminal_state = $5,
       updated_at = NOW()
WHERE run_id = $1
RETURNING run_id, org_id, flavor, multiplier, sandbox_vcpu, sandbox_memory_gib,
          sandbox_disk_gib, started_at, ended_at, billable_minutes,
          captured_credits, terminal_state, created_at, updated_at;

-- name: ListBillingRunMetersByOrg :many
SELECT run_id, org_id, flavor, multiplier, sandbox_vcpu, sandbox_memory_gib,
       sandbox_disk_gib, started_at, ended_at, billable_minutes,
       captured_credits, terminal_state, created_at, updated_at
FROM billing_run_meters
WHERE org_id = $1
ORDER BY started_at DESC
LIMIT $2;

-- name: GetRepoBillingSetting :one
SELECT org_id, github_owner, github_repo, flavor, created_at, updated_at
FROM repo_billing_settings
WHERE org_id = $1 AND github_owner = $2 AND github_repo = $3;

-- name: ListRepoBillingSettingsByOrg :many
SELECT org_id, github_owner, github_repo, flavor, created_at, updated_at
FROM repo_billing_settings
WHERE org_id = $1
ORDER BY github_owner, github_repo;

-- name: UpsertRepoBillingSetting :one
INSERT INTO repo_billing_settings (org_id, github_owner, github_repo, flavor)
VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id, github_owner, github_repo) DO UPDATE SET
    flavor = EXCLUDED.flavor,
    updated_at = NOW()
RETURNING org_id, github_owner, github_repo, flavor, created_at, updated_at;

-- name: DeleteRepoBillingSetting :exec
DELETE FROM repo_billing_settings
WHERE org_id = $1 AND github_owner = $2 AND github_repo = $3;

-- name: InsertBillingStripeEvent :one
WITH inserted AS (
    INSERT INTO billing_stripe_events (event_id, event_type, org_id)
    VALUES ($1, $2, $3)
    ON CONFLICT (event_id) DO NOTHING
    RETURNING event_id
)
SELECT EXISTS(SELECT 1 FROM inserted)::boolean AS inserted;
