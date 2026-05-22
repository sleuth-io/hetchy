package bot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"

	"github.com/hetchyhq/hetchy/internal/billing"
)

func TestStripeWebhookHandlerValidation(t *testing.T) {
	b := &Bot{cfg: Config{StripeWebhookSecret: "whsec_test"}, log: discardLogger()}

	req := httptest.NewRequest(http.MethodGet, "/stripe/webhook", nil)
	rr := httptest.NewRecorder()
	b.stripeWebhookHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}

	b.cfg.StripeWebhookSecret = ""
	req = httptest.NewRequest(http.MethodPost, "/stripe/webhook", strings.NewReader("{}"))
	rr = httptest.NewRecorder()
	b.stripeWebhookHandler(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}

	b.cfg.StripeWebhookSecret = "whsec_test"
	req = httptest.NewRequest(http.MethodPost, "/stripe/webhook", strings.NewReader("{}"))
	req.Header.Set("Stripe-Signature", "t=1,v1=bad")
	rr = httptest.NewRecorder()
	b.stripeWebhookHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad signature status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestStripeWebhookHandlerAcceptsSignedDisabledBilling(t *testing.T) {
	const secret = "whsec_test"
	payload := []byte(`{"id":"evt_1","object":"event","type":"customer.subscription.updated","data":{"object":{"id":"sub_1","metadata":{"org_id":"org_1"}}}}`)
	b := &Bot{
		cfg:     Config{StripeWebhookSecret: secret},
		log:     discardLogger(),
		billing: billing.NewService(nil, nil),
	}
	req := httptest.NewRequest(http.MethodPost, "/stripe/webhook", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", stripeTestSignature(payload, secret))
	rr := httptest.NewRecorder()

	b.stripeWebhookHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestHandleStripeEventDispatchesNoOpBillingPaths(t *testing.T) {
	b := &Bot{billing: billing.NewService(nil, nil)}

	events := []stripe.Event{
		stripeTestEvent("unhandled.event", `{"id":"obj_1"}`),
		stripeTestEvent("checkout.session.completed", `{
			"id":"cs_topup",
			"mode":"payment",
			"payment_status":"paid",
			"customer":{"id":"cus_1"},
			"client_reference_id":"org_1",
			"metadata":{"kind":"topup","quantity":"3"}
		}`),
		stripeTestEvent("checkout.session.completed", `{
			"id":"cs_sub",
			"subscription":"sub_1",
			"customer":"cus_1",
			"client_reference_id":"org_1",
			"metadata":{"kind":"subscription","plan_code":"team"}
		}`),
		stripeTestEvent("customer.subscription.updated", `{
			"id":"sub_1",
			"customer":"cus_1",
			"status":"active",
			"metadata":{"org_id":"org_1","plan_code":"growth"},
			"items":{"data":[{"current_period_start":1780272000,"current_period_end":1782864000}]}
		}`),
		stripeTestEvent("customer.subscription.deleted", `{
			"id":"sub_1",
			"customer":"cus_1",
			"status":"canceled",
			"metadata":{"org_id":"org_1","plan_code":"team"},
			"current_period_start":1780272000,
			"current_period_end":1782864000
		}`),
		stripeTestEvent("invoice.paid", `{
			"id":"in_1",
			"customer":"cus_1",
			"metadata":{"kind":"topup","org_id":"org_1","credits":"10"}
		}`),
		stripeTestEvent("invoice.payment_succeeded", `{
			"id":"in_2",
			"customer":{"id":"cus_1"},
			"metadata":{"kind":"topup","credits":"10"}
		}`),
		stripeTestEvent("invoice.paid", `{
			"id":"in_3",
			"metadata":{"kind":"subscription","org_id":"org_1"}
		}`),
		stripeTestEvent("invoice.payment_failed", `{
			"id":"in_4",
			"customer":"cus_1",
			"metadata":{"org_id":"org_1"}
		}`),
	}

	for _, event := range events {
		t.Run(string(event.Type), func(t *testing.T) {
			if err := b.handleStripeEvent(t.Context(), event); err != nil {
				t.Fatalf("handleStripeEvent returned error: %v", err)
			}
		})
	}
}

func TestDecodeStripeEventObjectRejectsEmptyAndInvalidData(t *testing.T) {
	var inv stripeInvoiceObject
	if err := decodeStripeEventObject(stripe.Event{Type: "invoice.paid"}, &inv); err == nil {
		t.Fatal("decodeStripeEventObject with nil data returned nil error")
	}
	if err := decodeStripeEventObject(stripeTestEvent("invoice.paid", `{`), &inv); err == nil {
		t.Fatal("decodeStripeEventObject with invalid JSON returned nil error")
	}
}

func TestStripeIDUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "string", raw: `"cus_1"`, want: "cus_1"},
		{name: "object", raw: `{"id":"cus_2","object":"customer"}`, want: "cus_2"},
		{name: "null", raw: `null`, want: ""},
		{name: "empty", raw: `   `, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var id stripeID
			if err := id.UnmarshalJSON([]byte(tc.raw)); err != nil {
				t.Fatalf("UnmarshalJSON returned error: %v", err)
			}
			if got := id.String(); got != tc.want {
				t.Fatalf("String = %q, want %q", got, tc.want)
			}
		})
	}

	var id stripeID
	if err := id.UnmarshalJSON([]byte(`{`)); err == nil {
		t.Fatal("UnmarshalJSON with invalid object returned nil error")
	}
}

func TestStripeOrgIDAndSubscriptionPeriod(t *testing.T) {
	if got := stripeOrgID(map[string]string{"org_id": " org_meta "}, "fallback"); got != "org_meta" {
		t.Fatalf("stripeOrgID metadata = %q, want org_meta", got)
	}
	if got := stripeOrgID(map[string]string{"org_id": " "}, " fallback "); got != "fallback" {
		t.Fatalf("stripeOrgID fallback = %q, want fallback", got)
	}

	sub := stripeSubscriptionObject{}
	sub.Items.Data = append(sub.Items.Data, struct {
		CurrentPeriodStart int64 `json:"current_period_start"`
		CurrentPeriodEnd   int64 `json:"current_period_end"`
	}{
		CurrentPeriodStart: 1780272000,
		CurrentPeriodEnd:   1782864000,
	})
	start, end := sub.Period()
	if start.Unix() != 1780272000 || end.Unix() != 1782864000 {
		t.Fatalf("Period = %v-%v, want item period", start, end)
	}

	cancelAt := stripeSubscriptionObject{CancelAt: 1782165951}.CancellationEffectiveAt()
	if cancelAt.Unix() != 1782165951 {
		t.Fatalf("CancellationEffectiveAt cancel_at = %v, want unix 1782165951", cancelAt)
	}
	cancelAt = stripeSubscriptionObject{
		CancelAtPeriodEnd: true,
		CurrentPeriodEnd:  1782864000,
	}.CancellationEffectiveAt()
	if cancelAt.Unix() != 1782864000 {
		t.Fatalf("CancellationEffectiveAt period end = %v, want unix 1782864000", cancelAt)
	}
	if got := (stripeSubscriptionObject{}).CancellationEffectiveAt(); !got.IsZero() {
		t.Fatalf("CancellationEffectiveAt without cancellation = %v, want zero", got)
	}
}

func TestStripeCheckoutTopupCreditsUsesMetadataWithoutLookup(t *testing.T) {
	b := &Bot{}
	got, err := b.stripeCheckoutTopupCredits(t.Context(), stripeCheckoutSessionObject{
		Metadata: map[string]string{"quantity": "4"},
	})
	if err != nil {
		t.Fatalf("stripeCheckoutTopupCredits returned error: %v", err)
	}
	if got != 400 {
		t.Fatalf("stripeCheckoutTopupCredits = %d, want 400", got)
	}
}

func TestStripeCheckoutTopupCreditsFetchesLineItems(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/checkout/sessions/cs_1/line_items" {
			http.Error(w, "unexpected Stripe request "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"li_1","object":"item","quantity":2}],"has_more":false}`)
	})
	defer restore()

	b := &Bot{cfg: Config{StripeSecretKey: "sk_test"}}
	got, err := b.stripeCheckoutTopupCredits(t.Context(), stripeCheckoutSessionObject{ID: "cs_1"})
	if err != nil {
		t.Fatalf("stripeCheckoutTopupCredits returned error: %v", err)
	}
	if got != 200 {
		t.Fatalf("stripeCheckoutTopupCredits = %d, want 200", got)
	}
}

func TestStripeCheckoutTopupCreditsRejectsEmptyLineItems(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[],"has_more":false}`)
	})
	defer restore()

	b := &Bot{cfg: Config{StripeSecretKey: "sk_test"}}
	if _, err := b.stripeCheckoutTopupCredits(t.Context(), stripeCheckoutSessionObject{ID: "cs_empty"}); err == nil {
		t.Fatal("stripeCheckoutTopupCredits with empty line items returned nil error")
	}
}

func TestStripeCheckoutLineItemQuantity(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/checkout/sessions/cs_1/line_items" {
			http.Error(w, "unexpected Stripe request "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"li_1","object":"item","quantity":2},{"id":"li_2","object":"item","quantity":3}],"has_more":false}`)
	})
	defer restore()

	b := &Bot{cfg: Config{StripeSecretKey: "sk_test"}}
	got, err := b.stripeCheckoutLineItemQuantity(t.Context(), "cs_1")
	if err != nil {
		t.Fatalf("stripeCheckoutLineItemQuantity returned error: %v", err)
	}
	if got != 5 {
		t.Fatalf("stripeCheckoutLineItemQuantity = %d, want 5", got)
	}
}

func TestStripeCheckoutLineItemQuantityPropagatesStripeError(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"stripe failed"}}`, http.StatusInternalServerError)
	})
	defer restore()

	b := &Bot{cfg: Config{StripeSecretKey: "sk_test"}}
	if _, err := b.stripeCheckoutLineItemQuantity(t.Context(), "cs_1"); err == nil {
		t.Fatal("stripeCheckoutLineItemQuantity returned nil error")
	}
}

func TestStartStripeSubscriptionCheckout(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
			http.Error(w, "unexpected Stripe request "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("metadata[app]"); got != "hetchy" {
			http.Error(w, "metadata app = "+got, http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("subscription_data[metadata][app]"); got != "hetchy" {
			http.Error(w, "subscription metadata app = "+got, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cs_1","object":"checkout.session","url":"https://stripe.example/checkout"}`)
	})
	defer restore()

	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	b := &Bot{cfg: Config{
		StripeSecretKey: "sk_test",
		StripeReturnTo:  "https://app.example.test",
	}, log: discardLogger()}
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	rr := httptest.NewRecorder()

	b.startStripeSubscriptionCheckout(rr, req, "org_1", "cus_1", team, "price_team")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusSeeOther, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "https://stripe.example/checkout" {
		t.Fatalf("Location = %q, want Stripe Checkout URL", got)
	}
}

func TestStartStripeSubscriptionCheckoutPropagatesStripeError(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"stripe failed"}}`, http.StatusInternalServerError)
	})
	defer restore()

	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	b := &Bot{cfg: Config{StripeSecretKey: "sk_test"}, log: discardLogger()}
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	rr := httptest.NewRecorder()

	b.startStripeSubscriptionCheckout(rr, req, "org_1", "cus_1", team, "price_team")

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
}

func TestEnsureStripeCustomerCreatesCustomer(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/customers" {
			http.Error(w, "unexpected Stripe request "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("metadata[app]"); got != "hetchy" {
			http.Error(w, "metadata app = "+got, http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("metadata[org_id]"); got != "org_1" {
			http.Error(w, "metadata org_id = "+got, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cus_new","object":"customer"}`)
	})
	defer restore()

	b := &Bot{
		cfg:     Config{StripeSecretKey: "sk_test"},
		billing: billing.NewService(nil, nil),
	}
	if _, err := b.ensureStripeCustomer(t.Context(), "org_1", "user@example.test"); err != nil {
		t.Fatalf("ensureStripeCustomer returned error: %v", err)
	}
}

func TestEnsureStripeCustomerPropagatesStripeError(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"stripe failed"}}`, http.StatusInternalServerError)
	})
	defer restore()

	b := &Bot{
		cfg:     Config{StripeSecretKey: "sk_test"},
		billing: billing.NewService(nil, nil),
	}
	if _, err := b.ensureStripeCustomer(t.Context(), "org_1", "user@example.test"); err == nil {
		t.Fatal("ensureStripeCustomer returned nil error")
	}
}

func TestStripeConfigAndPlanHelpers(t *testing.T) {
	if got := newStripeAutoTopupper(Config{}); got != nil {
		t.Fatalf("newStripeAutoTopupper without config = %#v, want nil", got)
	}
	if got := newStripeAutoTopupper(Config{
		StripeSecretKey: " sk_test ",
		StripeTopupPriceIDs: map[string]string{
			billing.DefaultPaidPlan().Code: " price_topup ",
		},
	}); got == nil {
		t.Fatal("newStripeAutoTopupper with config returned nil")
	}

	b := &Bot{cfg: Config{
		StripeSecretKey:            "sk_test",
		StripeSubscriptionPriceID:  "price_legacy_sub",
		StripeTopupPriceID:         "price_legacy_topup",
		StripeSubscriptionPriceIDs: map[string]string{"growth": " price_growth "},
	}}
	if !b.stripeConfigured() {
		t.Fatal("stripeConfigured = false, want true")
	}
	if got := b.stripeSubscriptionPriceID(" growth "); got != "price_growth" {
		t.Fatalf("stripeSubscriptionPriceID = %q, want price_growth", got)
	}
	plan, priceID, err := b.checkoutPlan("growth")
	if err != nil {
		t.Fatalf("checkoutPlan returned error: %v", err)
	}
	if plan.Code != billing.PlanGrowth || priceID != "price_growth" {
		t.Fatalf("checkoutPlan = %s/%s, want growth/price_growth", plan.Code, priceID)
	}
	if _, _, err := b.checkoutPlan("unknown"); err == nil {
		t.Fatal("checkoutPlan unknown plan returned nil error")
	}

	if got := metadataString(map[string]string{"x": " value "}, "x", "def"); got != "value" {
		t.Fatalf("metadataString = %q, want value", got)
	}
	if got := metadataString(map[string]string{"x": " "}, "x", "def"); got != "def" {
		t.Fatalf("metadataString blank = %q, want def", got)
	}
	if got := metadataInt(map[string]string{"n": "-1"}, "n", 7); got != 7 {
		t.Fatalf("metadataInt negative = %d, want 7", got)
	}
	if got := metadataInt(map[string]string{"n": "bad"}, "n", 7); got != 7 {
		t.Fatalf("metadataInt bad = %d, want 7", got)
	}
	if b.stripeClient() == nil {
		t.Fatal("stripeClient returned nil")
	}
	if got := unixTime(0); !got.IsZero() {
		t.Fatalf("unixTime(0) = %v, want zero", got)
	}
}

func TestStripePlanSwitchEarlyReturns(t *testing.T) {
	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)
	b := &Bot{}

	if _, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{PlanCode: billing.PlanTeam}, growth, "price_growth"); err == nil {
		t.Fatal("switch without subscription returned nil error")
	}
	if _, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             "unknown",
	}, growth, "price_growth"); err == nil {
		t.Fatal("switch with unknown current plan returned nil error")
	}
	result, err := b.switchStripeSubscriptionPlan(t.Context(), "org_1", billing.Account{
		StripeSubscriptionID: "sub_1",
		PlanCode:             billing.PlanTeam,
	}, team, "price_team")
	if err != nil {
		t.Fatalf("same-plan switch returned error: %v", err)
	}
	if result != stripePlanSwitchNoop {
		t.Fatalf("same-plan result = %q, want %q", result, stripePlanSwitchNoop)
	}
}

func TestStripeSubscriptionPlanItemErrorsAndPeriods(t *testing.T) {
	if _, _, err := stripeSubscriptionPlanItem(nil, nil); err == nil {
		t.Fatal("nil subscription returned nil error")
	}
	_, _, err := stripeSubscriptionPlanItem(&stripe.Subscription{Items: &stripe.SubscriptionItemList{Data: []*stripe.SubscriptionItem{
		{ID: "si_1", Price: &stripe.Price{ID: "price_a"}},
		{ID: "si_2", Price: &stripe.Price{ID: "price_b"}},
	}}}, map[string]string{"team": "price_team"})
	if err == nil {
		t.Fatal("multi-item subscription without known price returned nil error")
	}

	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	acct := billing.Account{CurrentPeriodStart: start, CurrentPeriodEnd: end}
	gotStart, gotEnd := stripeSubscriptionItemPeriod(acct, &stripe.SubscriptionItem{})
	if gotStart != start.Unix() || gotEnd != end.Unix() {
		t.Fatalf("stripeSubscriptionItemPeriod = %d-%d, want account period", gotStart, gotEnd)
	}
	periodStart, periodEnd := stripeSubscriptionPeriod(acct, &stripe.Subscription{}, nil)
	if !periodStart.Equal(start) || !periodEnd.Equal(end) {
		t.Fatalf("stripeSubscriptionPeriod = %v-%v, want account period", periodStart, periodEnd)
	}
}

func TestStripePlanSwitchHelpersWithoutStripeNetwork(t *testing.T) {
	b := &Bot{
		cfg: Config{StripeSubscriptionPriceIDs: map[string]string{
			billing.PlanTeam:   "price_team",
			billing.PlanGrowth: "price_growth",
		}},
		billing: billing.NewService(nil, nil),
	}
	priceIDs := b.stripeSubscriptionPriceIDsByPlan()
	if priceIDs[billing.PlanTeam] != "price_team" || priceIDs[billing.PlanGrowth] != "price_growth" {
		t.Fatalf("stripeSubscriptionPriceIDsByPlan = %v, want configured team and growth", priceIDs)
	}

	scheduleID, err := mutableStripeSubscriptionScheduleID(t.Context(), b.stripeClient(), &stripe.Subscription{
		Schedule: &stripe.SubscriptionSchedule{
			ID:     "sub_sched_1",
			Status: stripe.SubscriptionScheduleStatusActive,
		},
	})
	if err != nil {
		t.Fatalf("mutableStripeSubscriptionScheduleID returned error: %v", err)
	}
	if scheduleID != "sub_sched_1" {
		t.Fatalf("scheduleID = %q, want sub_sched_1", scheduleID)
	}
	scheduleID, err = mutableStripeSubscriptionScheduleID(t.Context(), b.stripeClient(), &stripe.Subscription{
		Schedule: &stripe.SubscriptionSchedule{
			ID:     "sub_sched_2",
			Status: stripe.SubscriptionScheduleStatusReleased,
		},
	})
	if err != nil {
		t.Fatalf("released schedule returned error: %v", err)
	}
	if scheduleID != "" {
		t.Fatalf("released scheduleID = %q, want empty", scheduleID)
	}

	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)
	err = b.mirrorStripeSubscription(t.Context(), "org_1", billing.Account{StripeCustomerID: "cus_old"}, &stripe.Subscription{
		ID:       "sub_1",
		Status:   stripe.SubscriptionStatusActive,
		Customer: &stripe.Customer{ID: "cus_new"},
		Items: &stripe.SubscriptionItemList{Data: []*stripe.SubscriptionItem{{
			ID:                 "si_1",
			Price:              &stripe.Price{ID: "price_growth"},
			CurrentPeriodStart: 1780272000,
			CurrentPeriodEnd:   1782864000,
		}}},
	}, growth)
	if err != nil {
		t.Fatalf("mirrorStripeSubscription returned error: %v", err)
	}
	if err := b.mirrorStripeSubscription(t.Context(), "org_1", billing.Account{}, nil, growth); err == nil {
		t.Fatal("mirrorStripeSubscription with nil subscription returned nil error")
	}
}

func stripeTestEvent(eventType, object string) stripe.Event {
	return stripe.Event{
		ID:   strings.ReplaceAll("evt_"+eventType, ".", "_"),
		Type: stripe.EventType(eventType),
		Data: &stripe.EventData{Raw: json.RawMessage(object)},
	}
}

func stripeTestSignature(payload []byte, secret string) string {
	ts := time.Now()
	sig := webhook.ComputeSignature(ts, payload, secret)
	return fmt.Sprintf("t=%d,v1=%x", ts.Unix(), sig)
}

func useStripeTestServer(t *testing.T, h http.HandlerFunc) func() {
	t.Helper()
	server := httptest.NewServer(h)
	original := stripe.GetBackend(stripe.APIBackend)
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL)})
	stripe.SetBackend(stripe.APIBackend, backend)
	return func() {
		stripe.SetBackend(stripe.APIBackend, original)
		server.Close()
	}
}
