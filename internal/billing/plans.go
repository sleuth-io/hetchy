package billing

type PaidPlan struct {
	Code              string
	Label             string
	MonthlyUSDCents   int
	TopupUnitUSDCents int
	IncludedCredits   int
	MaxFlavor         string
	PerRunMaxCredits  int
}

const (
	FreeIncludedCredits  = 25
	FreePerRunMaxCredits = 4
)

var paidPlans = []PaidPlan{
	{
		Code: PlanBuilder, Label: "Builder", MonthlyUSDCents: 1900, TopupUnitUSDCents: 2500,
		IncludedCredits: 100, MaxFlavor: FlavorStandard, PerRunMaxCredits: 4,
	},
	{
		Code: PlanStudio, Label: "Studio", MonthlyUSDCents: 7900, TopupUnitUSDCents: 2200,
		IncludedCredits: 500, MaxFlavor: FlavorPlus, PerRunMaxCredits: 12,
	},
	{
		Code: PlanGrowth, Label: "Growth", MonthlyUSDCents: 19900, TopupUnitUSDCents: 2000,
		IncludedCredits: 1400, MaxFlavor: FlavorPlus, PerRunMaxCredits: 24,
	},
	{
		Code: PlanBusiness, Label: "Business", MonthlyUSDCents: 49900, TopupUnitUSDCents: 1600,
		IncludedCredits: 3600, MaxFlavor: FlavorPlus, PerRunMaxCredits: 48,
	},
}

func PaidPlans() []PaidPlan {
	out := make([]PaidPlan, len(paidPlans))
	copy(out, paidPlans)
	return out
}

func DefaultPaidPlan() PaidPlan {
	return paidPlans[1]
}

func PaidPlanByCode(code string) (PaidPlan, bool) {
	for _, plan := range paidPlans {
		if plan.Code == code {
			return plan, true
		}
	}
	return PaidPlan{}, false
}
