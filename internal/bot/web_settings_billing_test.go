package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func TestLoadBillingOverviewDisabledDefaults(t *testing.T) {
	b := &Bot{}
	overview, err := b.loadBillingOverview(t.Context(), b.billingSvc(), "org_1")
	if err != nil {
		t.Fatalf("loadBillingOverview returned error: %v", err)
	}
	if overview.PlanCode != billing.PlanFree {
		t.Fatalf("PlanCode = %q, want free", overview.PlanCode)
	}
	if overview.Balance != billing.FreeIncludedCredits || overview.IncludedRemaining != billing.FreeIncludedCredits {
		t.Fatalf("default credits = balance %d remaining %d, want %d/%d",
			overview.Balance, overview.IncludedRemaining, billing.FreeIncludedCredits, billing.FreeIncludedCredits)
	}
	if overview.SandboxOptions != "Standard only" {
		t.Fatalf("SandboxOptions = %q, want Standard only", overview.SandboxOptions)
	}
	if overview.MonthlyMaxSpend != "0" || overview.MonthlySpendUsed != "$0" {
		t.Fatalf("monthly spend labels = %q/%q, want 0/$0", overview.MonthlyMaxSpend, overview.MonthlySpendUsed)
	}
}

func TestBillingPlanOptionsAndConfirmationCopy(t *testing.T) {
	b := &Bot{cfg: Config{
		StripeSubscriptionPriceIDs: map[string]string{
			billing.PlanStarter: "price_starter",
			billing.PlanTeam:    "price_team",
		},
	}}
	options := b.billingPlanOptions(billing.PlanTeam, billing.PlanStarter, true, "Jun 1, 2026")
	if len(options) != len(billing.PaidPlans()) {
		t.Fatalf("len(options) = %d, want %d", len(options), len(billing.PaidPlans()))
	}

	var starter, team, growth billingPlanOptionView
	for _, option := range options {
		switch option.Code {
		case billing.PlanStarter:
			starter = option
		case billing.PlanTeam:
			team = option
		case billing.PlanGrowth:
			growth = option
		}
	}
	if !starter.Configured || !starter.Scheduled || starter.ActionLabel != "Scheduled" {
		t.Fatalf("starter option = %+v, want configured scheduled", starter)
	}
	if !team.Current || team.ActionLabel != "Current" {
		t.Fatalf("team option = %+v, want current", team)
	}
	if growth.Configured || growth.ActionLabel != "Switch" {
		t.Fatalf("growth option = %+v, want unconfigured switch", growth)
	}
	if !strings.Contains(growth.ConfirmMessage, "takes effect immediately") {
		t.Fatalf("growth confirm message = %q, want immediate upgrade copy", growth.ConfirmMessage)
	}
}

func TestBillingDisplayAccountUsesBusinessLimitsForCompedOrgs(t *testing.T) {
	acct, plan := billingDisplayAccount(billing.Account{
		PlanCode:            billing.PlanFree,
		Status:              billing.PlanFree,
		IncludedCredits:     billing.FreeIncludedCredits,
		IncludedCreditsUsed: 7,
		TopupCredits:        3,
		MaxFlavor:           billing.FlavorStandard,
		PerRunMaxCredits:    billing.FreePerRunMaxCredits,
		BillingExempt:       true,
	})

	if plan.Code != billing.PlanBusiness {
		t.Fatalf("display plan = %q, want business", plan.Code)
	}
	if acct.PlanCode != billing.PlanBusiness || acct.Status != "comped" {
		t.Fatalf("display account plan/status = %q/%q, want business/comped", acct.PlanCode, acct.Status)
	}
	if acct.IncludedCredits != 3600 || acct.IncludedCreditsUsed != 0 || acct.IncludedRemaining() != 3600 {
		t.Fatalf("display credits = included %d used %d remaining %d, want 3600/0/3600",
			acct.IncludedCredits, acct.IncludedCreditsUsed, acct.IncludedRemaining())
	}
	if acct.TopupCredits != 0 || acct.Balance() != 3600 {
		t.Fatalf("display topups/balance = %d/%d, want 0/3600", acct.TopupCredits, acct.Balance())
	}
	if acct.MaxFlavor != billing.FlavorPlus || acct.PerRunMaxCredits != 48 {
		t.Fatalf("display limits = %s/%d, want plus/48", acct.MaxFlavor, acct.PerRunMaxCredits)
	}
}

func TestBillingAccountAllowsTopupsOnlyForPaidNonCompedPlans(t *testing.T) {
	if billingAccountAllowsTopups(billing.Account{PlanCode: billing.PlanFree}) {
		t.Fatal("free account unexpectedly allows top-ups")
	}
	if billingAccountAllowsTopups(billing.Account{PlanCode: billing.PlanBusiness, BillingExempt: true}) {
		t.Fatal("comped account unexpectedly allows top-ups")
	}
	if !billingAccountAllowsTopups(billing.Account{PlanCode: " TEAM "}) {
		t.Fatal("paid plan should allow top-ups")
	}
}

func TestBillingSettingsFormattingHelpers(t *testing.T) {
	if got := billingPlanActionLabel(false, false, false); got != "Choose" {
		t.Fatalf("billingPlanActionLabel = %q, want Choose", got)
	}
	if got := billingPlanActionLabel(false, false, true); got != "Switch" {
		t.Fatalf("billingPlanActionLabel subscription = %q, want Switch", got)
	}
	if got := sandboxOptionsLabel(billing.FlavorPlus); got != "Standard and Plus" {
		t.Fatalf("sandboxOptionsLabel = %q, want Standard and Plus", got)
	}
	if got := billingPlanLabel(""); got != "Free" {
		t.Fatalf("billingPlanLabel empty = %q, want Free", got)
	}
	if got := billingPlanLabel(billing.PlanTrial); got != "Trial" {
		t.Fatalf("billingPlanLabel trial = %q, want Trial", got)
	}
	if got := billingPlanLabel("custom"); got != "custom" {
		t.Fatalf("billingPlanLabel custom = %q, want custom", got)
	}
	if got := formatUSDCents(1250); got != "$12.50" {
		t.Fatalf("formatUSDCents = %q, want $12.50", got)
	}
	if got := formatUSDDollarInput(1250); got != "12.50" {
		t.Fatalf("formatUSDDollarInput = %q, want 12.50", got)
	}
	if got := formatUSDDollarInput(0); got != "0" {
		t.Fatalf("formatUSDDollarInput zero = %q, want 0", got)
	}
	if got := formatUSDDollarInput(1200); got != "12" {
		t.Fatalf("formatUSDDollarInput dollars = %q, want 12", got)
	}
	if got := parseBillingInt(" bad ", 7); got != 7 {
		t.Fatalf("parseBillingInt bad = %d, want 7", got)
	}
	if got := parseBillingInt("-1", 7); got != 7 {
		t.Fatalf("parseBillingInt negative = %d, want 7", got)
	}
	if got := parseBillingCents("$1,234.5", 0); got != 123450 {
		t.Fatalf("parseBillingCents = %d, want 123450", got)
	}
	if got := parseBillingCents("1.234", 9); got != 9 {
		t.Fatalf("parseBillingCents too precise = %d, want 9", got)
	}
	if got := parseBillingCents(".5", 0); got != 50 {
		t.Fatalf("parseBillingCents cents-only = %d, want 50", got)
	}
	if got := parseBillingCents("bad", 9); got != 9 {
		t.Fatalf("parseBillingCents bad = %d, want 9", got)
	}
	if got := formatBillingTime(time.Time{}); got != "" {
		t.Fatalf("formatBillingTime zero = %q, want empty", got)
	}
}

func TestBillingTopupSettingsFromSpend(t *testing.T) {
	account := billing.Account{PlanCode: billing.PlanGrowth, PerRunMaxCredits: 24}
	got := billingTopupSettingsFromSpend(account, true, 2000)

	if !got.AutoTopupEnabled {
		t.Fatal("AutoTopupEnabled = false, want true")
	}
	if got.TriggerThreshold != 24 {
		t.Fatalf("TriggerThreshold = %d, want 24", got.TriggerThreshold)
	}
	if got.TargetBalance != 124 {
		t.Fatalf("TargetBalance = %d, want 124", got.TargetBalance)
	}
	// Growth top-ups are $20, so a $20 spend cap permits 1 whole unit.
	if got.MonthlyMaxUnits != 1 {
		t.Fatalf("MonthlyMaxUnits = %d, want 1", got.MonthlyMaxUnits)
	}
	if got.MonthlyMaxCents != 2000 {
		t.Fatalf("MonthlyMaxCents = %d, want 2000", got.MonthlyMaxCents)
	}

	got = billingTopupSettingsFromSpend(
		billing.Account{PlanCode: billing.PlanStarter, PerRunMaxCredits: 4},
		true,
		2000,
	)
	if got.MonthlyMaxUnits != 0 {
		t.Fatalf("starter MonthlyMaxUnits = %d, want 0", got.MonthlyMaxUnits)
	}
}

func TestBillingTopupSettingsFromSpendAdditionalCases(t *testing.T) {
	settings := billingTopupSettingsFromSpend(billing.Account{
		PlanCode:         billing.PlanTeam,
		PerRunMaxCredits: 12,
	}, true, 1800)
	if !settings.AutoTopupEnabled {
		t.Fatal("AutoTopupEnabled = false, want true")
	}
	if settings.TriggerThreshold != 12 || settings.TargetBalance != 112 {
		t.Fatalf("threshold/target = %d/%d, want 12/112", settings.TriggerThreshold, settings.TargetBalance)
	}
	if settings.MonthlyMaxUnits != 0 || settings.MonthlyMaxCents != 1800 {
		t.Fatalf("monthly cap = %d/%d, want 0/1800", settings.MonthlyMaxUnits, settings.MonthlyMaxCents)
	}

	settings = billingTopupSettingsFromSpend(billing.Account{PlanCode: "unknown"}, false, -10)
	if settings.MonthlyMaxCents != 0 || settings.MonthlyMaxUnits != 0 {
		t.Fatalf("negative monthly cap = %d/%d, want 0/0", settings.MonthlyMaxCents, settings.MonthlyMaxUnits)
	}
}

func TestBillingPlanSwitchConfirmation(t *testing.T) {
	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)

	title, msg := billingPlanSwitchConfirmation(team, true, growth, false, true, "Jun 18, 2026")
	if title != "Switch to Growth?" {
		t.Fatalf("upgrade title = %q, want Switch to Growth?", title)
	}
	if !strings.Contains(msg, "takes effect immediately") || !strings.Contains(msg, "prorated") {
		t.Fatalf("upgrade message = %q, want immediate prorated billing copy", msg)
	}

	title, msg = billingPlanSwitchConfirmation(growth, true, team, false, true, "Jun 18, 2026")
	if title != "Switch to Studio?" {
		t.Fatalf("downgrade title = %q, want Switch to Studio?", title)
	}
	if !strings.Contains(msg, "Jun 18, 2026") || !strings.Contains(msg, "no immediate charge") {
		t.Fatalf("downgrade message = %q, want next-cycle no-charge copy", msg)
	}

	_, msg = billingPlanSwitchConfirmation(growth, true, team, true, true, "Jun 18, 2026")
	if msg != "" {
		t.Fatalf("current-plan confirmation = %q, want empty", msg)
	}
}

func TestBillingPendingPlanChange(t *testing.T) {
	effective := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	code, label, when, ok := billingPendingPlanChange(billing.Account{
		PlanCode:               billing.PlanBusiness,
		PendingPlanCode:        billing.PlanTeam,
		PendingPlanEffectiveAt: effective,
	})
	if !ok || code != billing.PlanTeam || label != "Studio" || when != "Jun 18, 2026" {
		t.Fatalf("pending plan = (%q, %q, %q, %v), want Studio on Jun 18, 2026", code, label, when, ok)
	}

	code, label, when, ok = billingPendingPlanChange(billing.Account{
		PlanCode:               billing.PlanGrowth,
		PendingPlanCode:        billing.PlanFree,
		PendingPlanEffectiveAt: effective,
	})
	if !ok || code != billing.PlanFree || label != "Free" || when != "Jun 18, 2026" {
		t.Fatalf("pending cancellation = (%q, %q, %q, %v), want Free on Jun 18, 2026", code, label, when, ok)
	}

	code, label, when, ok = billingPendingPlanChange(billing.Account{
		PlanCode:        billing.PlanTeam,
		PendingPlanCode: billing.PlanTeam,
	})
	if ok {
		t.Fatalf("pending plan matching current plan should not render, got (%q, %q, %q)", code, label, when)
	}
}

func TestParseBillingCents(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 42},
		{"$27", 2700},
		{"12.50", 1250},
		{".99", 99},
		{"1,234.05", 123405},
		{"12.345", 42},
		{"-1", 42},
		{"abc", 42},
	}
	for _, tc := range cases {
		if got := parseBillingCents(tc.raw, 42); got != tc.want {
			t.Errorf("parseBillingCents(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestRepoBillingViewDataDisabled(t *testing.T) {
	b := &Bot{}
	settings, allowed := b.repoBillingViewData(t.Context(), b.billingSvc(), "org_1")
	if len(settings) != 0 {
		t.Fatalf("settings len = %d, want 0", len(settings))
	}
	if len(allowed) != 1 || allowed[0].Code != billing.FlavorStandard {
		t.Fatalf("allowed = %+v, want standard only", allowed)
	}
}

func TestBillingHandlersRejectBeforeExternalDependencies(t *testing.T) {
	b := &Bot{log: discardLogger()}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		call   func(http.ResponseWriter, *http.Request)
		want   int
	}{
		{name: "checkout method", method: http.MethodGet, path: "/billing/checkout", call: b.billingCheckoutHandler, want: http.StatusMethodNotAllowed},
		{name: "topup method", method: http.MethodGet, path: "/billing/topup", call: b.billingTopupHandler, want: http.StatusMethodNotAllowed},
		{name: "portal method", method: http.MethodPut, path: "/billing/portal", call: b.billingPortalHandler, want: http.StatusMethodNotAllowed},
		{name: "topup settings method", method: http.MethodGet, path: "/billing/topup-settings", call: b.billingTopupSettingsHandler, want: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			tc.call(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		path string
		call func(http.ResponseWriter, *http.Request)
	}{
		{name: "checkout forbidden", path: "/billing/checkout", call: b.billingCheckoutHandler},
		{name: "topup forbidden", path: "/billing/topup", call: b.billingTopupHandler},
		{name: "topup settings forbidden", path: "/billing/topup-settings", call: b.billingTopupSettingsHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			rr := httptest.NewRecorder()
			tc.call(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
			}
		})
	}

	for _, tc := range []struct {
		name string
		path string
		call func(http.ResponseWriter, *http.Request)
	}{
		{name: "checkout no billing", path: "/billing/checkout", call: b.billingCheckoutHandler},
		{name: "topup no billing", path: "/billing/topup", call: b.billingTopupHandler},
		{name: "topup settings no billing", path: "/billing/topup-settings", call: b.billingTopupSettingsHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(""))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://example.com")
			rr := httptest.NewRecorder()
			serveWithBypassAuth(t, "admin", rr, req, tc.call)
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
			}
		})
	}

	t.Run("portal no billing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/billing/portal", nil)
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingPortalHandler)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
		}
	})

	t.Run("portal forbidden", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/billing/portal", nil)
		rr := httptest.NewRecorder()
		b.billingPortalHandler(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})
}

func TestBillingHandlersValidateConfigBeforeDatabaseUse(t *testing.T) {
	b := &Bot{
		log:     discardLogger(),
		billing: billing.NewService(billing.NewStore(&db.Store{}), nil),
	}

	t.Run("checkout stripe unconfigured", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingCheckoutHandler)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("checkout missing origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingCheckoutHandler)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})

	t.Run("checkout unknown plan", func(t *testing.T) {
		b.cfg = Config{
			StripeSecretKey:           "sk_test",
			StripeSubscriptionPriceID: "price_team",
			StripeTopupPriceID:        "price_topup",
		}
		req := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader("plan=unknown"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingCheckoutHandler)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("topup stripe unconfigured", func(t *testing.T) {
		b.cfg = Config{}
		req := httptest.NewRequest(http.MethodPost, "/billing/topup", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingTopupHandler)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("topup missing origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/billing/topup", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingTopupHandler)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})

	t.Run("portal post missing origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/billing/portal", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingPortalHandler)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})

	t.Run("topup invalid quantity", func(t *testing.T) {
		b.cfg = Config{
			StripeSecretKey:           "sk_test",
			StripeSubscriptionPriceID: "price_team",
			StripeTopupPriceID:        "price_topup",
		}
		req := httptest.NewRequest(http.MethodPost, "/billing/topup", strings.NewReader("quantity=0"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		rr := httptest.NewRecorder()
		serveWithBypassAuth(t, "admin", rr, req, b.billingTopupHandler)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})
}

func TestBillingTopupHandlerRejectsFreePlanBeforeStripeCustomer(t *testing.T) {
	billingDB := &billingTopupEligibilityDB{account: billing.Account{
		OrgID:               "org_1",
		PlanCode:            billing.PlanFree,
		Status:              billing.PlanFree,
		IncludedCredits:     billing.FreeIncludedCredits,
		IncludedCreditsUsed: 0,
		MaxFlavor:           billing.FlavorStandard,
		PerRunMaxCredits:    billing.FreePerRunMaxCredits,
	}}
	b := &Bot{
		log: discardLogger(),
		cfg: Config{
			StripeSecretKey:           "sk_test",
			StripeSubscriptionPriceID: "price_team",
			StripeTopupPriceID:        "price_topup",
		},
		billing: billing.NewService(
			billing.NewStore(&db.Store{Queries: sqlc.New(billingDB)}),
			nil,
		),
	}
	req := httptest.NewRequest(http.MethodPost, "/billing/topup", strings.NewReader("quantity=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	serveWithBypassAuth(t, "admin", rr, req, b.billingTopupHandler)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%q, want %d", rr.Code, rr.Body.String(), http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "top-ups require a paid plan") {
		t.Fatalf("body = %q, want paid plan error", rr.Body.String())
	}
	if billingDB.setStripeCustomerCalls != 0 {
		t.Fatalf("set stripe customer calls = %d, want 0", billingDB.setStripeCustomerCalls)
	}
}

func TestBillingCheckoutHandlerRejectsCompedOrgBeforeStripeCustomer(t *testing.T) {
	billingDB := &billingTopupEligibilityDB{account: billing.Account{
		OrgID:            "org_1",
		PlanCode:         billing.PlanBusiness,
		Status:           "comped",
		IncludedCredits:  3600,
		MaxFlavor:        billing.FlavorPlus,
		PerRunMaxCredits: 48,
		BillingExempt:    true,
	}}
	b := &Bot{
		log: discardLogger(),
		cfg: Config{
			StripeSecretKey:           "sk_test",
			StripeSubscriptionPriceID: "price_team",
			StripeTopupPriceID:        "price_topup",
		},
		billing: billing.NewService(
			billing.NewStore(&db.Store{Queries: sqlc.New(billingDB)}),
			nil,
		),
	}
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader("plan=team"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	serveWithBypassAuth(t, "admin", rr, req, b.billingCheckoutHandler)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%q, want %d", rr.Code, rr.Body.String(), http.StatusForbidden)
	}
	if !strings.Contains(rr.Body.String(), "subscriptions are managed externally") {
		t.Fatalf("body = %q, want comped subscription error", rr.Body.String())
	}
	if billingDB.setStripeCustomerCalls != 0 {
		t.Fatalf("set stripe customer calls = %d, want 0", billingDB.setStripeCustomerCalls)
	}
}

func serveWithBypassAuth(t *testing.T, role string, rr *httptest.ResponseRecorder, req *http.Request, h http.HandlerFunc) {
	t.Helper()
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_1",
		BypassEmail: "user@example.test",
		BypassOrg:   "org_1",
		BypassRole:  role,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	a.Middleware(http.HandlerFunc(h)).ServeHTTP(rr, req)
}

type billingTopupEligibilityDB struct {
	account                billing.Account
	setStripeCustomerCalls int
}

func (f *billingTopupEligibilityDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *billingTopupEligibilityDB) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	if strings.Contains(query, "ListBillingRunMetersByOrg") {
		return &workOSStringRows{}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

func (f *billingTopupEligibilityDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	switch {
	case strings.Contains(query, "EnsureBillingAccount"):
		return billingTopupAccountRow{account: f.account}
	case strings.Contains(query, "EnsureBillingTopupSettings"),
		strings.Contains(query, "ResetBillingTopupMonthlyUsage"):
		return billingTopupSettingsRow{orgID: f.account.OrgID}
	case strings.Contains(query, "UpdateBillingStripeCustomer"):
		f.setStripeCustomerCalls++
		return billingTopupAccountRow{account: f.account}
	default:
		return billingTopupErrRow{err: fmt.Errorf("unexpected query row: %s", query)}
	}
}

type billingTopupAccountRow struct {
	account billing.Account
}

func (r billingTopupAccountRow) Scan(dest ...any) error {
	values := []any{
		r.account.OrgID,
		r.account.StripeCustomerID,
		r.account.StripeSubscriptionID,
		r.account.PlanCode,
		r.account.Status,
		timestamptzValue(r.account.CurrentPeriodStart),
		timestamptzValue(r.account.CurrentPeriodEnd),
		int32(r.account.IncludedCredits),
		int32(r.account.IncludedCreditsUsed),
		int32(r.account.TopupCredits),
		r.account.MaxFlavor,
		int32(r.account.PerRunMaxCredits),
		r.account.BillingExempt,
		r.account.LastPaymentError,
		timestamptzValue(r.account.CreatedAt),
		timestamptzValue(r.account.UpdatedAt),
		r.account.PendingPlanCode,
		timestamptzValue(r.account.PendingPlanEffectiveAt),
	}
	return scanBillingTopupValues(dest, values)
}

type billingTopupSettingsRow struct {
	orgID string
}

func (r billingTopupSettingsRow) Scan(dest ...any) error {
	values := []any{
		r.orgID,
		false,
		int32(2),
		int32(10),
		int32(0),
		int32(0),
		"2026-05",
		pgtype.Timestamptz{},
		pgtype.Timestamptz{},
		int32(0),
		int32(0),
	}
	return scanBillingTopupValues(dest, values)
}

type billingTopupErrRow struct {
	err error
}

func (r billingTopupErrRow) Scan(...any) error {
	return r.err
}

func scanBillingTopupValues(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(values))
	}
	for i := range dest {
		if err := assignWorkOSScanValue(dest[i], values[i]); err != nil {
			return err
		}
	}
	return nil
}

func timestamptzValue(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}
