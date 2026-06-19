package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v85"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/billing"
)

const (
	stripeCheckoutKindSubscription = "subscription"
	stripeCheckoutKindTopup        = "topup"
	stripeMetadataAppKey           = "app"
	stripeMetadataAppHetchy        = "hetchy"
)

func hetchyStripeMetadata(values map[string]string) map[string]string {
	metadata := map[string]string{}
	maps.Copy(metadata, values)
	metadata[stripeMetadataAppKey] = stripeMetadataAppHetchy
	return metadata
}

func newStripeAutoTopupper(cfg Config) billing.AutoTopupper {
	if strings.TrimSpace(cfg.StripeSecretKey) == "" ||
		stripeTopupPlanPriceID(cfg.StripeTopupPriceIDs, cfg.StripeTopupPriceID, billing.DefaultPaidPlan().Code) == "" {
		return nil
	}
	return stripeAutoTopupper{
		secretKey:     strings.TrimSpace(cfg.StripeSecretKey),
		topupPriceID:  strings.TrimSpace(cfg.StripeTopupPriceID),
		topupPriceIDs: cfg.StripeTopupPriceIDs,
	}
}

func (b *Bot) stripeConfigured() bool {
	return strings.TrimSpace(b.cfg.StripeSecretKey) != "" &&
		b.stripeSubscriptionPriceID(billing.DefaultPaidPlan().Code) != "" &&
		b.stripeTopupPriceID(billing.DefaultPaidPlan().Code) != ""
}

func (b *Bot) stripeClient() *stripe.Client {
	return stripe.NewClient(strings.TrimSpace(b.cfg.StripeSecretKey))
}

func (b *Bot) stripeSubscriptionPriceID(planCode string) string {
	planCode = strings.ToLower(strings.TrimSpace(planCode))
	if planCode == "" {
		planCode = billing.DefaultPaidPlan().Code
	}
	if b.cfg.StripeSubscriptionPriceIDs != nil {
		if priceID := strings.TrimSpace(b.cfg.StripeSubscriptionPriceIDs[planCode]); priceID != "" {
			return priceID
		}
	}
	if planCode == billing.DefaultPaidPlan().Code {
		return strings.TrimSpace(b.cfg.StripeSubscriptionPriceID)
	}
	return ""
}

func (b *Bot) stripeTopupPriceID(planCode string) string {
	return stripeTopupPlanPriceID(b.cfg.StripeTopupPriceIDs, b.cfg.StripeTopupPriceID, planCode)
}

func stripeTopupPlanPriceID(priceIDs map[string]string, legacy, planCode string) string {
	planCode = strings.ToLower(strings.TrimSpace(planCode))
	if _, ok := billing.PaidPlanByCode(planCode); !ok {
		return ""
	}
	if priceIDs != nil {
		if priceID := strings.TrimSpace(priceIDs[planCode]); priceID != "" {
			return priceID
		}
	}
	return strings.TrimSpace(legacy)
}

func (b *Bot) checkoutPlan(planCode string) (billing.PaidPlan, string, error) {
	planCode = strings.ToLower(strings.TrimSpace(planCode))
	if planCode == "" {
		planCode = billing.DefaultPaidPlan().Code
	}
	plan, ok := billing.PaidPlanByCode(planCode)
	if !ok {
		return billing.PaidPlan{}, "", fmt.Errorf("unknown billing plan %q", planCode)
	}
	priceID := b.stripeSubscriptionPriceID(plan.Code)
	if priceID == "" {
		return billing.PaidPlan{}, "", fmt.Errorf("billing plan %q is not configured", plan.Code)
	}
	return plan, priceID, nil
}

func (b *Bot) billingCheckoutHandler(w http.ResponseWriter, r *http.Request) {
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
	if !b.stripeConfigured() {
		http.Error(w, "stripe is not configured", http.StatusServiceUnavailable)
		return
	}
	plan, priceID, err := b.checkoutPlan(r.FormValue("plan"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Load overview only to gate comped accounts before touching Stripe.
	overview, err := b.billing.Overview(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("load billing account for checkout", "error", err, "org", p.OrgID)
		http.Error(w, "load billing account: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if overview.Account.BillingExempt {
		http.Error(w, "subscriptions are managed externally for comped accounts", http.StatusForbidden)
		return
	}
	acct, err := b.ensureStripeCustomer(r.Context(), p.OrgID, p.Email)
	if err != nil {
		b.log.Error("ensure stripe customer for checkout", "error", err, "org", p.OrgID)
		http.Error(w, "stripe customer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if acct.StripeSubscriptionID != "" {
		result, err := b.switchStripeSubscriptionPlan(r.Context(), p.OrgID, acct, plan, priceID)
		if err != nil {
			b.log.Error("switch stripe subscription plan", "error", err, "org", p.OrgID, "plan", plan.Code)
			http.Error(w, "stripe plan switch: "+err.Error(), http.StatusBadGateway)
			return
		}
		saved := "plan_switched"
		if result == stripePlanSwitchScheduled {
			saved = "plan_scheduled"
		}
		http.Redirect(w, r, b.settingsURL("billing", "saved="+saved), http.StatusSeeOther)
		return
	}

	b.startStripeSubscriptionCheckout(w, r, p.OrgID, acct.StripeCustomerID, plan, priceID)
}

func (b *Bot) startStripeSubscriptionCheckout(w http.ResponseWriter, r *http.Request, orgID, customerID string, plan billing.PaidPlan, priceID string) {
	metadata := paidSubscriptionMetadata(orgID, plan)
	params := &stripe.CheckoutSessionCreateParams{
		Mode: stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{
			Price:    stripe.String(priceID),
			Quantity: stripe.Int64(1),
		}},
		Customer:          stripe.String(customerID),
		ClientReferenceID: stripe.String(orgID),
		SuccessURL:        stripe.String(b.settingsURL("billing", "saved=1")),
		CancelURL:         stripe.String(b.settingsURL("billing", "")),
		Metadata:          metadata,
		SubscriptionData: &stripe.CheckoutSessionCreateSubscriptionDataParams{
			Metadata: metadata,
		},
	}
	sess, err := b.stripeClient().V1CheckoutSessions.Create(r.Context(), params)
	if err != nil {
		b.log.Error("create stripe subscription checkout", "error", err, "org", orgID)
		http.Error(w, "stripe checkout: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, sess.URL, http.StatusSeeOther)
}

func (b *Bot) billingTopupHandler(w http.ResponseWriter, r *http.Request) {
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
	if !b.stripeConfigured() {
		http.Error(w, "stripe is not configured", http.StatusServiceUnavailable)
		return
	}
	quantity := parseBillingInt(r.FormValue("quantity"), 1)
	if quantity < 1 || quantity > 100 {
		http.Error(w, "quantity must be between 1 and 100", http.StatusBadRequest)
		return
	}
	credits := quantity * billing.TopupUnitCredits

	overview, err := b.billing.Overview(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("load billing account for top-up", "error", err, "org", p.OrgID)
		http.Error(w, "load billing account: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !billingAccountAllowsTopups(overview.Account) {
		http.Error(w, "top-ups require a paid plan", http.StatusBadRequest)
		return
	}

	acct, err := b.ensureStripeCustomer(r.Context(), p.OrgID, p.Email)
	if err != nil {
		b.log.Error("ensure stripe customer for top-up", "error", err, "org", p.OrgID)
		http.Error(w, "stripe customer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	priceID := b.stripeTopupPriceID(acct.PlanCode)
	if priceID == "" {
		http.Error(w, "top-up price is not configured for plan "+acct.PlanCode, http.StatusServiceUnavailable)
		return
	}

	params := &stripe.CheckoutSessionCreateParams{
		Mode: stripe.String(string(stripe.CheckoutSessionModePayment)),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			stripeTopupLineItem(priceID, quantity),
		},
		Customer:          stripe.String(acct.StripeCustomerID),
		ClientReferenceID: stripe.String(p.OrgID),
		SuccessURL:        stripe.String(b.settingsURL("billing", "saved=topup_started")),
		CancelURL:         stripe.String(b.settingsURL("billing", "")),
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			SetupFutureUsage: stripe.String("off_session"),
			Metadata: hetchyStripeMetadata(map[string]string{
				"kind":    stripeCheckoutKindTopup,
				"org_id":  p.OrgID,
				"credits": strconv.Itoa(credits),
			}),
		},
		Metadata: hetchyStripeMetadata(map[string]string{
			"kind":     stripeCheckoutKindTopup,
			"org_id":   p.OrgID,
			"quantity": strconv.Itoa(quantity),
			"credits":  strconv.Itoa(credits),
		}),
	}
	sess, err := b.stripeClient().V1CheckoutSessions.Create(r.Context(), params)
	if err != nil {
		b.log.Error("create stripe top-up checkout", "error", err, "org", p.OrgID, "quantity", quantity)
		http.Error(w, "stripe checkout: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, sess.URL, http.StatusSeeOther)
}

func stripeTopupLineItem(priceID string, quantity int) *stripe.CheckoutSessionCreateLineItemParams {
	return &stripe.CheckoutSessionCreateLineItemParams{
		Price:    stripe.String(priceID),
		Quantity: stripe.Int64(int64(quantity)),
		AdjustableQuantity: &stripe.CheckoutSessionCreateLineItemAdjustableQuantityParams{
			Enabled: stripe.Bool(true),
			Minimum: stripe.Int64(1),
			Maximum: stripe.Int64(100),
		},
	}
}

func (b *Bot) billingPortalHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		if err := requireSameOrigin(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}
	if b.billing == nil || !b.billing.Enabled() {
		http.Error(w, "billing is not configured", http.StatusInternalServerError)
		return
	}
	overview, err := b.billing.Overview(r.Context(), p.OrgID)
	if err != nil {
		http.Error(w, "load billing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if overview.Account.StripeCustomerID == "" {
		http.Error(w, "stripe customer is not available yet", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(b.cfg.StripeSecretKey) == "" {
		http.Error(w, "stripe is not configured", http.StatusServiceUnavailable)
		return
	}

	sess, err := b.stripeClient().V1BillingPortalSessions.Create(r.Context(), &stripe.BillingPortalSessionCreateParams{
		Customer:  stripe.String(overview.Account.StripeCustomerID),
		ReturnURL: stripe.String(b.settingsURL("billing", "saved=portal_return")),
	})
	if err != nil {
		b.log.Error("create stripe portal session", "error", err, "org", p.OrgID)
		http.Error(w, "stripe portal: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, sess.URL, http.StatusSeeOther)
}

func (b *Bot) ensureStripeCustomer(ctx context.Context, orgID, email string) (billing.Account, error) {
	overview, err := b.billing.Overview(ctx, orgID)
	if err != nil {
		return billing.Account{}, err
	}
	if overview.Account.StripeCustomerID != "" {
		return overview.Account, nil
	}
	cust, err := b.stripeClient().V1Customers.Create(ctx, &stripe.CustomerCreateParams{
		Email:       stripe.String(email),
		Description: stripe.String("Hetchy organization " + orgID),
		Metadata: hetchyStripeMetadata(map[string]string{
			"org_id": orgID,
		}),
	})
	if err != nil {
		return billing.Account{}, err
	}
	return b.billing.SetStripeCustomer(ctx, orgID, cust.ID)
}

func paidAccountMirror(orgID, customerID, subscriptionID, status string, periodStart, periodEnd time.Time, metadata map[string]string) billing.AccountMirror {
	if status == "" {
		status = "active"
	}
	defaultPlan := billing.DefaultPaidPlan()
	planCode := metadataString(metadata, "plan_code", defaultPlan.Code)
	plan, ok := billing.PaidPlanByCode(planCode)
	if !ok {
		plan = defaultPlan
		planCode = defaultPlan.Code
	}
	return billing.AccountMirror{
		OrgID:                orgID,
		StripeCustomerID:     customerID,
		StripeSubscriptionID: subscriptionID,
		PlanCode:             planCode,
		Status:               status,
		CurrentPeriodStart:   periodStart,
		CurrentPeriodEnd:     periodEnd,
		IncludedCredits:      plan.IncludedCredits,
		MaxFlavor:            plan.MaxFlavor,
		PerRunMaxCredits:     plan.PerRunMaxCredits,
	}
}

func paidSubscriptionMetadata(orgID string, plan billing.PaidPlan) map[string]string {
	return hetchyStripeMetadata(map[string]string{
		"kind":                stripeCheckoutKindSubscription,
		"org_id":              orgID,
		"plan_code":           plan.Code,
		"included_credits":    strconv.Itoa(plan.IncludedCredits),
		"max_flavor":          plan.MaxFlavor,
		"per_run_max_credits": strconv.Itoa(plan.PerRunMaxCredits),
	})
}

func metadataString(metadata map[string]string, key, def string) string {
	if metadata == nil {
		return def
	}
	if v := strings.TrimSpace(metadata[key]); v != "" {
		return v
	}
	return def
}

func metadataInt(metadata map[string]string, key string, def int) int {
	if metadata == nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(metadata[key]))
	if err != nil || n < 0 {
		return def
	}
	return n
}

func topupCreditsFromCheckoutMetadata(metadata map[string]string) int {
	credits := metadataInt(metadata, "credits", 0)
	if credits == 0 {
		credits = metadataInt(metadata, "quantity", 1) * billing.TopupUnitCredits
	}
	return credits
}

func (b *Bot) stripeCheckoutTopupCredits(ctx context.Context, sess stripeCheckoutSessionObject) (int, error) {
	if strings.TrimSpace(sess.ID) == "" || strings.TrimSpace(b.cfg.StripeSecretKey) == "" {
		return topupCreditsFromCheckoutMetadata(sess.Metadata), nil
	}
	quantity, err := b.stripeCheckoutLineItemQuantity(ctx, sess.ID)
	if err != nil {
		return 0, err
	}
	if quantity <= 0 {
		return 0, fmt.Errorf("stripe checkout session %s has no top-up line item quantity", sess.ID)
	}
	return quantity * billing.TopupUnitCredits, nil
}

func (b *Bot) stripeCheckoutLineItemQuantity(ctx context.Context, sessionID string) (int, error) {
	lines := b.stripeClient().V1CheckoutSessions.ListLineItems(ctx, &stripe.CheckoutSessionListLineItemsParams{
		Session: stripe.String(sessionID),
	})
	if err := lines.Err(); err != nil {
		return 0, fmt.Errorf("list stripe checkout session line items: %w", err)
	}
	var quantity int64
	for _, item := range lines.Data() {
		if item != nil {
			quantity += item.Quantity
		}
	}
	return int(quantity), nil
}

func (b *Bot) settingsURL(tab, query string) string {
	base := strings.TrimRight(b.cfg.StripeReturnBaseURL(), "/") + "/settings/org?tab=" + tab
	if query == "" {
		return base
	}
	return base + "&" + strings.TrimPrefix(query, "?")
}

type stripeAutoTopupper struct {
	secretKey     string
	topupPriceID  string
	topupPriceIDs map[string]string
}

func (s stripeAutoTopupper) PurchaseTopupUnit(ctx context.Context, account billing.Account, idempotencyKey string) (string, error) {
	if strings.TrimSpace(account.StripeCustomerID) == "" {
		return "", billing.ErrAutoTopupNotConfigured
	}
	priceID := stripeTopupPlanPriceID(s.topupPriceIDs, s.topupPriceID, account.PlanCode)
	if priceID == "" {
		return "", billing.ErrAutoTopupNotConfigured
	}
	client := stripe.NewClient(s.secretKey)
	idempotencySuffix := strings.TrimSpace(idempotencyKey)
	if idempotencySuffix == "" {
		idempotencySuffix = account.OrgID + "-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	created, err := client.V1Invoices.Create(ctx, &stripe.InvoiceCreateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-auto-topup-invoice-" + idempotencySuffix),
		},
		Customer:                    stripe.String(account.StripeCustomerID),
		CollectionMethod:            stripe.String(string(stripe.InvoiceCollectionMethodChargeAutomatically)),
		PendingInvoiceItemsBehavior: stripe.String("exclude"),
		AutoAdvance:                 stripe.Bool(false),
		Metadata: hetchyStripeMetadata(map[string]string{
			"kind":    stripeCheckoutKindTopup,
			"org_id":  account.OrgID,
			"credits": strconv.Itoa(billing.TopupUnitCredits),
		}),
	})
	if err != nil {
		return "", err
	}
	if _, err := client.V1InvoiceItems.Create(ctx, &stripe.InvoiceItemCreateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-auto-topup-item-" + idempotencySuffix),
		},
		Customer: stripe.String(account.StripeCustomerID),
		Invoice:  stripe.String(created.ID),
		Pricing: &stripe.InvoiceItemCreatePricingParams{
			Price: stripe.String(priceID),
		},
		Quantity: stripe.Int64(1),
		Metadata: hetchyStripeMetadata(map[string]string{
			"kind":    stripeCheckoutKindTopup,
			"org_id":  account.OrgID,
			"credits": strconv.Itoa(billing.TopupUnitCredits),
		}),
	}); err != nil {
		return "", err
	}
	finalized, err := client.V1Invoices.FinalizeInvoice(ctx, created.ID, &stripe.InvoiceFinalizeInvoiceParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-auto-topup-finalize-" + idempotencySuffix),
		},
		AutoAdvance: stripe.Bool(false),
	})
	if err != nil {
		return "", err
	}
	if finalized.Status == stripe.InvoiceStatusPaid {
		return finalized.ID, nil
	}
	paid, err := client.V1Invoices.Pay(ctx, finalized.ID, &stripe.InvoicePayParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-auto-topup-pay-" + idempotencySuffix),
		},
	})
	if err != nil {
		return "", err
	}
	if paid.Status != stripe.InvoiceStatusPaid {
		return "", fmt.Errorf("stripe auto top-up invoice %s finished with status %q", paid.ID, paid.Status)
	}
	return paid.ID, nil
}

type stripeCheckoutSessionObject struct {
	ID                string            `json:"id"`
	Mode              string            `json:"mode"`
	PaymentStatus     string            `json:"payment_status"`
	Customer          stripeID          `json:"customer"`
	Subscription      stripeID          `json:"subscription"`
	ClientReferenceID string            `json:"client_reference_id"`
	Metadata          map[string]string `json:"metadata"`
}

type stripeID string

func (id *stripeID) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*id = ""
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		*id = stripeID(s)
		return nil
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	*id = stripeID(obj.ID)
	return nil
}

func (id stripeID) String() string {
	return string(id)
}

func unixTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
