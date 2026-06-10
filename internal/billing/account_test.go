package billing

import "testing"

func TestAccountIncludedRemaining(t *testing.T) {
	cases := []struct {
		name    string
		a       Account
		wantRem int
		wantBal int
	}{
		{
			name:    "no credits used",
			a:       Account{IncludedCredits: 100, IncludedCreditsUsed: 0, TopupCredits: 0},
			wantRem: 100,
			wantBal: 100,
		},
		{
			name:    "some credits used",
			a:       Account{IncludedCredits: 100, IncludedCreditsUsed: 40, TopupCredits: 0},
			wantRem: 60,
			wantBal: 60,
		},
		{
			name:    "all included credits used",
			a:       Account{IncludedCredits: 100, IncludedCreditsUsed: 100, TopupCredits: 0},
			wantRem: 0,
			wantBal: 0,
		},
		{
			name:    "over-used credits clamp to zero",
			a:       Account{IncludedCredits: 100, IncludedCreditsUsed: 150, TopupCredits: 0},
			wantRem: 0,
			wantBal: 0,
		},
		{
			name:    "topup credits add to balance",
			a:       Account{IncludedCredits: 100, IncludedCreditsUsed: 40, TopupCredits: 200},
			wantRem: 60,
			wantBal: 260,
		},
		{
			name:    "only topup credits remaining",
			a:       Account{IncludedCredits: 0, IncludedCreditsUsed: 0, TopupCredits: 50},
			wantRem: 0,
			wantBal: 50,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.IncludedRemaining(); got != tc.wantRem {
				t.Errorf("IncludedRemaining() = %d, want %d", got, tc.wantRem)
			}
			if got := tc.a.Balance(); got != tc.wantBal {
				t.Errorf("Balance() = %d, want %d", got, tc.wantBal)
			}
		})
	}
}

func TestPaidPlansReturnsDefensiveCopy(t *testing.T) {
	plans := PaidPlans()
	if len(plans) == 0 {
		t.Fatal("PaidPlans() returned empty slice")
	}
	// mutating the returned slice must not affect subsequent calls
	plans[0].Label = "MUTATED"
	plans2 := PaidPlans()
	if plans2[0].Label == "MUTATED" {
		t.Error("PaidPlans() did not return a defensive copy")
	}
}

func TestDefaultPaidPlan(t *testing.T) {
	plan := DefaultPaidPlan()
	// default is the studio (team) plan
	if plan.Code != PlanStudio {
		t.Errorf("DefaultPaidPlan().Code = %q, want %q", plan.Code, PlanStudio)
	}
	if plan.Label != "Studio" {
		t.Errorf("DefaultPaidPlan().Label = %q, want Studio", plan.Label)
	}
}

func TestPaidPlanByCodeUnknown(t *testing.T) {
	_, ok := PaidPlanByCode("no-such-plan")
	if ok {
		t.Error("PaidPlanByCode(unknown) should return ok=false")
	}
}

func TestPaidPlanCodesAreConsistent(t *testing.T) {
	for _, plan := range PaidPlans() {
		found, ok := PaidPlanByCode(plan.Code)
		if !ok {
			t.Errorf("PaidPlanByCode(%q) not found, but it is in PaidPlans()", plan.Code)
		}
		if found.Label != plan.Label {
			t.Errorf("PaidPlanByCode(%q).Label = %q, want %q", plan.Code, found.Label, plan.Label)
		}
	}
}

func TestAutoTopupIdempotencyKey(t *testing.T) {
	k1 := autoTopupIdempotencyKey("org1", "starter", "2026-01", 1, 2500)
	k2 := autoTopupIdempotencyKey("org1", "starter", "2026-01", 1, 2500)
	if k1 != k2 {
		t.Error("same inputs must produce same idempotency key")
	}
	k3 := autoTopupIdempotencyKey("org1", "starter", "2026-01", 2, 2500)
	if k1 == k3 {
		t.Error("different unit number must produce different idempotency key")
	}
	if !hasPrefix(k1, "hetchy-auto-topup-") {
		t.Errorf("idempotency key %q should start with 'hetchy-auto-topup-'", k1)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
