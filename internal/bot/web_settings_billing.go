package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/billing"
)

type billingOverviewView struct {
	PlanCode          string
	CurrentPlanLabel  string
	Status            string
	PeriodStart       string
	PeriodEnd         string
	PendingPlanCode   string
	PendingPlanLabel  string
	PendingPlanAt     string
	HasPendingPlan    bool
	IncludedCredits   int
	IncludedUsed      int
	IncludedRemaining int
	TopupCredits      int
	Balance           int
	MaxFlavor         string
	SandboxOptions    string
	PerRunMaxCredits  int
	BillingExempt     bool
	LastPaymentError  string
	AutoTopupEnabled  bool
	MonthlyMaxSpend   string
	MonthlySpendUsed  string
	TopupUnitPrice    string
	TopupUnitCredits  int
	StripeConfigured  bool
	HasStripeCustomer bool
	HasSubscription   bool
	CanTopup          bool
	PlanOptions       []billingPlanOptionView
	RecentMeters      []billingMeterView
}

type billingPlanOptionView struct {
	Code             string
	Label            string
	Monthly          string
	TopupUnitPrice   string
	IncludedCredits  int
	MaxFlavor        string
	SandboxOptions   string
	PerRunMaxCredits int
	Configured       bool
	Current          bool
	Scheduled        bool
	ActionLabel      string
	ConfirmTitle     string
	ConfirmMessage   string
}

type billingMeterView struct {
	RunID           string
	Flavor          string
	BillableMinutes int
	CapturedCredits int
	TerminalState   string
	StartedAt       string
}

func (b *Bot) loadBillingOverview(ctx context.Context, orgID string) (billingOverviewView, error) {
	if b.billing == nil || !b.billing.Enabled() {
		plan := billing.DefaultPaidPlan()
		return billingOverviewView{
			PlanCode: "free", CurrentPlanLabel: "Free", Status: "free", MaxFlavor: billing.FlavorStandard,
			SandboxOptions:  sandboxOptionsLabel(billing.FlavorStandard),
			IncludedCredits: billing.FreeIncludedCredits, IncludedRemaining: billing.FreeIncludedCredits,
			Balance: billing.FreeIncludedCredits, PerRunMaxCredits: billing.FreePerRunMaxCredits,
			TopupUnitPrice: formatUSDCents(plan.TopupUnitUSDCents), TopupUnitCredits: billing.TopupUnitCredits,
			MonthlyMaxSpend: "0", MonthlySpendUsed: "$0",
		}, nil
	}
	overview, err := b.billing.Overview(ctx, orgID)
	if err != nil {
		return billingOverviewView{}, err
	}
	acct := overview.Account
	settings := overview.TopupSettings
	displayAcct, plan := billingDisplayAccount(acct)
	pendingPlanCode, pendingPlanLabel, pendingPlanAt, hasPendingPlan := billingPendingPlanChange(acct)
	currentPlanLabel := billingPlanLabel(displayAcct.PlanCode)
	status := displayAcct.Status
	sandboxOptions := sandboxOptionsLabel(displayAcct.MaxFlavor)
	if acct.BillingExempt {
		currentPlanLabel = "Comped"
		status = "comped"
	}
	out := billingOverviewView{
		PlanCode:          displayAcct.PlanCode,
		CurrentPlanLabel:  currentPlanLabel,
		Status:            status,
		PeriodStart:       formatBillingTime(acct.CurrentPeriodStart),
		PeriodEnd:         formatBillingTime(acct.CurrentPeriodEnd),
		PendingPlanCode:   pendingPlanCode,
		PendingPlanLabel:  pendingPlanLabel,
		PendingPlanAt:     pendingPlanAt,
		HasPendingPlan:    hasPendingPlan,
		IncludedCredits:   displayAcct.IncludedCredits,
		IncludedUsed:      displayAcct.IncludedCreditsUsed,
		IncludedRemaining: displayAcct.IncludedRemaining(),
		TopupCredits:      displayAcct.TopupCredits,
		Balance:           displayAcct.Balance(),
		MaxFlavor:         displayAcct.MaxFlavor,
		SandboxOptions:    sandboxOptions,
		PerRunMaxCredits:  displayAcct.PerRunMaxCredits,
		BillingExempt:     acct.BillingExempt,
		LastPaymentError:  acct.LastPaymentError,
		AutoTopupEnabled:  settings.AutoTopupEnabled,
		MonthlyMaxSpend:   formatUSDDollarInput(settings.MonthlyMaxCents),
		MonthlySpendUsed:  formatUSDCents(settings.MonthlySpendCentsUsed),
		TopupUnitPrice:    formatUSDCents(plan.TopupUnitUSDCents),
		TopupUnitCredits:  billing.TopupUnitCredits,
		StripeConfigured:  b.stripeConfigured(),
		HasStripeCustomer: acct.StripeCustomerID != "",
		HasSubscription:   acct.StripeSubscriptionID != "",
		CanTopup:          billingAccountAllowsTopups(acct),
		PlanOptions:       b.billingPlanOptions(displayAcct.PlanCode, pendingPlanCode, acct.StripeSubscriptionID != "", formatBillingTime(acct.CurrentPeriodEnd)),
	}
	for _, meter := range overview.RecentMeters {
		out.RecentMeters = append(out.RecentMeters, billingMeterView{
			RunID:           meter.RunID,
			Flavor:          meter.Flavor,
			BillableMinutes: meter.BillableMinutes,
			CapturedCredits: meter.CapturedCredits,
			TerminalState:   meter.TerminalState,
			StartedAt:       formatBillingTime(meter.StartedAt),
		})
	}
	return out, nil
}

func billingDisplayAccount(acct billing.Account) (billing.Account, billing.PaidPlan) {
	plan := billingPlanForTopup(acct.PlanCode)
	if !acct.BillingExempt {
		return acct, plan
	}
	business, ok := billing.PaidPlanByCode(billing.PlanBusiness)
	if !ok {
		return acct, plan
	}
	acct.PlanCode = business.Code
	acct.Status = "comped"
	acct.IncludedCredits = business.IncludedCredits
	acct.IncludedCreditsUsed = 0
	acct.MaxFlavor = business.MaxFlavor
	acct.PerRunMaxCredits = business.PerRunMaxCredits
	return acct, business
}

func billingAccountAllowsTopups(acct billing.Account) bool {
	if acct.BillingExempt {
		return false
	}
	_, ok := billing.PaidPlanByCode(strings.ToLower(strings.TrimSpace(acct.PlanCode)))
	return ok
}

func (b *Bot) billingPlanOptions(currentPlan, pendingPlan string, hasSubscription bool, periodEnd string) []billingPlanOptionView {
	out := make([]billingPlanOptionView, 0, len(billing.PaidPlans()))
	currentPaidPlan, hasCurrentPaidPlan := billing.PaidPlanByCode(currentPlan)
	for _, plan := range billing.PaidPlans() {
		current := plan.Code == currentPlan
		scheduled := plan.Code == pendingPlan
		confirmTitle, confirmMessage := billingPlanSwitchConfirmation(currentPaidPlan, hasCurrentPaidPlan, plan, current || scheduled, hasSubscription, periodEnd)
		out = append(out, billingPlanOptionView{
			Code:             plan.Code,
			Label:            plan.Label,
			Monthly:          formatUSDCents(plan.MonthlyUSDCents),
			TopupUnitPrice:   formatUSDCents(plan.TopupUnitUSDCents),
			IncludedCredits:  plan.IncludedCredits,
			MaxFlavor:        plan.MaxFlavor,
			SandboxOptions:   sandboxOptionsLabel(plan.MaxFlavor),
			PerRunMaxCredits: plan.PerRunMaxCredits,
			Configured:       b.stripeSubscriptionPriceID(plan.Code) != "",
			Current:          current,
			Scheduled:        scheduled,
			ActionLabel:      billingPlanActionLabel(current, scheduled, hasSubscription),
			ConfirmTitle:     confirmTitle,
			ConfirmMessage:   confirmMessage,
		})
	}
	return out
}

func billingPendingPlanChange(acct billing.Account) (string, string, string, bool) {
	code := strings.TrimSpace(acct.PendingPlanCode)
	if code == "" || code == acct.PlanCode {
		return "", "", "", false
	}
	return code, billingPlanLabel(code), formatBillingTime(acct.PendingPlanEffectiveAt), true
}

func billingPlanActionLabel(current, scheduled, hasSubscription bool) string {
	if current {
		return "Current"
	}
	if scheduled {
		return "Scheduled"
	}
	if hasSubscription {
		return "Switch"
	}
	return "Choose"
}

func billingPlanSwitchConfirmation(currentPlan billing.PaidPlan, hasCurrentPlan bool, targetPlan billing.PaidPlan, current, hasSubscription bool, periodEnd string) (string, string) {
	if current || !hasSubscription || !hasCurrentPlan {
		return "", ""
	}
	title := "Switch to " + targetPlan.Label + "?"
	if isBillingPlanDowngrade(currentPlan, targetPlan) {
		when := "the start of your next billing cycle"
		if periodEnd != "" {
			when = periodEnd
		}
		return title, fmt.Sprintf("This downgrade will take effect on %s. Your current %s plan stays active until then, and there is no immediate charge.", when, currentPlan.Label)
	}
	return title, fmt.Sprintf("This upgrade takes effect immediately. Stripe will invoice the prorated difference now, and the %s plan limits will apply after the switch succeeds.", targetPlan.Label)
}

func isBillingPlanDowngrade(currentPlan, targetPlan billing.PaidPlan) bool {
	return targetPlan.MonthlyUSDCents < currentPlan.MonthlyUSDCents
}

func sandboxOptionsLabel(maxFlavor string) string {
	if maxFlavor == billing.FlavorStandard {
		return "Standard only"
	}
	return "Standard and Plus"
}

func billingPlanForTopup(planCode string) billing.PaidPlan {
	if plan, ok := billing.PaidPlanByCode(planCode); ok {
		return plan
	}
	return billing.DefaultPaidPlan()
}

func billingPlanLabel(planCode string) string {
	if plan, ok := billing.PaidPlanByCode(planCode); ok {
		return plan.Label
	}
	switch planCode {
	case "", billing.PlanFree:
		return "Free"
	case billing.PlanTrial:
		return "Trial"
	default:
		return planCode
	}
}

func formatUSDCents(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("$%d", cents/100)
	}
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}

func formatUSDDollarInput(cents int) string {
	if cents <= 0 {
		return "0"
	}
	if cents%100 == 0 {
		return strconv.Itoa(cents / 100)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func (b *Bot) repoBillingViewData(ctx context.Context, orgID string) (map[string]billing.RepoSetting, []billing.Flavor) {
	settings := map[string]billing.RepoSetting{}
	allowed := []billing.Flavor{billing.MustFlavor(billing.FlavorStandard)}
	if b.billing == nil || !b.billing.Enabled() {
		return settings, allowed
	}
	if overview, err := b.billing.Overview(ctx, orgID); err == nil {
		allowed = overview.AllowedFlavors
		if overview.Account.BillingExempt {
			allowed = billing.AllowedFlavors(billing.FlavorEnterprise)
		}
	}
	if rows, err := b.billing.ListRepoSettings(ctx, orgID); err == nil {
		settings = rows
	}
	return settings, allowed
}

func formatBillingTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("Jan 2, 2006")
}

func (b *Bot) repoFlavorSettingsHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	owner := strings.TrimSpace(r.FormValue("owner"))
	name := strings.TrimSpace(r.FormValue("name"))
	flavor := strings.TrimSpace(r.FormValue("flavor"))
	if owner == "" || name == "" || flavor == "" {
		http.Error(w, "owner, name, and flavor are required", http.StatusBadRequest)
		return
	}
	if _, err := b.lookupRepoForOrg(r.Context(), p.OrgID, owner, name); err != nil {
		writeRepoErr(w, err)
		return
	}
	if b.billing == nil || !b.billing.Enabled() {
		http.Error(w, "billing is not configured", http.StatusInternalServerError)
		return
	}
	if _, err := b.billing.SetRepoFlavor(r.Context(), p.OrgID, owner, name, flavor); err != nil {
		var flavorErr billing.FlavorNotAllowedError
		if errors.Is(err, billing.ErrUnknownFlavor) || errors.As(err, &flavorErr) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.log.Error("set repo billing flavor", "error", err, "org", p.OrgID, "owner", owner, "repo", name)
		http.Error(w, "save repo flavor: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("repo billing flavor saved", "org", p.OrgID, "actor", p.UserID, "owner", owner, "repo", name, "flavor", flavor)
	http.Redirect(w, r, "/settings/org?tab=repositories&saved=repo_flavor_saved", http.StatusFound)
}

func (b *Bot) billingTopupSettingsHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if b.billing == nil || !b.billing.Enabled() {
		http.Error(w, "billing is not configured", http.StatusInternalServerError)
		return
	}
	overview, err := b.billing.Overview(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("load billing account for top-up settings", "error", err, "org", p.OrgID)
		http.Error(w, "load billing settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !billingAccountAllowsTopups(overview.Account) {
		http.Error(w, "top-ups require a paid plan", http.StatusBadRequest)
		return
	}
	settings := billingTopupSettingsFromSpend(
		overview.Account,
		r.FormValue("auto_topup_enabled") == "1",
		parseBillingCents(r.FormValue("monthly_max_spend"), 0),
	)
	if _, err := b.billing.UpdateTopupSettings(r.Context(), p.OrgID, settings); err != nil {
		b.log.Error("update billing top-up settings", "error", err, "org", p.OrgID)
		http.Error(w, "save billing settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/org?tab=billing&saved=billing_saved", http.StatusFound)
}

func billingTopupSettingsFromSpend(account billing.Account, enabled bool, monthlyMaxSpendCents int) billing.TopupSettings {
	plan := billingPlanForTopup(account.PlanCode)
	monthlyMaxUnits := 0
	if monthlyMaxSpendCents > 0 && plan.TopupUnitUSDCents > 0 {
		monthlyMaxUnits = monthlyMaxSpendCents / plan.TopupUnitUSDCents
	}
	reserve := max(account.PerRunMaxCredits, 1)
	return billing.TopupSettings{
		AutoTopupEnabled: enabled,
		TriggerThreshold: reserve,
		TargetBalance:    reserve + billing.TopupUnitCredits,
		MonthlyMaxUnits:  monthlyMaxUnits,
		MonthlyMaxCents:  max(monthlyMaxSpendCents, 0),
	}
}

func parseBillingInt(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func parseBillingCents(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "$")
	raw = strings.ReplaceAll(raw, ",", "")
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 {
		return def
	}
	dollarsRaw := parts[0]
	if dollarsRaw == "" {
		dollarsRaw = "0"
	}
	dollars, err := strconv.Atoi(dollarsRaw)
	if err != nil || dollars < 0 {
		return def
	}
	cents := 0
	if len(parts) == 2 {
		centsRaw := parts[1]
		if centsRaw == "" {
			centsRaw = "0"
		}
		if len(centsRaw) > 2 {
			return def
		}
		for len(centsRaw) < 2 {
			centsRaw += "0"
		}
		cents, err = strconv.Atoi(centsRaw)
		if err != nil || cents < 0 {
			return def
		}
	}
	return dollars*100 + cents
}
