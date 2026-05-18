package bot

import "testing"

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
