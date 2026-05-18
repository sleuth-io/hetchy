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
- Comped orgs via `billing_accounts.billing_exempt`.
- Local credit balance and real-time admission before Daytona starts.
- Repo sandbox flavor defaults in `repo_billing_settings`.
- Auto top-up thresholds, target balance, and monthly max units.
- Run metering and credit capture/release.

Do not configure Stripe Billing Credits or Stripe Meters for this launch. The app uses Hetchy-local credits and Stripe only as the payment and subscription source of truth.

## Implementation Contract

The app expects these environment variables:

| Variable | Value |
| --- | --- |
| `STRIPE_SECRET_KEY` | Stripe server-side secret key for the environment. Use `sk_test_...` or a restricted test key for dev/stage, and `sk_live_...` or a restricted live key for prod. |
| `STRIPE_WEBHOOK_SECRET` | Webhook signing secret for that environment's endpoint. Starts with `whsec_...`. |
| `STRIPE_SUBSCRIPTION_PRICE_IDS` | Comma-, semicolon-, or newline-separated map of paid plan code to recurring Price ID, for example `starter=price_...,team=price_...`. |
| `STRIPE_SUBSCRIPTION_PRICE_ID` | Legacy fallback recurring Stripe Price ID for the default Team plan. Use only when a single subscription price is configured. |
| `STRIPE_TOPUP_PRICE_IDS` | Comma-, semicolon-, or newline-separated map of paid plan code to one-time 10-credit top-up Price ID. |
| `STRIPE_TOPUP_PRICE_ID` | Legacy fallback one-time top-up Price ID. Use only when a single top-up price is configured. |
| `STRIPE_RETURN_TO` | Public app root used for Stripe Checkout and Customer Portal return URLs. |

Also confirm `STRIPE_RETURN_TO` points at the public app root for the environment, because Checkout and Portal return URLs are built from `Config.StripeReturnBaseURL()`:

| Environment | Example |
| --- | --- |
| Dev | `http://dev.hetchy.ai:8080/` or the exact localhost/tunnel origin you use in the browser |
| Stage | `https://<stage-host>/` |
| Prod | `https://app.hetchy.ai/` |

The app exposes these Stripe-facing routes:

| Route | Purpose |
| --- | --- |
| `POST /billing/checkout` | Creates a Stripe Checkout Session in subscription mode. Admin-only. |
| `POST /billing/topup` | Creates a Stripe Checkout Session in payment mode. Admin-only. |
| `POST /billing/portal` | Creates a Stripe Customer Portal session. Admin-only. |
| `POST /stripe/webhook` | Public Stripe webhook endpoint with signature verification. |

The webhook handler listens for these event types:

```text
checkout.session.completed
customer.subscription.created
customer.subscription.updated
customer.subscription.deleted
invoice.payment_failed
```

Use snapshot events if Stripe asks you to choose between snapshot and thin events. The handler decodes the object included in the event payload.

## Products And Prices

Create the products and prices separately for each Stripe mode or Stripe account you use. Test/sandbox Price IDs cannot be used with live keys, and live Price IDs cannot be used with test keys.

### Subscription Products

Create one monthly recurring price for each paid public plan:

| Plan | Product name | Amount | Included credits | Max flavor | Per-run reservation |
| --- | --- | --- | ---: | --- | ---: |
| `starter` | `Hetchy Starter` | `$49.00` | 50 | `standard` | 1 |
| `team` | `Hetchy Team` | `$199.00` | 300 | `pro` | 3 |
| `growth` | `Hetchy Growth` | `$499.00` | 1,000 | `max` | 6 |
| `business` | `Hetchy Business` | `$1,499.00` | 4,000 | `max` | 6 |

Set the resulting Price IDs in `STRIPE_SUBSCRIPTION_PRICE_IDS`.

Do not add these prices to Customer Portal plan switching until the app updates subscription metadata during portal-driven plan changes. Checkout writes plan metadata onto the Stripe Subscription, and the webhook mirrors those fields locally:

```text
plan_code=<starter|team|growth|business>
included_credits=<plan included credits>
max_flavor=<plan max flavor>
per_run_max_credits=<plan per-run reservation>
```

### Top-Up Product

Create one top-up product and one one-time Price for each paid plan:

- Product name: `Hetchy Usage Credits - 10 credits`
- Price type: One-time
- Currency: `usd`

| Plan | Amount | Extra credit price |
| --- | ---: | ---: |
| `starter` | `$12.50` | `$1.25/credit` |
| `team` | `$9.00` | `$0.90/credit` |
| `growth` | `$6.50` | `$0.65/credit` |
| `business` | `$4.50` | `$0.45/credit` |

Set the resulting Price IDs in `STRIPE_TOPUP_PRICE_IDS`.

Manual top-ups use Checkout quantity:

```text
quantity N => charges N top-up units at the org's current plan price and grants N * 10 Hetchy credits after checkout.session.completed
```

Auto top-up uses the org's current plan top-up Price ID and charges exactly one quantity at a time.

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
```

After saving the endpoint:

1. Open the webhook endpoint in Stripe.
2. Reveal the signing secret.
3. Store it as `STRIPE_WEBHOOK_SECRET` for that app environment.

Do not configure Connect webhooks. Hetchy listens for account-level events from its own Stripe account.

## Dev Setup

Use Stripe test mode or a dedicated Stripe Sandbox for local development.

Current dev sandbox resources created with the Stripe CLI on May 15 and May 18, 2026:

| Plan/resource | Product ID | Price ID | Notes |
| --- | --- | --- | --- |
| Starter | `prod_UWcwzkjaHwsh1N` | `price_1TXZgoGbUsjCTHqj7uIOpQWI` | `$49.00` monthly, 50 credits, max `standard` |
| Team | `prod_UWcwNawJSICYqO` | `price_1TXZgoGbUsjCTHqjsKErCD1E` | `$199.00` monthly, 300 credits, max `pro` |
| Growth | `prod_UWcw1Pw1B7R2sp` | `price_1TXZgoGbUsjCTHqjr0AVzxsg` | `$499.00` monthly, 1,000 credits, max `max` |
| Business | `prod_UWcwILdgEPHWOM` | `price_1TXZgoGbUsjCTHqj3G6pt2w8` | `$1,499.00` monthly, 4,000 credits, max `max` |
| Starter 10-credit top-up | `prod_UWcxj9L8dLX5G9` | `price_1TYYNpGbUsjCTHqjXgbdT1Zg` | `$12.50` one-time top-up |
| Team 10-credit top-up | `prod_UWcxj9L8dLX5G9` | `price_1TXZhKGbUsjCTHqjDnhVPea5` | `$9.00` one-time top-up |
| Growth 10-credit top-up | `prod_UWcxj9L8dLX5G9` | `price_1TYYNpGbUsjCTHqjYiOWMJ6a` | `$6.50` one-time top-up |
| Business 10-credit top-up | `prod_UWcxj9L8dLX5G9` | `price_1TYYNpGbUsjCTHqjW2f0gT6c` | `$4.50` one-time top-up |

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
  --events checkout.session.completed,customer.subscription.created,customer.subscription.updated,customer.subscription.deleted,invoice.payment_failed \
  --forward-to localhost:8080/stripe/webhook
```

7. Copy the `whsec_...` value printed by `stripe listen`.
8. Set local env:

```bash
STRIPE_SECRET_KEY=sk_test_...
STRIPE_WEBHOOK_SECRET=whsec_...
STRIPE_SUBSCRIPTION_PRICE_IDS=starter=price_1TXZgoGbUsjCTHqj7uIOpQWI,team=price_1TXZgoGbUsjCTHqjsKErCD1E,growth=price_1TXZgoGbUsjCTHqjr0AVzxsg,business=price_1TXZgoGbUsjCTHqj3G6pt2w8
STRIPE_TOPUP_PRICE_IDS=starter=price_1TYYNpGbUsjCTHqjXgbdT1Zg,team=price_1TXZhKGbUsjCTHqjDnhVPea5,growth=price_1TYYNpGbUsjCTHqjYiOWMJ6a,business=price_1TYYNpGbUsjCTHqjW2f0gT6c
STRIPE_RETURN_TO=http://dev.hetchy.ai:8080/
```

9. Start the app.
10. In Hetchy, open `/settings/org?tab=billing`.
11. Choose each paid plan, complete Checkout with a Stripe test card, and confirm the org shows the selected paid plan after the webhook arrives.
12. Buy a manual top-up and confirm the top-up balance increases by `quantity * 10`.
13. Enable auto top-up in Hetchy and run a low-balance org through admission to verify one 10-credit unit is charged.

If you use a tunnel instead of localhost, set `STRIPE_RETURN_TO` to the tunnel origin and either keep using `stripe listen --forward-to localhost:8080/stripe/webhook` or create a Dashboard webhook endpoint to the tunnel URL.

## Stage Setup

Use test mode or a dedicated Stripe Sandbox for stage. Prefer a separate sandbox from dev so stage data, customers, webhooks, and Price IDs do not collide with local development.

Current stage sandbox resources created with the Stripe CLI on May 15, 2026:

| Resource | ID | Notes |
| --- | --- | --- |
| Product | `prod_UWckrzWkGC3Cav` | `Hetchy Team` |
| Price | `price_1TXZVXGbUsjCTHqjSNKTjCOl` | `$199.00` monthly recurring subscription |
| Product | `prod_UWclbhPcwf0Bjg` | `Hetchy Usage Credits - 10 credits` |
| Price | `price_1TXZVeGbUsjCTHqjFnecuTue` | `$9.00` one-time top-up for 10 credits |
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

6. Select the five required webhook events.
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

7. Select the five required webhook events.
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
- The webhook endpoint includes all five required events.
- Every price in `STRIPE_TOPUP_PRICE_IDS` is a one-time price for exactly 10 Hetchy credits.
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
