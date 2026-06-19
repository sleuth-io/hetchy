package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v85"

	"github.com/sleuth-io/hetchy/internal/billing"
)

type stripePlanSwitchResult string

const (
	stripePlanSwitchNoop      stripePlanSwitchResult = "noop"
	stripePlanSwitchImmediate stripePlanSwitchResult = "immediate"
	stripePlanSwitchScheduled stripePlanSwitchResult = "scheduled"
)

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
	if stripeSubscriptionHasPendingCancellation(sub) {
		updated, err := client.V1Subscriptions.Update(ctx, sub.ID, stripeResumeSubscriptionParams(sub))
		if err != nil {
			return stripePlanSwitchNoop, fmt.Errorf("resume subscription before plan switch: %w", err)
		}
		sub = updated
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
				PreserveCancelDate: stripe.Bool(false),
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
		if _, err := b.billing.ClearPendingPlanChange(ctx, orgID); err != nil {
			return stripePlanSwitchNoop, fmt.Errorf("clear pending plan change: %w", err)
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

	if _, err := client.V1SubscriptionSchedules.Update(ctx, scheduleID, stripeDowngradeScheduleParams(scheduleID, orgID, acct, item, currentPlan, currentPriceID, targetPlan, targetPriceID)); err != nil {
		return stripePlanSwitchNoop, fmt.Errorf("schedule subscription downgrade: %w", err)
	}
	if _, err := b.billing.SetPendingPlanChange(ctx, orgID, targetPlan.Code, unixTime(periodEnd)); err != nil {
		return stripePlanSwitchNoop, fmt.Errorf("record pending plan change: %w", err)
	}
	return stripePlanSwitchScheduled, nil
}

func stripeSubscriptionHasPendingCancellation(sub *stripe.Subscription) bool {
	return sub != nil && (sub.CancelAt > 0 || sub.CancelAtPeriodEnd)
}

func stripeResumeSubscriptionParams(sub *stripe.Subscription) *stripe.SubscriptionUpdateParams {
	subscriptionID := ""
	if sub != nil {
		subscriptionID = sub.ID
	}
	params := &stripe.SubscriptionUpdateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-plan-resume-" + subscriptionID),
		},
	}
	if sub != nil && sub.CancelAt > 0 {
		params.AddUnsetField(stripe.SubscriptionUpdateParamsUnsetFieldCancelAt)
		return params
	}
	params.CancelAtPeriodEnd = stripe.Bool(false)
	return params
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
		// Stripe rejects metadata when creating a schedule from a subscription.
		// The immediate update call applies Hetchy's schedule metadata instead.
		FromSubscription: stripe.String(subscriptionID),
	}
}

func stripeDowngradeScheduleParams(scheduleID, orgID string, acct billing.Account, currentItem *stripe.SubscriptionItem, currentPlan billing.PaidPlan, currentPriceID string, targetPlan billing.PaidPlan, targetPriceID string) *stripe.SubscriptionScheduleUpdateParams {
	periodStart, periodEnd := stripeSubscriptionItemPeriod(acct, currentItem)
	quantity := max(currentItem.Quantity, 1)
	return &stripe.SubscriptionScheduleUpdateParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("hetchy-plan-downgrade-" + scheduleID + "-" + orgID + "-" + currentPlan.Code + "-" + currentPriceID + "-" + targetPlan.Code + "-" + targetPriceID + "-" + strconv.FormatInt(periodStart, 10) + "-" + strconv.FormatInt(periodEnd, 10)),
		},
		EndBehavior:       stripe.String(string(stripe.SubscriptionScheduleEndBehaviorRelease)),
		ProrationBehavior: stripe.String("none"),
		Metadata: hetchyStripeMetadata(map[string]string{
			"kind":              stripeCheckoutKindSubscription,
			"org_id":            orgID,
			"pending_plan_code": targetPlan.Code,
		}),
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
