package bot

import (
	"testing"
	"time"

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
