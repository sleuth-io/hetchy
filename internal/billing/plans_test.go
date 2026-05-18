package billing

import "testing"

func TestPaidPlansSandboxOptions(t *testing.T) {
	team, ok := PaidPlanByCode(PlanTeam)
	if !ok {
		t.Fatal("team plan not found")
	}
	if team.MaxFlavor != FlavorMax {
		t.Fatalf("team MaxFlavor = %q, want %q", team.MaxFlavor, FlavorMax)
	}

	starter, ok := PaidPlanByCode(PlanStarter)
	if !ok {
		t.Fatal("starter plan not found")
	}
	if starter.MaxFlavor != FlavorStandard {
		t.Fatalf("starter MaxFlavor = %q, want %q", starter.MaxFlavor, FlavorStandard)
	}
}
