package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v85"

	"github.com/hetchyhq/hetchy/internal/billing"
)

func TestSettingsURLUsesStripeReturnTo(t *testing.T) {
	b := &Bot{cfg: Config{
		LogoutReturnTo: "https://app.example.test/",
		StripeReturnTo: "https://billing.example.test/",
	}}

	got := b.settingsURL("billing", "saved=1")
	want := "https://billing.example.test/settings/org?tab=billing&saved=1"
	if got != want {
		t.Fatalf("settingsURL = %q, want %q", got, want)
	}
}

func TestStripeTopupPlanPriceID(t *testing.T) {
	priceIDs := map[string]string{
		"starter": "price_starter_topup",
		"team":    "price_team_topup",
		"growth":  "price_growth_topup",
	}

	for _, tc := range []struct {
		name     string
		planCode string
		legacy   string
		want     string
	}{
		{name: "plan specific", planCode: "growth", want: "price_growth_topup"},
		{name: "case insensitive", planCode: " STARTER ", want: "price_starter_topup"},
		{name: "free falls back to default plan", planCode: "free", want: "price_team_topup"},
		{name: "missing paid plan uses legacy", planCode: "business", legacy: "price_legacy_topup", want: "price_legacy_topup"},
		{name: "missing paid plan without legacy", planCode: "business", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stripeTopupPlanPriceID(priceIDs, tc.legacy, tc.planCode)
			if got != tc.want {
				t.Fatalf("stripeTopupPlanPriceID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripeTopupLineItemAllowsAdjustableQuantity(t *testing.T) {
	line := stripeTopupLineItem("price_topup", 3)

	if line.Price == nil || *line.Price != "price_topup" {
		t.Fatalf("Price = %v", line.Price)
	}
	if line.Quantity == nil || *line.Quantity != 3 {
		t.Fatalf("Quantity = %v", line.Quantity)
	}
	if line.AdjustableQuantity == nil {
		t.Fatal("AdjustableQuantity is nil")
	}
	if line.AdjustableQuantity.Enabled == nil || !*line.AdjustableQuantity.Enabled {
		t.Fatalf("AdjustableQuantity.Enabled = %v", line.AdjustableQuantity.Enabled)
	}
	if line.AdjustableQuantity.Minimum == nil || *line.AdjustableQuantity.Minimum != 1 {
		t.Fatalf("AdjustableQuantity.Minimum = %v", line.AdjustableQuantity.Minimum)
	}
	if line.AdjustableQuantity.Maximum == nil || *line.AdjustableQuantity.Maximum != 100 {
		t.Fatalf("AdjustableQuantity.Maximum = %v", line.AdjustableQuantity.Maximum)
	}
}

func TestTopupCreditsFromCheckoutMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]string
		want     int
	}{
		{name: "explicit credits", metadata: map[string]string{"credits": "50", "quantity": "2"}, want: 50},
		{name: "quantity fallback", metadata: map[string]string{"quantity": "4"}, want: 40},
		{name: "default quantity", metadata: nil, want: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := topupCreditsFromCheckoutMetadata(tc.metadata); got != tc.want {
				t.Fatalf("topupCreditsFromCheckoutMetadata = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPaidAccountMirrorUsesLocalPlanLimits(t *testing.T) {
	mirror := paidAccountMirror("org_1", "cus_1", "sub_1", "active", time.Time{}, time.Time{}, map[string]string{
		"plan_code":           billing.PlanTeam,
		"included_credits":    "300",
		"max_flavor":          billing.FlavorPro,
		"per_run_max_credits": "3",
	})

	if mirror.MaxFlavor != billing.FlavorMax {
		t.Fatalf("MaxFlavor = %q, want %q", mirror.MaxFlavor, billing.FlavorMax)
	}
	if mirror.PerRunMaxCredits != 6 {
		t.Fatalf("PerRunMaxCredits = %d, want 6", mirror.PerRunMaxCredits)
	}
}

func TestBillingPlanSwitchDirection(t *testing.T) {
	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)
	starter, _ := billing.PaidPlanByCode(billing.PlanStarter)

	if !isBillingPlanUpgrade(team, growth) {
		t.Fatal("team -> growth should be an upgrade")
	}
	if isBillingPlanUpgrade(growth, starter) {
		t.Fatal("growth -> starter should be a downgrade")
	}
	if isBillingPlanUpgrade(team, team) {
		t.Fatal("team -> team should not be an upgrade")
	}
}

func TestStripeSubscriptionPlanItemFindsConfiguredPlanPrice(t *testing.T) {
	sub := &stripe.Subscription{
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{
				{ID: "si_metered", Price: &stripe.Price{ID: "price_metered"}},
				{ID: "si_plan", Price: &stripe.Price{ID: "price_growth"}},
			},
		},
	}

	item, priceID, err := stripeSubscriptionPlanItem(sub, map[string]string{
		billing.PlanTeam:   "price_team",
		billing.PlanGrowth: "price_growth",
	})
	if err != nil {
		t.Fatalf("stripeSubscriptionPlanItem returned error: %v", err)
	}
	if item.ID != "si_plan" || priceID != "price_growth" {
		t.Fatalf("item=%q priceID=%q, want si_plan price_growth", item.ID, priceID)
	}
}

func TestStripeSubscriptionPlanItemFallsBackToSingleItem(t *testing.T) {
	sub := &stripe.Subscription{
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{
				{ID: "si_only", Price: &stripe.Price{ID: "price_unknown"}},
			},
		},
	}

	item, priceID, err := stripeSubscriptionPlanItem(sub, map[string]string{billing.PlanTeam: "price_team"})
	if err != nil {
		t.Fatalf("stripeSubscriptionPlanItem returned error: %v", err)
	}
	if item.ID != "si_only" || priceID != "price_unknown" {
		t.Fatalf("item=%q priceID=%q, want si_only price_unknown", item.ID, priceID)
	}
}

func TestStripeUpgradeSubscriptionParamsInvoicesImmediately(t *testing.T) {
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)
	params := stripeUpgradeSubscriptionParams("org_1", "sub_1", "si_1", growth, "price_growth")

	if got := *params.Items[0].ID; got != "si_1" {
		t.Fatalf("item ID = %q, want si_1", got)
	}
	if got := *params.Items[0].Price; got != "price_growth" {
		t.Fatalf("item price = %q, want price_growth", got)
	}
	if got := *params.ProrationBehavior; got != "always_invoice" {
		t.Fatalf("ProrationBehavior = %q, want always_invoice", got)
	}
	if got := *params.PaymentBehavior; got != "pending_if_incomplete" {
		t.Fatalf("PaymentBehavior = %q, want pending_if_incomplete", got)
	}
	if got := params.Metadata["plan_code"]; got != billing.PlanGrowth {
		t.Fatalf("metadata plan_code = %q, want growth", got)
	}
}

func TestStripeDowngradeScheduleCreateParamsOnlySetsSubscription(t *testing.T) {
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)
	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	params := stripeDowngradeScheduleCreateParams("sub_1", growth, "price_growth", team, "price_team", 1, 2)

	if params.FromSubscription == nil || *params.FromSubscription != "sub_1" {
		t.Fatalf("FromSubscription = %v, want sub_1", params.FromSubscription)
	}
	if key := *params.Params.IdempotencyKey; !strings.Contains(key, "price_growth") || !strings.Contains(key, "price_team") {
		t.Fatalf("idempotency key = %q, want current and target prices", key)
	}
	if params.Metadata != nil {
		t.Fatalf("Metadata = %v, want nil because Stripe rejects metadata with from_subscription", params.Metadata)
	}
	if params.Phases != nil {
		t.Fatalf("Phases = %v, want nil with from_subscription create", params.Phases)
	}
}

func TestStripeSubscriptionScheduleCanUpdate(t *testing.T) {
	for _, status := range []stripe.SubscriptionScheduleStatus{
		stripe.SubscriptionScheduleStatusActive,
		stripe.SubscriptionScheduleStatusNotStarted,
	} {
		if !stripeSubscriptionScheduleCanUpdate(status) {
			t.Fatalf("status %q should be mutable", status)
		}
	}
	for _, status := range []stripe.SubscriptionScheduleStatus{
		stripe.SubscriptionScheduleStatusReleased,
		stripe.SubscriptionScheduleStatusCanceled,
		stripe.SubscriptionScheduleStatusCompleted,
	} {
		if stripeSubscriptionScheduleCanUpdate(status) {
			t.Fatalf("status %q should not be mutable", status)
		}
	}
}

func TestStripePlanSwitchAlreadyPendingSkipsStripe(t *testing.T) {
	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	result, err := (&Bot{}).switchStripeSubscriptionPlan(
		t.Context(),
		"org_1",
		billing.Account{
			OrgID:                  "org_1",
			StripeSubscriptionID:   "sub_1",
			PlanCode:               billing.PlanBusiness,
			PendingPlanCode:        billing.PlanTeam,
			PendingPlanEffectiveAt: time.Now().Add(time.Hour),
		},
		team,
		"price_team",
	)
	if err != nil {
		t.Fatalf("switchStripeSubscriptionPlan returned error: %v", err)
	}
	if result != stripePlanSwitchScheduled {
		t.Fatalf("result = %q, want %q", result, stripePlanSwitchScheduled)
	}
}

func TestStripeDowngradeScheduleParamsAppliesNextCycle(t *testing.T) {
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)
	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	params := stripeDowngradeScheduleParams(
		"sub_sched_1",
		"org_1",
		billing.Account{OrgID: "org_1", CurrentPeriodStart: start, CurrentPeriodEnd: end},
		&stripe.SubscriptionItem{ID: "si_1", Price: &stripe.Price{ID: "price_growth"}, Quantity: 1},
		growth,
		"price_growth",
		team,
		"price_team",
	)

	if got := *params.ProrationBehavior; got != "none" {
		t.Fatalf("ProrationBehavior = %q, want none", got)
	}
	if len(params.Phases) != 2 {
		t.Fatalf("len(Phases) = %d, want 2", len(params.Phases))
	}
	if got := *params.Phases[0].EndDate; got != end.Unix() {
		t.Fatalf("current phase end = %d, want %d", got, end.Unix())
	}
	if got := *params.Phases[0].Items[0].Price; got != "price_growth" {
		t.Fatalf("current phase price = %q, want price_growth", got)
	}
	if got := *params.Phases[1].Items[0].Price; got != "price_team" {
		t.Fatalf("next phase price = %q, want price_team", got)
	}
	if got := params.Phases[1].Metadata["plan_code"]; got != billing.PlanTeam {
		t.Fatalf("next phase metadata plan_code = %q, want team", got)
	}
	if key := *params.Params.IdempotencyKey; !strings.Contains(key, "sub_sched_1") || !strings.Contains(key, "price_growth") || !strings.Contains(key, "price_team") {
		t.Fatalf("idempotency key = %q, want schedule ID plus current and target prices", key)
	}
}
