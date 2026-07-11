package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// These tests drive switchStripeSubscriptionPlan through its Stripe-network
// branches (upgrade, downgrade, resume-before-switch, schedule release/reuse,
// and error propagation) using the shared useStripeTestServer backend swap and
// an in-memory billing store fake. No production code changes; the goal is to
// cover the plan-switch orchestration that TestStripePlanSwitchEarlyReturns
// stops short of once a live Stripe client is required.

// stripeMockServer routes Stripe SDK calls by "METHOD /path". Each key holds a
// queue of JSON bodies consumed in order; a single-body queue serves every
// repeated call so idempotent lookups don't need duplicated fixtures. Keys in
// errs short-circuit to a 500 so error branches are exercised.
type stripeMockServer struct {
	routes map[string][]string
	errs   map[string]int
	calls  []string
}

func (m *stripeMockServer) handler(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	m.calls = append(m.calls, key)
	if status, ok := m.errs[key]; ok {
		http.Error(w, `{"error":{"message":"stripe failed"}}`, status)
		return
	}
	bodies := m.routes[key]
	if len(bodies) == 0 {
		http.Error(w, "unexpected Stripe request "+key, http.StatusInternalServerError)
		return
	}
	body := bodies[0]
	if len(bodies) > 1 {
		m.routes[key] = bodies[1:]
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

func (m *stripeMockServer) called(key string) int {
	n := 0
	for _, c := range m.calls {
		if c == key {
			n++
		}
	}
	return n
}

// subscriptionJSON builds a minimal Stripe subscription object with a single
// plan item at priceID. schedule is inlined verbatim when non-empty; extra is
// appended as trailing top-level fields (e.g. cancel_at_period_end).
func subscriptionJSON(priceID, schedule, extra string) string {
	body := `{"id":"sub_1","object":"subscription","status":"active",` +
		`"customer":{"id":"cus_1","object":"customer"},`
	if schedule != "" {
		body += `"schedule":` + schedule + `,`
	}
	if extra != "" {
		body += extra + `,`
	}
	body += `"items":{"object":"list","data":[{"id":"si_1","object":"subscription_item",` +
		`"price":{"id":"` + priceID + `","object":"price"},"quantity":1,` +
		`"current_period_start":1780272000,"current_period_end":1782864000}]}}`
	return body
}

func scheduleJSON(id, status string) string {
	return `{"id":"` + id + `","object":"subscription_schedule","status":"` + status + `"}`
}

// planSwitchBillingDB is a DBTX fake that satisfies the billing store writes
// switchStripeSubscriptionPlan issues: the account-mirror upsert on upgrade and
// the pending-plan set/clear rows. Reuses the account-row scanner already
// defined for the cancellation tests in this package.
type planSwitchBillingDB struct {
	pendingPlanCalls []stripePendingPlanCall
	mirrorCalls      int
	mirrorErr        bool
	setErr           bool
	clearErr         bool
}

func (f *planSwitchBillingDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *planSwitchBillingDB) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected query: %s", query)
}

func (f *planSwitchBillingDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	switch {
	case strings.Contains(query, "UpsertBillingAccountMirror"):
		f.mirrorCalls++
		if f.mirrorErr {
			return stripeErrRow{err: errors.New("mirror boom")}
		}
		return stripeBillingAccountRow{orgID: "org_1"}
	case strings.Contains(query, "SetBillingPendingPlanChange"):
		if len(args) != 3 {
			return stripeErrRow{err: fmt.Errorf("set pending plan args = %d, want 3", len(args))}
		}
		orgID, _ := args[0].(string)
		planCode, _ := args[1].(string)
		effectiveAt, ok := args[2].(pgtype.Timestamptz)
		if !ok {
			return stripeErrRow{err: fmt.Errorf("set pending effective at arg = %T, want pgtype.Timestamptz", args[2])}
		}
		f.pendingPlanCalls = append(f.pendingPlanCalls, stripePendingPlanCall{
			kind:        "set",
			orgID:       orgID,
			planCode:    planCode,
			effectiveAt: effectiveAt.Time,
		})
		if f.setErr {
			return stripeErrRow{err: errors.New("set pending boom")}
		}
		return stripeBillingAccountRow{orgID: orgID, pendingPlanCode: planCode, pendingPlanEffectiveAt: effectiveAt}
	case strings.Contains(query, "ClearBillingPendingPlanChange"):
		orgID, _ := args[0].(string)
		f.pendingPlanCalls = append(f.pendingPlanCalls, stripePendingPlanCall{kind: "clear", orgID: orgID})
		if f.clearErr {
			return stripeErrRow{err: errors.New("clear pending boom")}
		}
		return stripeBillingAccountRow{orgID: orgID}
	default:
		return stripeErrRow{err: fmt.Errorf("unexpected query row: %s", query)}
	}
}

func newPlanSwitchBillingTestBot(cfg Config, billingDB *planSwitchBillingDB) *Bot {
	return &Bot{
		cfg: cfg,
		log: discardLogger(),
		billing: billing.NewService(
			billing.NewStore(&db.Store{Queries: sqlc.New(billingDB)}),
			nil,
		),
	}
}

func planSwitchTestConfig() Config {
	return Config{
		StripeSecretKey: "sk_test",
		StripeSubscriptionPriceIDs: map[string]string{
			billing.PlanBuilder: "price_builder",
			billing.PlanGrowth:  "price_growth",
		},
	}
}

func (m *stripeMockServer) install(t *testing.T) func() {
	t.Helper()
	if m.routes == nil {
		m.routes = map[string][]string{}
	}
	if m.errs == nil {
		m.errs = map[string]int{}
	}
	return useStripeTestServer(t, m.handler)
}

func TestSwitchStripeSubscriptionPlanImmediateUpgrade(t *testing.T) {
	mock := &stripeMockServer{routes: map[string][]string{
		"GET /v1/subscriptions/sub_1":  {subscriptionJSON("price_builder", "", "")},
		"POST /v1/subscriptions/sub_1": {subscriptionJSON("price_growth", "", "")},
	}}
	defer mock.install(t)()

	billingDB := &planSwitchBillingDB{}
	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), billingDB)
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)

	result, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		StripeCustomerID:     "cus_1",
		PlanCode:             billing.PlanBuilder,
	}, growth, "price_growth")
	if err != nil {
		t.Fatalf("switch error: %v", err)
	}
	if result != stripePlanSwitchImmediate {
		t.Fatalf("result = %q, want %q", result, stripePlanSwitchImmediate)
	}
	if mock.called("POST /v1/subscriptions/sub_1") != 1 {
		t.Fatalf("subscription update calls = %d, want 1", mock.called("POST /v1/subscriptions/sub_1"))
	}
	if billingDB.mirrorCalls != 1 {
		t.Fatalf("account mirror upserts = %d, want 1", billingDB.mirrorCalls)
	}
	if len(billingDB.pendingPlanCalls) != 1 || billingDB.pendingPlanCalls[0].kind != "clear" {
		t.Fatalf("pending plan calls = %+v, want a single clear", billingDB.pendingPlanCalls)
	}
}

func TestSwitchStripeSubscriptionPlanUpgradeResumesAndReleases(t *testing.T) {
	mock := &stripeMockServer{routes: map[string][]string{
		// Subscription is pending cancellation with an active schedule.
		"GET /v1/subscriptions/sub_1": {subscriptionJSON("price_builder",
			scheduleJSON("sub_sched_1", "active"), `"cancel_at_period_end":true`)},
		// First POST resumes (still builder + schedule), second applies the upgrade.
		"POST /v1/subscriptions/sub_1": {
			subscriptionJSON("price_builder", scheduleJSON("sub_sched_1", "active"), ""),
			subscriptionJSON("price_growth", "", ""),
		},
		"POST /v1/subscription_schedules/sub_sched_1/release": {scheduleJSON("sub_sched_1", "released")},
	}}
	defer mock.install(t)()

	billingDB := &planSwitchBillingDB{}
	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), billingDB)
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)

	result, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanBuilder,
	}, growth, "price_growth")
	if err != nil {
		t.Fatalf("switch error: %v", err)
	}
	if result != stripePlanSwitchImmediate {
		t.Fatalf("result = %q, want %q", result, stripePlanSwitchImmediate)
	}
	if mock.called("POST /v1/subscriptions/sub_1") != 2 {
		t.Fatalf("subscription update calls = %d, want 2 (resume + upgrade)", mock.called("POST /v1/subscriptions/sub_1"))
	}
	if mock.called("POST /v1/subscription_schedules/sub_sched_1/release") != 1 {
		t.Fatal("expected pending schedule to be released before upgrade")
	}
}

func TestSwitchStripeSubscriptionPlanScheduledDowngrade(t *testing.T) {
	mock := &stripeMockServer{routes: map[string][]string{
		"GET /v1/subscriptions/sub_1":                   {subscriptionJSON("price_growth", "", "")},
		"POST /v1/subscription_schedules":               {scheduleJSON("sub_sched_new", "active")},
		"POST /v1/subscription_schedules/sub_sched_new": {scheduleJSON("sub_sched_new", "active")},
	}}
	defer mock.install(t)()

	billingDB := &planSwitchBillingDB{}
	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), billingDB)
	builder, _ := billing.PaidPlanByCode(billing.PlanBuilder)

	result, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanGrowth,
	}, builder, "price_builder")
	if err != nil {
		t.Fatalf("switch error: %v", err)
	}
	if result != stripePlanSwitchScheduled {
		t.Fatalf("result = %q, want %q", result, stripePlanSwitchScheduled)
	}
	if mock.called("POST /v1/subscription_schedules") != 1 {
		t.Fatal("expected a subscription schedule to be created")
	}
	if len(billingDB.pendingPlanCalls) != 1 || billingDB.pendingPlanCalls[0].kind != "set" ||
		billingDB.pendingPlanCalls[0].planCode != billing.PlanBuilder {
		t.Fatalf("pending plan calls = %+v, want a single set to builder", billingDB.pendingPlanCalls)
	}
}

func TestSwitchStripeSubscriptionPlanDowngradeReusesSchedule(t *testing.T) {
	mock := &stripeMockServer{routes: map[string][]string{
		// Schedule present but status blank forces a schedule Retrieve, which
		// reports it active/updatable so Create is skipped and Update reused.
		"GET /v1/subscriptions/sub_1":                 {subscriptionJSON("price_growth", scheduleJSON("sub_sched_9", ""), "")},
		"GET /v1/subscription_schedules/sub_sched_9":  {scheduleJSON("sub_sched_9", "active")},
		"POST /v1/subscription_schedules/sub_sched_9": {scheduleJSON("sub_sched_9", "active")},
	}}
	defer mock.install(t)()

	billingDB := &planSwitchBillingDB{}
	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), billingDB)
	builder, _ := billing.PaidPlanByCode(billing.PlanBuilder)

	result, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanGrowth,
	}, builder, "price_builder")
	if err != nil {
		t.Fatalf("switch error: %v", err)
	}
	if result != stripePlanSwitchScheduled {
		t.Fatalf("result = %q, want %q", result, stripePlanSwitchScheduled)
	}
	if mock.called("POST /v1/subscription_schedules") != 0 {
		t.Fatal("existing schedule should be reused, not recreated")
	}
	if mock.called("GET /v1/subscription_schedules/sub_sched_9") != 1 {
		t.Fatal("blank schedule status should trigger a schedule retrieve")
	}
}

func TestSwitchStripeSubscriptionPlanRetrieveError(t *testing.T) {
	mock := &stripeMockServer{errs: map[string]int{
		"GET /v1/subscriptions/sub_1": http.StatusInternalServerError,
	}}
	defer mock.install(t)()

	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{})
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)

	_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanBuilder,
	}, growth, "price_growth")
	if err == nil || !strings.Contains(err.Error(), "retrieve subscription") {
		t.Fatalf("err = %v, want retrieve subscription error", err)
	}
}

func TestSwitchStripeSubscriptionPlanUpgradeUpdateError(t *testing.T) {
	mock := &stripeMockServer{
		routes: map[string][]string{
			"GET /v1/subscriptions/sub_1": {subscriptionJSON("price_builder", "", "")},
		},
		errs: map[string]int{
			"POST /v1/subscriptions/sub_1": http.StatusInternalServerError,
		},
	}
	defer mock.install(t)()

	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{})
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)

	_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanBuilder,
	}, growth, "price_growth")
	if err == nil || !strings.Contains(err.Error(), "update subscription") {
		t.Fatalf("err = %v, want update subscription error", err)
	}
}

func TestSwitchStripeSubscriptionPlanDowngradeCreateError(t *testing.T) {
	mock := &stripeMockServer{
		routes: map[string][]string{
			"GET /v1/subscriptions/sub_1": {subscriptionJSON("price_growth", "", "")},
		},
		errs: map[string]int{
			"POST /v1/subscription_schedules": http.StatusInternalServerError,
		},
	}
	defer mock.install(t)()

	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{})
	builder, _ := billing.PaidPlanByCode(billing.PlanBuilder)

	_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanGrowth,
	}, builder, "price_builder")
	if err == nil || !strings.Contains(err.Error(), "create subscription schedule") {
		t.Fatalf("err = %v, want create subscription schedule error", err)
	}
}

func TestSwitchStripeSubscriptionPlanResumeError(t *testing.T) {
	mock := &stripeMockServer{
		routes: map[string][]string{
			"GET /v1/subscriptions/sub_1": {subscriptionJSON("price_builder", "", `"cancel_at":1782864000`)},
		},
		errs: map[string]int{
			"POST /v1/subscriptions/sub_1": http.StatusInternalServerError,
		},
	}
	defer mock.install(t)()

	b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{})
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)

	_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanBuilder,
	}, growth, "price_growth")
	if err == nil || !strings.Contains(err.Error(), "resume subscription before plan switch") {
		t.Fatalf("err = %v, want resume-before-switch error", err)
	}
}

func TestSwitchStripeSubscriptionPlanUpgradeMirrorAndClearErrors(t *testing.T) {
	routes := func() map[string][]string {
		return map[string][]string{
			"GET /v1/subscriptions/sub_1":  {subscriptionJSON("price_builder", "", "")},
			"POST /v1/subscriptions/sub_1": {subscriptionJSON("price_growth", "", "")},
		}
	}
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)
	acct := billing.Account{StripeSubscriptionID: "sub_1", PlanCode: billing.PlanBuilder}

	t.Run("mirror error", func(t *testing.T) {
		mock := &stripeMockServer{routes: routes()}
		defer mock.install(t)()
		b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{mirrorErr: true})
		_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", acct, growth, "price_growth")
		if err == nil || !strings.Contains(err.Error(), "mirror switched subscription") {
			t.Fatalf("err = %v, want mirror error", err)
		}
	})

	t.Run("clear pending error", func(t *testing.T) {
		mock := &stripeMockServer{routes: routes()}
		defer mock.install(t)()
		b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{clearErr: true})
		_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", acct, growth, "price_growth")
		if err == nil || !strings.Contains(err.Error(), "clear pending plan change") {
			t.Fatalf("err = %v, want clear pending error", err)
		}
	})
}

func TestSwitchStripeSubscriptionPlanDowngradeUpdateAndPendingErrors(t *testing.T) {
	builder, _ := billing.PaidPlanByCode(billing.PlanBuilder)
	acct := billing.Account{StripeSubscriptionID: "sub_1", PlanCode: billing.PlanGrowth}

	t.Run("schedule update error", func(t *testing.T) {
		mock := &stripeMockServer{
			routes: map[string][]string{
				"GET /v1/subscriptions/sub_1":     {subscriptionJSON("price_growth", "", "")},
				"POST /v1/subscription_schedules": {scheduleJSON("sub_sched_new", "active")},
			},
			errs: map[string]int{
				"POST /v1/subscription_schedules/sub_sched_new": http.StatusInternalServerError,
			},
		}
		defer mock.install(t)()
		b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{})
		_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", acct, builder, "price_builder")
		if err == nil || !strings.Contains(err.Error(), "schedule subscription downgrade") {
			t.Fatalf("err = %v, want schedule downgrade error", err)
		}
	})

	t.Run("record pending error", func(t *testing.T) {
		mock := &stripeMockServer{routes: map[string][]string{
			"GET /v1/subscriptions/sub_1":                   {subscriptionJSON("price_growth", "", "")},
			"POST /v1/subscription_schedules":               {scheduleJSON("sub_sched_new", "active")},
			"POST /v1/subscription_schedules/sub_sched_new": {scheduleJSON("sub_sched_new", "active")},
		}}
		defer mock.install(t)()
		b := newPlanSwitchBillingTestBot(planSwitchTestConfig(), &planSwitchBillingDB{setErr: true})
		_, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", acct, builder, "price_builder")
		if err == nil || !strings.Contains(err.Error(), "record pending plan change") {
			t.Fatalf("err = %v, want record pending error", err)
		}
	})
}
