#!/usr/bin/env bash
# Create or update the Stripe objects Hetchy needs for billing.
#
# This script assumes `stripe login` has already selected the intended Stripe
# account. Stripe CLI resource calls are test-mode by default; pass --live when
# provisioning live-mode objects.
set -euo pipefail

ENV_NAME=""
NAMESPACE=""
CURRENCY="usd"
RETURN_TO=""
WEBHOOK_URL=""
STRIPE_ACCOUNT=""
LIVE=0
YES=0
SKIP_PORTAL=0
SKIP_WEBHOOK=0

PLANS=(
  "starter|Builder|1900|100|standard|4|2500"
  "team|Studio|7900|500|plus|12|2200"
  "growth|Growth|19900|1400|plus|24|2000"
  "business|Business|49900|3600|plus|48|1600"
)

WEBHOOK_EVENTS=(
  "checkout.session.completed"
  "customer.subscription.created"
  "customer.subscription.updated"
  "customer.subscription.deleted"
  "invoice.payment_failed"
  "invoice.paid"
)

usage() {
  cat <<'EOF'
Usage:
  scripts/sync-stripe-billing.sh --env <dev|stage|prod> [options]

Options:
  --env NAME              Required. Environment label stored in Stripe metadata.
  --namespace NAME        Lookup-key namespace. Defaults to --env.
  --live                  Create/update live-mode Stripe objects. Default is test mode.
  --yes                   Skip the live-mode confirmation prompt.
  --return-to URL         STRIPE_RETURN_TO for this app environment.
  --webhook-url URL       Create/update a Dashboard webhook endpoint for this URL.
  --skip-portal           Do not create/update the default Customer Portal config.
  --skip-webhook          Do not create/update a Dashboard webhook endpoint.
  --stripe-account ACCT   Pass --stripe-account for a connected account.
  --currency CURRENCY     Stripe currency. Defaults to usd.
  -h, --help              Show this help.

Examples:
  scripts/sync-stripe-billing.sh \
    --env dev \
    --live \
    --return-to https://dev.hetchy.ai/ \
    --webhook-url https://dev.hetchy.ai/stripe/webhook

  scripts/sync-stripe-billing.sh \
    --env prod \
    --live \
    --return-to https://app.hetchy.ai/ \
    --webhook-url https://app.hetchy.ai/stripe/webhook

The script prints STRIPE_SUBSCRIPTION_PRICE_IDS and STRIPE_TOPUP_PRICE_IDS at
the end. Store those values in the matching Doppler config.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env)
      ENV_NAME="${2:-}"
      shift 2
      ;;
    --env=*)
      ENV_NAME="${1#--env=}"
      shift
      ;;
    --namespace)
      NAMESPACE="${2:-}"
      shift 2
      ;;
    --namespace=*)
      NAMESPACE="${1#--namespace=}"
      shift
      ;;
    --live)
      LIVE=1
      shift
      ;;
    --yes|-y)
      YES=1
      shift
      ;;
    --return-to)
      RETURN_TO="${2:-}"
      shift 2
      ;;
    --return-to=*)
      RETURN_TO="${1#--return-to=}"
      shift
      ;;
    --webhook-url)
      WEBHOOK_URL="${2:-}"
      shift 2
      ;;
    --webhook-url=*)
      WEBHOOK_URL="${1#--webhook-url=}"
      shift
      ;;
    --skip-portal)
      SKIP_PORTAL=1
      shift
      ;;
    --skip-webhook)
      SKIP_WEBHOOK=1
      shift
      ;;
    --stripe-account)
      STRIPE_ACCOUNT="${2:-}"
      shift 2
      ;;
    --stripe-account=*)
      STRIPE_ACCOUNT="${1#--stripe-account=}"
      shift
      ;;
    --currency)
      CURRENCY="${2:-}"
      shift 2
      ;;
    --currency=*)
      CURRENCY="${1#--currency=}"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "sync-stripe-billing: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$ENV_NAME" ]]; then
  echo "sync-stripe-billing: --env is required" >&2
  usage >&2
  exit 2
fi

if [[ -z "$NAMESPACE" ]]; then
  NAMESPACE="$ENV_NAME"
fi

if [[ ! "$ENV_NAME" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "sync-stripe-billing: --env may only contain letters, numbers, underscores, and dashes" >&2
  exit 2
fi

if [[ ! "$NAMESPACE" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "sync-stripe-billing: --namespace may only contain letters, numbers, underscores, and dashes" >&2
  exit 2
fi

if [[ ! "$CURRENCY" =~ ^[a-z]{3}$ ]]; then
  echo "sync-stripe-billing: --currency must be a lowercase three-letter Stripe currency code" >&2
  exit 2
fi

for bin in stripe jq; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "sync-stripe-billing: missing required command: $bin" >&2
    exit 1
  fi
done

STRIPE_MODE_ARGS=()
if [[ "$LIVE" -eq 1 ]]; then
  STRIPE_MODE_ARGS+=(--live)
fi
if [[ -n "$STRIPE_ACCOUNT" ]]; then
  STRIPE_MODE_ARGS+=(--stripe-account "$STRIPE_ACCOUNT")
fi

log() {
  printf '%s\n' "$*" >&2
}

stripe_op() {
  stripe --color off --log-level error "$@" "${STRIPE_MODE_ARGS[@]}" --confirm
}

display_name() {
  local base="$1"
  if [[ "$ENV_NAME" == "prod" || "$ENV_NAME" == "production" ]]; then
    printf '%s' "$base"
  else
    printf '%s (%s)' "$base" "$ENV_NAME"
  fi
}

lookup_key() {
  local kind="$1"
  local plan_code="$2"
  printf 'hetchy_%s_%s_%s' "$NAMESPACE" "$kind" "$plan_code"
}

product_query() {
  local kind="$1"
  local plan_code="${2:-}"
  local query="metadata['hetchy_managed']:'true' AND metadata['hetchy_env']:'${ENV_NAME}' AND metadata['hetchy_kind']:'${kind}'"
  if [[ -n "$plan_code" ]]; then
    query="${query} AND metadata['hetchy_plan_code']:'${plan_code}'"
  fi
  printf '%s' "$query"
}

ensure_product() {
  local kind="$1"
  local plan_code="$2"
  local name="$3"
  local description="$4"
  local unit_label="$5"
  local included_credits="${6:-}"
  local max_flavor="${7:-}"
  local per_run_max="${8:-}"
  local topup_cents="${9:-}"

  local metadata=(
    -d "metadata[app]=hetchy"
    -d "metadata[hetchy_managed]=true"
    -d "metadata[hetchy_env]=$ENV_NAME"
    -d "metadata[hetchy_kind]=$kind"
  )
  if [[ -n "$plan_code" ]]; then
    metadata+=(-d "metadata[hetchy_plan_code]=$plan_code")
  fi
  if [[ -n "$included_credits" ]]; then
    metadata+=(-d "metadata[included_credits]=$included_credits")
  fi
  if [[ -n "$max_flavor" ]]; then
    metadata+=(-d "metadata[max_flavor]=$max_flavor")
  fi
  if [[ -n "$per_run_max" ]]; then
    metadata+=(-d "metadata[per_run_max_credits]=$per_run_max")
  fi
  if [[ "$kind" == "topup" ]]; then
    metadata+=(-d "metadata[topup_unit_credits]=100")
  fi
  if [[ -n "$topup_cents" ]]; then
    metadata+=(-d "metadata[topup_unit_amount_cents]=$topup_cents")
  fi

  local query product_json product_id
  query="$(product_query "$kind" "$plan_code")"
  product_json="$(stripe_op products search --limit 1 --query "$query")"
  product_id="$(jq -r '.data[0].id // empty' <<<"$product_json")"

  if [[ -n "$product_id" ]]; then
    log "Updating product $product_id: $name"
    stripe_op products update "$product_id" \
      --active true \
      --name "$name" \
      --description "$description" \
      --unit-label "$unit_label" \
      "${metadata[@]}" >/dev/null
    printf '%s\n' "$product_id"
    return 0
  fi

  log "Creating product: $name"
  product_json="$(stripe_op products create \
    --active true \
    --name "$name" \
    --description "$description" \
    --type service \
    --unit-label "$unit_label" \
    --idempotency "hetchy-${NAMESPACE}-product-${kind}-${plan_code:-all}" \
    "${metadata[@]}")"
  jq -r '.id' <<<"$product_json"
}

price_metadata_args() {
  local kind="$1"
  local plan_code="$2"
  local label="$3"
  local included_credits="$4"
  local max_flavor="$5"
  local per_run_max="$6"
  local topup_cents="$7"

  printf '%s\n' \
    "-d" "metadata[app]=hetchy" \
    "-d" "metadata[hetchy_managed]=true" \
    "-d" "metadata[hetchy_env]=$ENV_NAME" \
    "-d" "metadata[hetchy_kind]=$kind" \
    "-d" "metadata[hetchy_plan_code]=$plan_code" \
    "-d" "metadata[plan_label]=$label"

  if [[ "$kind" == "subscription" ]]; then
    printf '%s\n' \
      "-d" "metadata[included_credits]=$included_credits" \
      "-d" "metadata[max_flavor]=$max_flavor" \
      "-d" "metadata[per_run_max_credits]=$per_run_max"
  else
    printf '%s\n' \
      "-d" "metadata[topup_unit_credits]=100" \
      "-d" "metadata[topup_unit_amount_cents]=$topup_cents"
  fi
}

ensure_price() {
  local kind="$1"
  local plan_code="$2"
  local label="$3"
  local product_id="$4"
  local amount_cents="$5"
  local recurring_interval="$6"
  local included_credits="$7"
  local max_flavor="$8"
  local per_run_max="$9"
  local topup_cents="${10}"

  local key nickname
  key="$(lookup_key "$kind" "$plan_code")"
  if [[ "$kind" == "subscription" ]]; then
    nickname="$(display_name "Hetchy ${label} monthly")"
  else
    nickname="$(display_name "Hetchy ${label} 100-credit top-up")"
  fi

  local metadata=()
  while IFS= read -r arg; do
    metadata+=("$arg")
  done < <(price_metadata_args "$kind" "$plan_code" "$label" "$included_credits" "$max_flavor" "$per_run_max" "$topup_cents")

  local price_json existing_id match_state
  price_json="$(stripe_op prices list --lookup-keys "$key" --limit 1)"
  existing_id="$(jq -r '.data[0].id // empty' <<<"$price_json")"
  match_state="$(jq -r \
    --argjson amount "$amount_cents" \
    --arg currency "$CURRENCY" \
    --arg product "$product_id" \
    --arg interval "$recurring_interval" '
      .data[0] as $p |
      if $p == null then
        "missing"
      elif (
        $p.unit_amount == $amount and
        $p.currency == $currency and
        $p.product == $product and
        (if $interval == "none" then ($p.recurring == null) else (($p.recurring.interval // "") == $interval) end)
      ) then
        "match"
      else
        "replace"
      end
    ' <<<"$price_json")"

  if [[ "$match_state" == "match" ]]; then
    log "Updating price metadata $existing_id: $nickname"
    stripe_op prices update "$existing_id" \
      --active true \
      --nickname "$nickname" \
      "${metadata[@]}" >/dev/null
    printf '%s\n' "$existing_id"
    return 0
  fi

  local create_args=(
    prices create
    --currency "$CURRENCY"
    --unit-amount "$amount_cents"
    --product "$product_id"
    --lookup-key "$key"
    --nickname "$nickname"
  )
  if [[ "$recurring_interval" != "none" ]]; then
    create_args+=(--recurring.interval "$recurring_interval")
  fi
  if [[ -n "$existing_id" ]]; then
    log "Replacing immutable price $existing_id: $nickname"
    create_args+=(--transfer-lookup-key true)
  else
    log "Creating price: $nickname"
  fi

  local created_json created_id
  created_json="$(stripe_op "${create_args[@]}" \
    --idempotency "hetchy-${NAMESPACE}-price-${kind}-${plan_code}-${amount_cents}-${product_id}" \
    "${metadata[@]}")"
  created_id="$(jq -r '.id' <<<"$created_json")"

  if [[ -n "$existing_id" ]]; then
    log "Deactivating replaced price $existing_id"
    stripe_op prices update "$existing_id" --active false >/dev/null
  fi

  printf '%s\n' "$created_id"
}

ensure_webhook() {
  if [[ "$SKIP_WEBHOOK" -eq 1 || -z "$WEBHOOK_URL" ]]; then
    return 0
  fi

  local endpoints endpoint_id description
  description="Hetchy ${ENV_NAME} billing webhook"
  endpoints="$(stripe_op webhook_endpoints list --limit 100)"
  endpoint_id="$(jq -r --arg url "$WEBHOOK_URL" '.data[] | select(.url == $url) | .id' <<<"$endpoints" | head -n 1)"

  local event_args=()
  local event
  for event in "${WEBHOOK_EVENTS[@]}"; do
    event_args+=(--enabled-events "$event")
  done

  if [[ -n "$endpoint_id" ]]; then
    log "Updating webhook endpoint $endpoint_id: $WEBHOOK_URL"
    stripe_op webhook_endpoints update "$endpoint_id" \
      --url "$WEBHOOK_URL" \
      --description "$description" \
      --disabled false \
      -d "metadata[app]=hetchy" \
      "${event_args[@]}" >/dev/null
    WEBHOOK_ENDPOINT_ID="$endpoint_id"
    WEBHOOK_SECRET=""
    return 0
  fi

  local created
  log "Creating webhook endpoint: $WEBHOOK_URL"
  created="$(stripe_op webhook_endpoints create \
    --url "$WEBHOOK_URL" \
    --description "$description" \
    -d "metadata[app]=hetchy" \
    "${event_args[@]}")"
  WEBHOOK_ENDPOINT_ID="$(jq -r '.id' <<<"$created")"
  WEBHOOK_SECRET="$(jq -r '.secret // empty' <<<"$created")"
}

ensure_portal() {
  if [[ "$SKIP_PORTAL" -eq 1 ]]; then
    return 0
  fi
  if [[ -z "$RETURN_TO" ]]; then
    log "Skipping Customer Portal config because --return-to was not provided."
    return 0
  fi

  local configs config_id name
  name="Hetchy Billing Portal"
  configs="$(stripe_op billing_portal configurations list --is-default true --limit 1)"
  config_id="$(jq -r '.data[0].id // empty' <<<"$configs")"

  local portal_args=(
    --name "$name"
    --default-return-url "$RETURN_TO"
    --features.payment-method-update.enabled true
    --features.invoice-history.enabled true
    --features.subscription-cancel.enabled true
    --features.subscription-cancel.mode at_period_end
    --features.subscription-cancel.proration-behavior none
    --features.subscription-cancel.cancellation-reason.enabled false
    --features.subscription-update.enabled false
    --features.customer-update.enabled false
    --login-page.enabled false
    -d "metadata[app]=hetchy"
  )

  if [[ -n "$config_id" ]]; then
    log "Updating default Customer Portal config $config_id"
    stripe_op billing_portal configurations update "$config_id" \
      --active true \
      "${portal_args[@]}" >/dev/null
    PORTAL_CONFIGURATION_ID="$config_id"
    return 0
  fi

  local created
  log "Creating Customer Portal config"
  created="$(stripe_op billing_portal configurations create "${portal_args[@]}")"
  PORTAL_CONFIGURATION_ID="$(jq -r '.id' <<<"$created")"
}

mode_label="test"
if [[ "$LIVE" -eq 1 ]]; then
  mode_label="live"
fi

log "Stripe CLI identity:"
stripe --color off --log-level error whoami >&2 || {
  echo "sync-stripe-billing: stripe whoami failed. Run 'stripe login' first." >&2
  exit 1
}
log ""
log "mode:       $mode_label"
log "env:        $ENV_NAME"
log "namespace:  $NAMESPACE"
log "currency:   $CURRENCY"
if [[ -n "$STRIPE_ACCOUNT" ]]; then
  log "account:    $STRIPE_ACCOUNT"
fi
log ""

if [[ "$LIVE" -eq 1 && "$YES" -ne 1 ]]; then
  printf 'About to create/update LIVE Stripe billing objects for env "%s". Type LIVE %s to continue: ' "$ENV_NAME" "$ENV_NAME" >&2
  read -r confirmation
  if [[ "$confirmation" != "LIVE $ENV_NAME" ]]; then
    echo "sync-stripe-billing: aborted" >&2
    exit 1
  fi
fi

subscription_price_ids=""
topup_price_ids=""

topup_product_name="$(display_name "Hetchy Usage Credits - 100 credits")"
topup_product_desc="100 Hetchy usage credits. The app grants quantity * 100 credits after checkout.session.completed."
topup_product_id="$(ensure_product "topup" "" "$topup_product_name" "$topup_product_desc" "100 credits" "" "" "" "")"

for plan in "${PLANS[@]}"; do
  IFS='|' read -r code label monthly_cents included_credits max_flavor per_run_max topup_cents <<<"$plan"

  product_name="$(display_name "Hetchy ${label}")"
  product_desc="Hetchy ${label} monthly subscription. Includes ${included_credits} credits, max sandbox ${max_flavor}, per-run max ${per_run_max} credits."
  subscription_product_id="$(ensure_product \
    "subscription_plan" \
    "$code" \
    "$product_name" \
    "$product_desc" \
    "subscription" \
    "$included_credits" \
    "$max_flavor" \
    "$per_run_max" \
    "")"

  subscription_price_id="$(ensure_price \
    "subscription" \
    "$code" \
    "$label" \
    "$subscription_product_id" \
    "$monthly_cents" \
    "month" \
    "$included_credits" \
    "$max_flavor" \
    "$per_run_max" \
    "$topup_cents")"
  topup_price_id="$(ensure_price \
    "topup" \
    "$code" \
    "$label" \
    "$topup_product_id" \
    "$topup_cents" \
    "none" \
    "$included_credits" \
    "$max_flavor" \
    "$per_run_max" \
    "$topup_cents")"

  subscription_price_ids="${subscription_price_ids}${subscription_price_ids:+,}${code}=${subscription_price_id}"
  topup_price_ids="${topup_price_ids}${topup_price_ids:+,}${code}=${topup_price_id}"
done

WEBHOOK_ENDPOINT_ID=""
WEBHOOK_SECRET=""
PORTAL_CONFIGURATION_ID=""
ensure_webhook
ensure_portal

cat <<EOF

Stripe billing sync complete.

Set these in the matching app environment:

STRIPE_SUBSCRIPTION_PRICE_IDS=${subscription_price_ids}
STRIPE_TOPUP_PRICE_IDS=${topup_price_ids}
EOF

if [[ -n "$RETURN_TO" ]]; then
  printf 'STRIPE_RETURN_TO=%s\n' "$RETURN_TO"
fi

if [[ -n "$WEBHOOK_ENDPOINT_ID" ]]; then
  printf '\nWebhook endpoint: %s\n' "$WEBHOOK_ENDPOINT_ID"
  if [[ -n "$WEBHOOK_SECRET" ]]; then
    printf 'STRIPE_WEBHOOK_SECRET=%s\n' "$WEBHOOK_SECRET"
  else
    printf 'STRIPE_WEBHOOK_SECRET=<reveal the signing secret for %s in the Stripe Dashboard>\n' "$WEBHOOK_ENDPOINT_ID"
  fi
fi

if [[ -n "$PORTAL_CONFIGURATION_ID" ]]; then
  printf '\nCustomer Portal configuration: %s\n' "$PORTAL_CONFIGURATION_ID"
fi

cat <<'EOF'

Notes:
- Keep STRIPE_SECRET_KEY in Doppler separate from this script; Stripe does not expose it here.
- Existing subscriptions keep their old Price IDs even when this script deactivates a replaced price.
- The app reads the Price ID maps above; it does not look up Stripe lookup keys at runtime.
EOF
