package billing

import "testing"

func TestPaidPlansMatchGenerousPricing(t *testing.T) {
	builder, ok := PaidPlanByCode(PlanBuilder)
	if !ok {
		t.Fatal("builder plan not found")
	}
	if builder.Label != "Builder" || builder.MonthlyUSDCents != 1900 || builder.TopupUnitUSDCents != 2500 ||
		builder.IncludedCredits != 100 || builder.MaxFlavor != FlavorStandard || builder.PerRunMaxCredits != 4 {
		t.Fatalf("builder plan = %+v", builder)
	}

	studio, ok := PaidPlanByCode(PlanStudio)
	if !ok {
		t.Fatal("studio plan not found")
	}
	if studio.Label != "Studio" || studio.MonthlyUSDCents != 7900 || studio.TopupUnitUSDCents != 2200 ||
		studio.IncludedCredits != 500 || studio.MaxFlavor != FlavorPlus || studio.PerRunMaxCredits != 12 {
		t.Fatalf("studio plan = %+v", studio)
	}
}
