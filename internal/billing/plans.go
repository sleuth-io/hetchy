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

var paidPlans = []PaidPlan{
	{
		Code: PlanStarter, Label: "Starter", MonthlyUSDCents: 4900, TopupUnitUSDCents: 1250,
		IncludedCredits: 50, MaxFlavor: FlavorStandard, PerRunMaxCredits: 1,
	},
	{
		Code: PlanTeam, Label: "Team", MonthlyUSDCents: 19900, TopupUnitUSDCents: 900,
		IncludedCredits: 300, MaxFlavor: FlavorMax, PerRunMaxCredits: 6,
	},
	{
		Code: PlanGrowth, Label: "Growth", MonthlyUSDCents: 49900, TopupUnitUSDCents: 650,
		IncludedCredits: 1000, MaxFlavor: FlavorMax, PerRunMaxCredits: 6,
	},
	{
		Code: PlanBusiness, Label: "Business", MonthlyUSDCents: 149900, TopupUnitUSDCents: 450,
		IncludedCredits: 4000, MaxFlavor: FlavorMax, PerRunMaxCredits: 6,
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
