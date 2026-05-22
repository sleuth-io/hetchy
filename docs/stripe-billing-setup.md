# Stripe Billing Setup

This document describes the Stripe setup required for Hetchy's billing implementation.

## What Stripe Owns

Stripe owns:

- Paid subscription checkout.
- Manual top-up checkout.
- Saved payment methods.
- Auto top-up charges against saved customer payment methods.
- Customer billing portal sessions.
- Invoices, receipts, payment failures, and subscription lifecycle events.

Hetchy owns:

- Free and trial usage balances.
- Comped org enforcement via `billing_accounts.billing_exempt`, with WorkOS Feature Flags as the operator-facing source of truth.
- Local credit balance and real-time admission before Daytona starts.
- Repo sandbox flavor defaults in `repo_billing_settings`.
- Auto top-up reserve behavior and monthly max spend enforcement.
- Run metering and credit capture/release.

Do not configure Stripe Billing Credits or Stripe Meters for this launch. The app uses Hetchy-local credits and Stripe only as the payment and subscription source of truth.

## Implementation Contract

The app expects these environment variables:

| Variable | Value |
| --- | --- |
| `STRIPE_SECRET_KEY` | Stripe server-side secret key for the environment. Use `sk_test_...` or a restricted test key for dev/stage, and `sk_live_...` or a restricted live key for prod. |
| `STRIPE_WEBHOOK_SECRET` | Webhook signing secret for that environment's endpoint. Starts with `whsec_...`. |
| `STRIPE_SUBSCRIPTION_PRICE_IDS` | Comma-, semicolon-, or newline-separated map of paid plan code to recurring Price ID, for example `starter=price_...,team=price_...`. |
| `STRIPE_SUBSCRIPTION_PRICE_ID` | Legacy fallback recurring Stripe Price ID for the default Studio plan. Use only when a single subscription price is configured. |
| `STRIPE_TOPUP_PRICE_IDS` | Comma-, semicolon-, or newline-separated map of paid plan code to one-time 100-credit top-up Price ID. |
| `STRIPE_TOPUP_PRICE_ID` | Legacy fallback one-time top-up Price ID. Use only when a single top-up price is configured. |
| `STRIPE_RETURN_TO` | Public app root used for Stripe Checkout and Customer Portal return URLs. |

Comped orgs use WorkOS, not Stripe. Create a WorkOS Feature Flag with slug `hetchy-billing-comped`
and target organizations from the WorkOS dashboard. For environments with a public HTTPS app URL,
configure `WORKOS_WEBHOOK_SECRET` for the app's `POST /workos/webhook` endpoint. Local dev can omit
that secret because there is no public HTTPS webhook target; the billing settings page refreshes that
org's flag state from WorkOS before rendering, so dev and missed webhooks are repaired the next time
an admin opens Billing / Usage.

Also confirm `STRIPE_RETURN_TO` points at the public app root for the environment, because Checkout and Portal return URLs are built from `Config.StripeReturnBaseURL()`:

| Environment | Example |
| --- | --- |
| Dev | `http://dev.hetchy.ai:8080/` or the exact localhost/tunnel origin you use in the browser |
| Stage | `https://<stage-host>/` |
| Prod | `https://app.hetchy.ai/` |

The app exposes these billing-facing routes:

| Route | Purpose |
| --- | --- |
| `POST /billing/checkout` | Creates the first Stripe subscription, or switches the existing subscription to another plan. Admin-only. |
| `POST /billing/topup` | Creates a Stripe Checkout Session in payment mode. Admin-only. |
| `GET /billing/portal` | Creates a Stripe Customer Portal session and redirects to Stripe. Admin-only. |
| `POST /stripe/webhook` | Public Stripe webhook endpoint with signature verification. |
| `POST /workos/webhook` | Public WorkOS webhook endpoint with signature verification for comped-org feature flag changes. |

The Stripe webhook handler listens for these event types:

```text
checkout.session.completed
customer.subscription.created
customer.subscription.updated
customer.subscription.deleted
invoice.payment_failed
invoice.paid
```

Use snapshot events if Stripe asks you to choose between snapshot and thin events. The handler decodes the object included in the event payload.

The WorkOS webhook handler listens for `flag.rule_updated`, `flag.updated`, and `flag.deleted`
events for the `hetchy-billing-comped` flag.

## Products And Prices

Create the products and prices separately for each Stripe mode or Stripe account you use. Test/sandbox Price IDs cannot be used with live keys, and live Price IDs cannot be used with test keys.

The preferred provisioning path is the Stripe CLI sync script:

```bash
scripts/sync-stripe-billing.sh \
  --env prod \
  --live \
  --return-to https://app.hetchy.ai/ \
  --webhook-url https://app.hetchy.ai/stripe/webhook
```

Run it once per app environment against the intended Stripe account or mode. The script creates or updates the Hetchy products, creates replacement Prices when immutable Price fields change, configures the default Customer Portal when `--return-to` is provided, optionally creates or updates the Dashboard webhook endpoint, and prints the `STRIPE_SUBSCRIPTION_PRICE_IDS` and `STRIPE_TOPUP_PRICE_IDS` values to store in Doppler.

### Subscription Products

Create one monthly recurring price for each paid public plan:

| Plan | Product name | Amount | Included credits | Sandbox options | Per-run maximum |
| --- | --- | --- | ---: | --- | ---: |
| `starter` | `Hetchy Builder` | `$19.00` | 100 | `standard` | 4 |
| `team` | `Hetchy Studio` | `$79.00` | 500 | `standard, plus` | 12 |
| `growth` | `Hetchy Growth` | `$199.00` | 1,400 | `standard, plus` | 24 |
| `business` | `Hetchy Business` | `$499.00` | 3,600 | `standard, plus` | 48 |

Set the resulting Price IDs in `STRIPE_SUBSCRIPTION_PRICE_IDS`.

Plan changes happen through the app, not Customer Portal plan switching. The first paid plan uses Stripe Checkout. Later upgrades update the existing subscription item and invoice immediately; downgrades are scheduled for the next billing cycle. The app writes plan metadata onto the Stripe Subscription or scheduled phase, and the webhook mirrors those fields locally:

```text
plan_code=<starter|team|growth|business>
included_credits=<plan included credits>
max_flavor=<plan max flavor>
per_run_max_credits=<plan per-run maximum>
```

### Top-Up Product

Create one top-up product and one one-time Price for each paid plan:

- Product name: `Hetchy Usage Credits - 100 credits`
- Price type: One-time
- Currency: `usd`

| Plan | Amount | Extra credit price |
| --- | ---: | ---: |
| `starter` | `$25.00` | `$0.25/credit` |
| `team` | `$22.00` | `$0.22/credit` |
| `growth` | `$20.00` | `$0.20/credit` |
| `business` | `$16.00` | `$0.16/credit` |

Set the resulting Price IDs in `STRIPE_TOPUP_PRICE_IDS`.

Manual top-ups use Checkout quantity:

```text
the admin UI starts Checkout at 1 top-up unit
Stripe Checkout allows the customer to adjust quantity from 1 to 100
the webhook grants final Checkout line-item quantity * 100 Hetchy credits after checkout.session.completed
```

Auto top-up uses the org's current plan top-up Price ID and charges exactly one quantity at a time. The admin UI collects a monthly dollar cap; Hetchy converts that to the largest whole number of 100-credit top-up units that will not exceed the cap.

## Customer Portal

Configure the Customer Portal separately in test/sandbox and live mode:

1. Open Stripe Dashboard.
2. Go to Customer Portal settings.
3. Enable payment method management.
4. Enable invoice history.
5. Enable subscription cancellation if customers should be able to cancel without support.
6. Disable plan switching for launch unless the app has been updated to map multiple Stripe prices to local plan limits.
7. Save the portal configuration.

The app creates Portal sessions with only `customer` and `return_url`. All Portal behavior comes from Dashboard configuration.

## Webhooks

For stage and prod, create a Dashboard webhook endpoint:

| Environment | Endpoint URL |
| --- | --- |
| Stage | `https://<stage-host>/stripe/webhook` |
| Prod | `https://app.hetchy.ai/stripe/webhook` |

Select exactly these events:

```text
checkout.session.completed
customer.subscription.created
customer.subscription.updated
customer.subscription.deleted
invoice.payment_failed
invoice.paid
```

After saving the endpoint:

1. Open the webhook endpoint in Stripe.
2. Reveal the signing secret.
3. Store it as `STRIPE_WEBHOOK_SECRET` for that app environment.

Do not configure Connect webhooks. Hetchy listens for account-level events from its own Stripe account.

## Dev Setup

Use Stripe test mode or a dedicated Stripe Sandbox for local development.

The older dev sandbox prices created on May 15 and May 18, 2026 used the retired
`$49 / $199 / $499 / $1,499` ladder and 10-credit top-ups. Do not reuse those Price IDs
for the generous launch pricing; create fresh prices that match the table above.

| Plan/resource | Product ID | Price ID | Notes |
| --- | --- | --- | --- |
| Builder | `prod_...` | `price_...` | `$19.00` monthly, 100 credits, max `standard` |
| Studio | `prod_...` | `price_...` | `$79.00` monthly, 500 credits, max `plus` |
| Growth | `prod_...` | `price_...` | `$199.00` monthly, 1,400 credits, max `plus` |
| Business | `prod_...` | `price_...` | `$499.00` monthly, 3,600 credits, max `plus` |
| Builder 100-credit top-up | `prod_...` | `price_...` | `$25.00` one-time top-up |
| Studio 100-credit top-up | `prod_...` | `price_...` | `$22.00` one-time top-up |
| Growth 100-credit top-up | `prod_...` | `price_...` | `$20.00` one-time top-up |
| Business 100-credit top-up | `prod_...` | `price_...` | `$16.00` one-time top-up |

1. In Stripe Dashboard, switch to test mode or select the dev sandbox.
2. Create the subscription product and recurring price.
3. Create the top-up product and one-time prices.
4. Copy the test secret key from Developers > API keys.
5. Log in to the Stripe CLI:

```bash
stripe login
```

6. Forward the required events to your local bot:

```bash
stripe listen \
  --events checkout.session.completed,customer.subscription.created,customer.subscription.updated,customer.subscription.deleted,invoice.payment_failed,invoice.paid \
  --forward-to localhost:8080/stripe/webhook
```

7. Copy the `whsec_...` value printed by `stripe listen`.
8. Set local env:

```bash
STRIPE_SECRET_KEY=sk_test_...
STRIPE_WEBHOOK_SECRET=whsec_...
STRIPE_SUBSCRIPTION_PRICE_IDS=starter=price_...,team=price_...,growth=price_...,business=price_...
STRIPE_TOPUP_PRICE_IDS=starter=price_...,team=price_...,growth=price_...,business=price_...
STRIPE_RETURN_TO=http://dev.hetchy.ai:8080/
```

9. Start the app.
10. In Hetchy, open `/settings/org?tab=billing`.
11. Choose each paid plan, complete Checkout with a Stripe test card, and confirm the org shows the selected paid plan after the webhook arrives.
12. Buy a manual top-up and confirm the top-up balance increases by `quantity * 100`.
13. Enable auto top-up in Hetchy and run a low-balance org through admission to verify one 100-credit unit is charged.

If you use a tunnel instead of localhost, set `STRIPE_RETURN_TO` to the tunnel origin and either keep using `stripe listen --forward-to localhost:8080/stripe/webhook` or create a Dashboard webhook endpoint to the tunnel URL.

## Stage Setup

Use test mode or a dedicated Stripe Sandbox for stage. Prefer a separate sandbox from dev so stage data, customers, webhooks, and Price IDs do not collide with local development.

The stage sandbox resources created with the Stripe CLI on May 15, 2026 used the retired pricing
model. Recreate stage prices before testing the generous launch ladder.

| Resource | ID | Notes |
| --- | --- | --- |
| Product | `prod_...` | `Hetchy Studio` |
| Price | `price_...` | `$79.00` monthly recurring subscription, 500 credits |
| Product | `prod_...` | `Hetchy Usage Credits - 100 credits` |
| Price | `price_...` | `$22.00` one-time top-up for 100 credits |
| Customer Portal config | `bpc_1TXZVzGbUsjCTHqjTnTDSYuK` | Default portal config in the stage sandbox |
| Webhook endpoint | `we_1TXZW3GbUsjCTHqjykZ2sazx` | `https://hetchy-hetchy-staging.demo.okteto.dev/stripe/webhook` |

Store the stage webhook endpoint signing secret from Stripe Dashboard as `STRIPE_WEBHOOK_SECRET`; do not commit it to this file.

1. In Stripe Dashboard, select the stage sandbox or test mode account.
2. Create the subscription product and recurring price.
3. Create the top-up product and one-time prices.
4. Configure Customer Portal in that same mode.
5. Create a webhook endpoint:

```text
https://<stage-host>/stripe/webhook
```

6. Select the six required webhook events.
7. Copy the endpoint signing secret.
8. Set stage secrets:

```bash
STRIPE_SECRET_KEY=sk_test_...
STRIPE_WEBHOOK_SECRET=whsec_...
STRIPE_SUBSCRIPTION_PRICE_ID=price_1TXZVXGbUsjCTHqjSNKTjCOl
STRIPE_TOPUP_PRICE_ID=price_1TXZVeGbUsjCTHqjFnecuTue
STRIPE_RETURN_TO=https://hetchy-hetchy-staging.demo.okteto.dev/
```

9. Deploy stage.
10. Run a full test purchase with Stripe test cards:
    - Subscription checkout.
    - Portal open and return.
    - Manual top-up.
    - Auto top-up.
    - Payment failure path.
11. Confirm stage database rows update in `billing_accounts`, `billing_topup_settings`, `billing_credit_reservations`, and `billing_run_meters`.

Do not use the Stripe CLI webhook signing secret in stage unless stage webhooks are actually forwarded through that local CLI process. Stage should normally use the Dashboard endpoint signing secret.

## Prod Setup

Use live mode only after stage passes.

The current Stripe CLI login is for `Hetchy.ai sandbox (acct_1TXZNrGbUsjCTHqj)` and has no live-mode key available. To create prod objects with the CLI, log in with live-mode access first, then repeat the same product, price, portal, and webhook steps using `--live`.

1. Activate the Stripe account for live payments.
2. In live mode, configure business profile, branding, customer emails, invoice settings, payment methods, and tax settings as required by the business.
3. Create the live subscription products and recurring prices.
4. Create the live top-up product and one-time prices.
5. Configure the live Customer Portal.
6. Create a live webhook endpoint:

```text
https://app.hetchy.ai/stripe/webhook
```

7. Select the six required webhook events.
8. Copy the live endpoint signing secret.
9. Store prod secrets:

```bash
STRIPE_SECRET_KEY=sk_live_...
STRIPE_WEBHOOK_SECRET=whsec_...
STRIPE_SUBSCRIPTION_PRICE_IDS=starter=price_...,team=price_...,growth=price_...,business=price_...
STRIPE_TOPUP_PRICE_IDS=starter=price_...,team=price_...,growth=price_...,business=price_...
STRIPE_RETURN_TO=https://app.hetchy.ai/
```

10. Deploy prod.
11. Smoke test with an internal paid org and a real payment method:
    - Start a subscription through Checkout.
    - Open Portal and return to Hetchy.
    - Buy one top-up unit.
    - Confirm credits are granted only after `checkout.session.completed`.
    - Confirm a run is admitted before Daytona starts.

Do not use test cards in live mode. Test cards only work with test/sandbox keys.

## Restricted Key Option

The simplest launch path is to use Stripe's standard secret key stored in the environment secret manager. If you use a restricted API key, it needs permission for:

- Customers: create and update.
- Checkout Sessions: create.
- Customer Portal Sessions: create.
- Invoices: create, finalize, and pay.
- Invoice Items: create.

Webhooks use `STRIPE_WEBHOOK_SECRET`; that secret is separate from API keys.

## Operational Checks

Before enabling billing in any environment:

- `STRIPE_SECRET_KEY` belongs to the same Stripe mode/account as every configured Price ID.
- `STRIPE_WEBHOOK_SECRET` belongs to the webhook endpoint that points at that app environment.
- The webhook endpoint includes all six required events.
- Every price in `STRIPE_TOPUP_PRICE_IDS` is a one-time price for exactly 100 Hetchy credits.
- Every price in `STRIPE_SUBSCRIPTION_PRICE_IDS` is a recurring price for the matching paid plan.
- Customer Portal is configured in the same Stripe mode/account.
- `STRIPE_RETURN_TO` is the public app root for that environment.
- The app has run the billing migration.

## Failure Modes

- If Checkout succeeds but credits do not update, check the webhook endpoint delivery log first.
- If webhook signature verification fails, `STRIPE_WEBHOOK_SECRET` is from the wrong endpoint or from the CLI while the event came from a Dashboard endpoint.
- If Checkout creation fails with "No such price", the secret key and selected Price ID are from different Stripe modes/accounts.
- If Portal fails, the org probably has no Stripe customer yet or Customer Portal is not configured for that mode.
- If auto top-up fails, verify the customer has a saved payment method and that the top-up Price ID is a one-time price.

## Stripe References

- Products and Prices: https://docs.stripe.com/products-prices/how-products-and-prices-work
- Manage Prices: https://docs.stripe.com/products-prices/manage-prices
- API Keys and Webhook Signing Secrets: https://docs.stripe.com/keys
- Webhooks: https://docs.stripe.com/webhooks
- Customer Portal: https://docs.stripe.com/customer-management
- Stripe CLI: https://docs.stripe.com/stripe-cli/use-cli
