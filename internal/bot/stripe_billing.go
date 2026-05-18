package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/billing"
)

const (
	stripeCheckoutKindSubscription = "subscription"
	stripeCheckoutKindTopup        = "topup"
)

type stripePlanSwitchResult string

const (
	stripePlanSwitchNoop      stripePlanSwitchResult = "noop"
	stripePlanSwitchImmediate stripePlanSwitchResult = "immediate"
	stripePlanSwitchScheduled stripePlanSwitchResult = "scheduled"
)

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
		planCode = billing.DefaultPaidPlan().Code
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

func (b *Bot) switchStripeSubscriptionPlan(ctx context.Context, orgID string, acct billing.Account, targetPlan billing.PaidPlan, targetPriceID string) (stripePlanSwitchResult, error) {
	if acct.StripeSubscriptionID == "" {
		return stripePlanSwitchNoop, errors.New("current Stripe subscription is not available")
	}
	currentPlan, ok := billing.PaidPlanByCode(acct.PlanCode)
	if !ok {
		return stripePlanSwitchNoop, fmt.Errorf("current billing plan %q cannot be switched", acct.PlanCode)
	}
	if currentPlan.Code == targetPlan.Code {
		return stripePlanSwitchNoop, nil
	}

	client := b.stripeClient()
	sub, err := client.V1Subscriptions.Retrieve(ctx, acct.StripeSubscriptionID, nil)
	if err != nil {
		return stripePlanSwitchNoop, fmt.Errorf("retrieve subscription: %w", err)
	}
	item, currentPriceID, err := stripeSubscriptionPlanItem(sub, b.stripeSubscriptionPriceIDsByPlan())
	if err != nil {
		return stripePlanSwitchNoop, err
	}
	if currentPriceID == "" {
		return stripePlanSwitchNoop, errors.New("subscription plan item has no price")
	}

	if isBillingPlanUpgrade(currentPlan, targetPlan) {
		scheduleID, err := mutableStripeSubscriptionScheduleID(ctx, client, sub)
		if err != nil {
			return stripePlanSwitchNoop, err
		}
		if scheduleID != "" {
			if _, err := client.V1SubscriptionSchedules.Release(ctx, scheduleID, &stripe.SubscriptionScheduleReleaseParams{
				PreserveCancelDate: stripe.Bool(true),
			}); err != nil {
				return stripePlanSwitchNoop, fmt.Errorf("release pending subscription schedule: %w", err)
			}
		}
		updated, err := client.V1Subscriptions.Update(ctx, sub.ID, stripeUpgradeSubscriptionParams(orgID, sub.ID, item.ID, targetPlan, targetPriceID))
		if err != nil {
			return stripePlanSwitchNoop, fmt.Errorf("update subscription: %w", err)
		}
		if err := b.mirrorStripeSubscription(ctx, orgID, acct, updated, targetPlan); err != nil {
			return stripePlanSwitchNoop, fmt.Errorf("mirror switched subscription: %w", err)
		}
		return stripePlanSwitchImmediate, nil
	}

	periodStart, periodEnd := stripeSubscriptionItemPeriod(acct, item)
	scheduleID, err := mutableStripeSubscriptionScheduleID(ctx, client, sub)
	if err != nil {
		return stripePlanSwitchNoop, err
	}
	if scheduleID == "" {
		schedule, err := client.V1SubscriptionSchedules.Create(ctx, stripeDowngradeScheduleCreateParams(sub.ID, currentPlan, currentPriceID, targetPlan, targetPriceID, periodStart, periodEnd))
		if err != nil {
			return stripePlanSwitchNoop, fmt.Errorf("create subscription schedule: %w", err)
		}
		scheduleID = schedule.ID
	}

	if _, err := client.V1SubscriptionSchedules.Update(ctx, scheduleID, stripeDowngradeScheduleParams(orgID, acct, item, currentPlan, currentPriceID, targetPlan, targetPriceID)); err != nil {
		return stripePlanSwitchNoop, fmt.Errorf("schedule subscription downgrade: %w", err)
	}
	return stripePlanSwitchScheduled, nil
}

func mutableStripeSubscriptionScheduleID(ctx context.Context, client *stripe.Client, sub *stripe.Subscription) (string, error) {
	if sub == nil || sub.Schedule == nil {
		return "", nil
	}
	scheduleID := strings.TrimSpace(sub.Schedule.ID)
	if scheduleID == "" {
		return "", nil
	}
	status := sub.Schedule.Status
	if status == "" {
		schedule, err := client.V1SubscriptionSchedules.Retrieve(ctx, scheduleID, nil)
		if err != nil {
			return "", fmt.Errorf("retrieve subscription schedule: %w", err)
		}
		status = schedule.Status
	}
	if !stripeSubscriptionScheduleCanUpdate(status) {
		return "", nil
	}
	return scheduleID, nil
}

func stripeSubscriptionScheduleCanUpdate(status stripe.SubscriptionScheduleStatus) bool {
	return status == stripe.SubscriptionScheduleStatusActive || status == stripe.SubscriptionScheduleStatusNotStarted
}

func (b *Bot) stripeSubscriptionPriceIDsByPlan() map[string]string {
	out := map[string]string{}
	for _, plan := range billing.PaidPlans() {
		if priceID := b.stripeSubscriptionPriceID(plan.Code); priceID != "" {
			out[plan.Code] = priceID
		}
	}
	return out
}

func isBillingPlanUpgrade(currentPlan, targetPlan billing.PaidPlan) bool {
	return targetPlan.MonthlyUSDCents > currentPlan.MonthlyUSDCents
}

func stripeSubscriptionPlanItem(sub *stripe.Subscription, priceIDsByPlan map[string]string) (*stripe.SubscriptionItem, string, error) {
	if sub == nil || sub.Items == nil || len(sub.Items.Data) == 0 {
		return nil, "", errors.New("subscription has no items")
	}
	knownPriceIDs := map[string]struct{}{}
	for _, priceID := range priceIDsByPlan {
		if priceID = strings.TrimSpace(priceID); priceID != "" {
			knownPriceIDs[priceID] = struct{}{}
		}
	}
	var singleItem *stripe.SubscriptionItem
	for _, item := range sub.Items.Data {
		if item == nil || item.ID == "" {
			continue
		}
		singleItem = item
		if item.Price != nil {
			if _, ok := knownPriceIDs[item.Price.ID]; ok {
				return item, item.Price.ID, nil
			}
		}
	}
	if len(sub.Items.Data) == 1 && singleItem != nil {
		priceID := ""
		if singleItem.Price != nil {
			priceID = singleItem.Price.ID
		}
		return singleItem, priceID, nil
	}
	return nil, "", errors.New("subscription has no Hetchy plan item")
}

func stripeUpgradeSubscriptionParams(orgID, subscriptionID, itemID string, targetPlan billing.PaidPlan, targetPriceID string) *stripe.SubscriptionUpdateParams {
	return &stripe.SubscriptionUpdateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-plan-upgrade-" + subscriptionID + "-" + itemID + "-" + targetPlan.Code),
		},
		Items: []*stripe.SubscriptionUpdateItemParams{{
			ID:       stripe.String(itemID),
			Price:    stripe.String(targetPriceID),
			Quantity: stripe.Int64(1),
		}},
		Metadata:          paidSubscriptionMetadata(orgID, targetPlan),
		PaymentBehavior:   stripe.String("pending_if_incomplete"),
		ProrationBehavior: stripe.String("always_invoice"),
	}
}

func (b *Bot) mirrorStripeSubscription(ctx context.Context, orgID string, acct billing.Account, sub *stripe.Subscription, plan billing.PaidPlan) error {
	if sub == nil || sub.ID == "" {
		return errors.New("stripe subscription response is empty")
	}
	customerID := acct.StripeCustomerID
	if sub.Customer != nil && sub.Customer.ID != "" {
		customerID = sub.Customer.ID
	}
	start, end := stripeSubscriptionPeriod(acct, sub, b.stripeSubscriptionPriceIDsByPlan())
	_, err := b.billing.UpsertAccountMirror(ctx, paidAccountMirror(
		orgID,
		customerID,
		sub.ID,
		string(sub.Status),
		start,
		end,
		paidSubscriptionMetadata(orgID, plan),
	))
	return err
}

func stripeSubscriptionPeriod(acct billing.Account, sub *stripe.Subscription, priceIDsByPlan map[string]string) (time.Time, time.Time) {
	item, _, err := stripeSubscriptionPlanItem(sub, priceIDsByPlan)
	if err != nil {
		return acct.CurrentPeriodStart, acct.CurrentPeriodEnd
	}
	start, end := stripeSubscriptionItemPeriod(acct, item)
	return unixTime(start), unixTime(end)
}

func stripeDowngradeScheduleCreateParams(subscriptionID string, currentPlan billing.PaidPlan, currentPriceID string, targetPlan billing.PaidPlan, targetPriceID string, periodStart, periodEnd int64) *stripe.SubscriptionScheduleCreateParams {
	return &stripe.SubscriptionScheduleCreateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-plan-schedule-" + subscriptionID + "-" + currentPlan.Code + "-" + currentPriceID + "-" + targetPlan.Code + "-" + targetPriceID + "-" + strconv.FormatInt(periodStart, 10) + "-" + strconv.FormatInt(periodEnd, 10)),
		},
		FromSubscription: stripe.String(subscriptionID),
	}
}

func stripeDowngradeScheduleParams(orgID string, acct billing.Account, currentItem *stripe.SubscriptionItem, currentPlan billing.PaidPlan, currentPriceID string, targetPlan billing.PaidPlan, targetPriceID string) *stripe.SubscriptionScheduleUpdateParams {
	periodStart, periodEnd := stripeSubscriptionItemPeriod(acct, currentItem)
	quantity := currentItem.Quantity
	if quantity < 1 {
		quantity = 1
	}
	return &stripe.SubscriptionScheduleUpdateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-plan-downgrade-" + orgID + "-" + currentPlan.Code + "-" + currentPriceID + "-" + targetPlan.Code + "-" + targetPriceID + "-" + strconv.FormatInt(periodStart, 10) + "-" + strconv.FormatInt(periodEnd, 10)),
		},
		EndBehavior:       stripe.String(string(stripe.SubscriptionScheduleEndBehaviorRelease)),
		ProrationBehavior: stripe.String("none"),
		Metadata: map[string]string{
			"kind":              stripeCheckoutKindSubscription,
			"org_id":            orgID,
			"pending_plan_code": targetPlan.Code,
		},
		Phases: []*stripe.SubscriptionScheduleUpdatePhaseParams{
			{
				StartDate:         stripe.Int64(periodStart),
				EndDate:           stripe.Int64(periodEnd),
				ProrationBehavior: stripe.String("none"),
				Metadata:          paidSubscriptionMetadata(orgID, currentPlan),
				Items: []*stripe.SubscriptionScheduleUpdatePhaseItemParams{{
					Price:    stripe.String(currentPriceID),
					Quantity: stripe.Int64(quantity),
				}},
			},
			{
				ProrationBehavior: stripe.String("none"),
				Metadata:          paidSubscriptionMetadata(orgID, targetPlan),
				Items: []*stripe.SubscriptionScheduleUpdatePhaseItemParams{{
					Price:    stripe.String(targetPriceID),
					Quantity: stripe.Int64(1),
				}},
			},
		},
	}
}

func stripeSubscriptionItemPeriod(acct billing.Account, item *stripe.SubscriptionItem) (int64, int64) {
	now := time.Now().Unix()
	start := item.CurrentPeriodStart
	if start <= 0 && !acct.CurrentPeriodStart.IsZero() {
		start = acct.CurrentPeriodStart.Unix()
	}
	if start <= 0 {
		start = now
	}
	end := item.CurrentPeriodEnd
	if end <= 0 && !acct.CurrentPeriodEnd.IsZero() {
		end = acct.CurrentPeriodEnd.Unix()
	}
	if end <= start {
		end = start + int64(30*24*time.Hour/time.Second)
	}
	return start, end
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
			Metadata: map[string]string{
				"kind":    stripeCheckoutKindTopup,
				"org_id":  p.OrgID,
				"credits": strconv.Itoa(credits),
			},
		},
		Metadata: map[string]string{
			"kind":     stripeCheckoutKindTopup,
			"org_id":   p.OrgID,
			"quantity": strconv.Itoa(quantity),
			"credits":  strconv.Itoa(credits),
		},
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

func (b *Bot) stripeWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(b.cfg.StripeWebhookSecret) == "" {
		http.Error(w, "stripe webhook is not configured", http.StatusServiceUnavailable)
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	event, err := webhook.ConstructEventWithOptions(payload, r.Header.Get("Stripe-Signature"), b.cfg.StripeWebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		b.log.Warn("stripe webhook signature verification failed", "error", err)
		http.Error(w, "invalid stripe signature", http.StatusBadRequest)
		return
	}
	if b.billing == nil || !b.billing.Enabled() {
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := b.handleStripeEvent(r.Context(), event); err != nil {
		b.log.Error("handle stripe event", "event_id", event.ID, "event_type", event.Type, "error", err)
		http.Error(w, "stripe webhook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *Bot) handleStripeEvent(ctx context.Context, event stripe.Event) error {
	switch string(event.Type) {
	case "checkout.session.completed":
		return b.handleStripeCheckoutCompleted(ctx, event)
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		return b.handleStripeSubscription(ctx, event)
	case "invoice.payment_failed":
		return b.handleStripeInvoicePaymentFailed(ctx, event)
	default:
		return nil
	}
}

func (b *Bot) handleStripeCheckoutCompleted(ctx context.Context, event stripe.Event) error {
	var sess stripeCheckoutSessionObject
	if err := decodeStripeEventObject(event, &sess); err != nil {
		return err
	}
	orgID := stripeOrgID(sess.Metadata, sess.ClientReferenceID)
	if orgID == "" {
		return nil
	}
	customerID := sess.Customer.String()
	if customerID != "" {
		if _, err := b.billing.SetStripeCustomer(ctx, orgID, customerID); err != nil {
			return err
		}
	}

	switch sess.Metadata["kind"] {
	case stripeCheckoutKindTopup:
		if sess.Mode != string(stripe.CheckoutSessionModePayment) || sess.PaymentStatus != "paid" {
			return nil
		}
		credits, err := b.stripeCheckoutTopupCredits(ctx, sess)
		if err != nil {
			return err
		}
		if _, _, err := b.billing.GrantTopupCreditsOnce(ctx, event.ID, string(event.Type), orgID, credits); err != nil {
			return err
		}
	case stripeCheckoutKindSubscription:
		if sess.Subscription.String() == "" {
			return nil
		}
		mirror := paidAccountMirror(orgID, customerID, sess.Subscription.String(), "active", time.Time{}, time.Time{}, sess.Metadata)
		if _, err := b.billing.UpsertAccountMirror(ctx, mirror); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) handleStripeSubscription(ctx context.Context, event stripe.Event) error {
	var sub stripeSubscriptionObject
	if err := decodeStripeEventObject(event, &sub); err != nil {
		return err
	}
	orgID := stripeOrgID(sub.Metadata, "")
	customerID := sub.Customer.String()
	if orgID == "" && customerID != "" {
		acct, err := b.billing.FindAccountByStripeCustomer(ctx, customerID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		orgID = acct.OrgID
	}
	if orgID == "" {
		return nil
	}
	start, end := sub.Period()
	mirror := paidAccountMirror(orgID, customerID, sub.ID, sub.Status, start, end, sub.Metadata)
	if string(event.Type) == "customer.subscription.deleted" || sub.Status == string(stripe.SubscriptionStatusCanceled) {
		mirror.IncludedCredits = 0
		mirror.MaxFlavor = billing.FlavorStandard
		mirror.PerRunMaxCredits = 4
	}
	_, err := b.billing.UpsertAccountMirror(ctx, mirror)
	return err
}

func (b *Bot) handleStripeInvoicePaymentFailed(ctx context.Context, event stripe.Event) error {
	var inv stripeInvoiceObject
	if err := decodeStripeEventObject(event, &inv); err != nil {
		return err
	}
	orgID := stripeOrgID(inv.Metadata, "")
	customerID := inv.Customer.String()
	if orgID == "" && customerID != "" {
		acct, err := b.billing.FindAccountByStripeCustomer(ctx, customerID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		orgID = acct.OrgID
	}
	if orgID == "" {
		return nil
	}
	return b.billing.SetLastPaymentError(ctx, orgID, "Stripe payment failed. Update the payment method in Billing / Usage before starting more paid runs.")
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
		Metadata: map[string]string{
			"org_id": orgID,
		},
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
		IncludedCredits:      metadataInt(metadata, "included_credits", plan.IncludedCredits),
		MaxFlavor:            plan.MaxFlavor,
		PerRunMaxCredits:     plan.PerRunMaxCredits,
	}
}

func paidSubscriptionMetadata(orgID string, plan billing.PaidPlan) map[string]string {
	return map[string]string{
		"kind":                stripeCheckoutKindSubscription,
		"org_id":              orgID,
		"plan_code":           plan.Code,
		"included_credits":    strconv.Itoa(plan.IncludedCredits),
		"max_flavor":          plan.MaxFlavor,
		"per_run_max_credits": strconv.Itoa(plan.PerRunMaxCredits),
	}
}

func decodeStripeEventObject(event stripe.Event, dst any) error {
	if event.Data == nil {
		return errors.New("stripe event data is empty")
	}
	if err := json.Unmarshal(event.Data.Raw, dst); err != nil {
		return fmt.Errorf("decode stripe %s object: %w", event.Type, err)
	}
	return nil
}

func stripeOrgID(metadata map[string]string, fallback string) string {
	if metadata != nil {
		if orgID := strings.TrimSpace(metadata["org_id"]); orgID != "" {
			return orgID
		}
	}
	return strings.TrimSpace(fallback)
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

func (s stripeAutoTopupper) PurchaseTopupUnit(ctx context.Context, account billing.Account) error {
	if strings.TrimSpace(account.StripeCustomerID) == "" {
		return billing.ErrAutoTopupNotConfigured
	}
	priceID := stripeTopupPlanPriceID(s.topupPriceIDs, s.topupPriceID, account.PlanCode)
	if priceID == "" {
		return billing.ErrAutoTopupNotConfigured
	}
	client := stripe.NewClient(s.secretKey)
	idempotencySuffix := account.OrgID + "-" + time.Now().UTC().Format("20060102150405.000000000")
	created, err := client.V1Invoices.Create(ctx, &stripe.InvoiceCreateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-auto-topup-invoice-" + idempotencySuffix),
		},
		Customer:                    stripe.String(account.StripeCustomerID),
		CollectionMethod:            stripe.String(string(stripe.InvoiceCollectionMethodChargeAutomatically)),
		PendingInvoiceItemsBehavior: stripe.String("exclude"),
		AutoAdvance:                 stripe.Bool(false),
		Metadata: map[string]string{
			"kind":    stripeCheckoutKindTopup,
			"org_id":  account.OrgID,
			"credits": strconv.Itoa(billing.TopupUnitCredits),
		},
	})
	if err != nil {
		return err
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
		Metadata: map[string]string{
			"kind":    stripeCheckoutKindTopup,
			"org_id":  account.OrgID,
			"credits": strconv.Itoa(billing.TopupUnitCredits),
		},
	}); err != nil {
		return err
	}
	finalized, err := client.V1Invoices.FinalizeInvoice(ctx, created.ID, &stripe.InvoiceFinalizeInvoiceParams{
		AutoAdvance: stripe.Bool(false),
	})
	if err != nil {
		return err
	}
	if finalized.Status == stripe.InvoiceStatusPaid {
		return nil
	}
	paid, err := client.V1Invoices.Pay(ctx, finalized.ID, &stripe.InvoicePayParams{})
	if err != nil {
		return err
	}
	if paid.Status != stripe.InvoiceStatusPaid {
		return fmt.Errorf("stripe auto top-up invoice %s finished with status %q", paid.ID, paid.Status)
	}
	return nil
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

type stripeSubscriptionObject struct {
	ID                 string            `json:"id"`
	Customer           stripeID          `json:"customer"`
	Status             string            `json:"status"`
	CurrentPeriodStart int64             `json:"current_period_start"`
	CurrentPeriodEnd   int64             `json:"current_period_end"`
	Metadata           map[string]string `json:"metadata"`
	Items              struct {
		Data []struct {
			CurrentPeriodStart int64 `json:"current_period_start"`
			CurrentPeriodEnd   int64 `json:"current_period_end"`
		} `json:"data"`
	} `json:"items"`
}

func (s stripeSubscriptionObject) Period() (time.Time, time.Time) {
	startUnix := s.CurrentPeriodStart
	endUnix := s.CurrentPeriodEnd
	if (startUnix == 0 || endUnix == 0) && len(s.Items.Data) > 0 {
		startUnix = s.Items.Data[0].CurrentPeriodStart
		endUnix = s.Items.Data[0].CurrentPeriodEnd
	}
	return unixTime(startUnix), unixTime(endUnix)
}

type stripeInvoiceObject struct {
	ID       string            `json:"id"`
	Customer stripeID          `json:"customer"`
	Metadata map[string]string `json:"metadata"`
}

type stripeID string

func (id *stripeID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*id = ""
		return nil
	}
	if strings.HasPrefix(raw, "\"") {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*id = stripeID(s)
		return nil
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
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
