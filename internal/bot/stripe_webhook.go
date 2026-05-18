package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"

	"github.com/hetchyhq/hetchy/internal/billing"
)

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
	case "invoice.paid", "invoice.payment_succeeded":
		return b.handleStripeInvoicePaid(ctx, event)
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

func (b *Bot) handleStripeInvoicePaid(ctx context.Context, event stripe.Event) error {
	var inv stripeInvoiceObject
	if err := decodeStripeEventObject(event, &inv); err != nil {
		return err
	}
	if inv.Metadata["kind"] != stripeCheckoutKindTopup {
		return nil
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
	credits := metadataInt(inv.Metadata, "credits", 0)
	if credits <= 0 {
		return nil
	}
	eventID := strings.TrimSpace(inv.ID)
	if eventID == "" {
		eventID = event.ID
	}
	_, _, err := b.billing.GrantTopupCreditsOnce(ctx, eventID, string(event.Type), orgID, credits)
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
