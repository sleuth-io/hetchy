package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/billing"
	"github.com/hetchyhq/hetchy/internal/webui"
)

func TestSettingsTemplate_RendersBillingTabLayout(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "billing", "SavedMessage": "",
		"Billing": billingOverviewView{
			PlanCode:          billing.PlanTeam,
			CurrentPlanLabel:  "Team",
			Status:            "active",
			IncludedCredits:   300,
			IncludedUsed:      60,
			IncludedRemaining: 240,
			TopupCredits:      20,
			Balance:           260,
			MaxFlavor:         billing.FlavorMax,
			SandboxOptions:    "All sizes",
			PerRunMaxCredits:  6,
			AutoTopupEnabled:  true,
			MonthlyMaxSpend:   "27",
			MonthlySpendUsed:  "$9",
			TopupUnitPrice:    "$9",
			TopupUnitCredits:  billing.TopupUnitCredits,
			StripeConfigured:  true,
			HasStripeCustomer: true,
			PlanOptions: []billingPlanOptionView{
				{
					Code: billing.PlanTeam, Label: "Team", Monthly: "$199", TopupUnitPrice: "$9",
					IncludedCredits: 300, MaxFlavor: billing.FlavorMax, SandboxOptions: "All sizes", PerRunMaxCredits: 6,
					Configured: true, Current: true, ActionLabel: "Current",
				},
				{
					Code: billing.PlanGrowth, Label: "Growth", Monthly: "$499", TopupUnitPrice: "$6.50",
					IncludedCredits: 1000, MaxFlavor: billing.FlavorMax, SandboxOptions: "All sizes", PerRunMaxCredits: 6,
					Configured: true, ActionLabel: "Switch", ConfirmTitle: "Switch to Growth?",
					ConfirmMessage: "This upgrade takes effect immediately. Stripe will invoice the prorated difference now.",
				},
			},
			RecentMeters: []billingMeterView{
				{
					RunID: "run_123", Flavor: billing.FlavorMax, BillableMinutes: 15,
					CapturedCredits: 4, TerminalState: "succeeded", StartedAt: "May 18, 2026",
				},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="billing-panel billing-section"`,
		`<h3>Plan</h3>`,
		`<h3>Usage</h3>`,
		`class="billing-usage-meter"`,
		`Credit balance`,
		`260 credits available`,
		`Included credits used`,
		`60 / 300`,
		`240 included credits remaining this period`,
		`20 top-up credits available after included credits`,
		`class="billing-topups-panel"`,
		`Buy credits now`,
		`class="secondary" type="submit"`,
		`Save top-up settings`,
		`name="quantity" value="1"`,
		`name="monthly_max_spend"`,
		`Monthly max spend`,
		`Recent runs`,
		`run_123`,
		`target="_blank"`,
		`href="/billing/portal"`,
		`One credit covers a 15-minute run on a standard sandbox.`,
		`Included credits`,
		`Top-up price`,
		`per 10-credit top-up`,
		`Sandbox size options: All sizes`,
		`Switch`,
		`id="billing-plan-switch-dialog"`,
		`data-billing-plan-confirm="1"`,
		`data-confirm-title="Switch to Growth?"`,
		`data-confirm-action="Switch"`,
		`billing-plan-switch-confirm`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("billing tab missing %q", want)
		}
	}
	for _, notWant := range []string{
		`<h3>Credits</h3>`,
		`class="billing-credit-row"`,
		`class="billing-usage-controls"`,
		`class="billing-control-card"`,
		`Save auto top-up`,
		`Buy top-up credits`,
		`id="topup_quantity"`,
		`Manual top-up units`,
		`name="trigger_threshold"`,
		`name="target_balance"`,
		`name="monthly_max_units"`,
		`Recent usage`,
		`max flavor`,
		`run reserve`,
	} {
		if strings.Contains(body, notWant) {
			t.Errorf("billing tab unexpectedly contained %q", notWant)
		}
	}
}

func TestSettingsTemplate_RendersPendingBillingPlanChange(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "billing", "SavedMessage": "",
		"Billing": billingOverviewView{
			PlanCode:          billing.PlanBusiness,
			CurrentPlanLabel:  "Business",
			Status:            "active",
			PeriodEnd:         "Jun 18, 2026",
			PendingPlanCode:   billing.PlanTeam,
			PendingPlanLabel:  "Team",
			PendingPlanAt:     "Jun 18, 2026",
			HasPendingPlan:    true,
			IncludedCredits:   4000,
			Balance:           4000,
			SandboxOptions:    "All sizes",
			TopupUnitCredits:  billing.TopupUnitCredits,
			StripeConfigured:  true,
			HasStripeCustomer: true,
			PlanOptions: []billingPlanOptionView{
				{
					Code: billing.PlanTeam, Label: "Team", Monthly: "$199", TopupUnitPrice: "$9",
					IncludedCredits: 300, SandboxOptions: "All sizes", Configured: true,
					Scheduled: true, ActionLabel: "Scheduled",
				},
				{
					Code: billing.PlanBusiness, Label: "Business", Monthly: "$1499", TopupUnitPrice: "$4.50",
					IncludedCredits: 4000, SandboxOptions: "All sizes", Configured: true,
					Current: true, ActionLabel: "Current",
				},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`Team scheduled`,
		`Takes effect on Jun 18, 2026.`,
		`Your current Business plan stays active until then.`,
		`class="billing-plan-option scheduled"`,
		`<button type="submit" disabled>Scheduled</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pending billing tab missing %q", want)
		}
	}
	if strings.Contains(body, `data-confirm-title="Switch to Team?"`) {
		t.Error("scheduled pending plan should not submit through the switch confirmation")
	}
}

func TestSettingsTemplate_RendersCompedBillingBanner(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": false, "Tab": "billing", "SavedMessage": "",
		"Billing": billingOverviewView{
			CurrentPlanLabel:  "Comped",
			Status:            "comped",
			BillingExempt:     true,
			IncludedCredits:   10,
			IncludedRemaining: 10,
			Balance:           10,
			SandboxOptions:    "All sizes",
			TopupUnitCredits:  billing.TopupUnitCredits,
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`Comped access enabled`,
		`full Hetchy access through WorkOS`,
		`runs are not charged against credits`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comped billing tab missing %q", want)
		}
	}
}
