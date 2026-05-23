package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/billing"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

func (b *Bot) admitBillingForRun(ctx context.Context, orgID, owner, repo string, emit blocks.Emitter) (billing.Flavor, bool) {
	flavor := billing.MustFlavor(billing.FlavorStandard)
	run, ok := agentRunFromContext(ctx)
	if !ok || b.billing == nil || !b.billing.Enabled() {
		return flavor, true
	}
	admission, err := b.billing.AdmitRun(ctx, billing.AdmissionRequest{
		OrgID:       orgID,
		RunID:       run.ID,
		GitHubOwner: owner,
		GitHubRepo:  repo,
		StartedAt:   run.CreatedAt,
	})
	if err != nil {
		b.log.Warn("billing admission failed",
			"org", orgID,
			"run_id", run.ID,
			"owner", owner,
			"repo", repo,
			"error", err,
		)
		emitBillingAdmissionError(emit, err)
		b.markRunState(ctx, runstore.StateFailed, err)
		return flavor, false
	}
	return admission.Flavor, true
}

func emitBillingAdmissionError(emit blocks.Emitter, err error) {
	var insufficient billing.InsufficientCreditsError
	var flavorErr billing.FlavorNotAllowedError
	switch {
	case errors.As(err, &insufficient):
		emit.Error("Usage limit reached", fmt.Sprintf("This organization has %d credit%s available, but this run needs %d reserved credit%s before a sandbox can start. Add credits or adjust billing settings in /settings/org?tab=billing.",
			insufficient.Available, plural(insufficient.Available),
			insufficient.Needed, plural(insufficient.Needed)))
	case errors.As(err, &flavorErr):
		emit.Error("Sandbox flavor unavailable", fmt.Sprintf("This repo is set to `%s`, but the organization plan only allows up to `%s`. Ask an administrator to change the repo flavor under Organization settings > Repositories.", flavorErr.Flavor, flavorErr.MaxFlavor))
	case errors.Is(err, billing.ErrAutoTopupNotConfigured):
		emit.Error("Auto top-up unavailable", "Auto top-up is enabled, but Stripe charging is not configured for this environment. Add credits manually or ask an administrator to update billing settings.")
	default:
		emit.Error("Billing check failed", "Hetchy could not verify this organization's usage balance before starting a sandbox. Try again or ask an administrator to check Billing / Usage.")
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (b *Bot) finishBillingRun(ctx context.Context, runID, state string) {
	if b.billing == nil || !b.billing.Enabled() || runID == "" || !isTerminalRunState(state) {
		return
	}
	if err := b.billing.FinalizeRun(ctx, runID, state, time.Now()); err != nil {
		b.log.Warn("billing finalize failed", "run_id", runID, "state", state, "error", err)
	}
}

func addBillingFlavorLabels(labels map[string]string, flavor billing.Flavor) {
	if labels == nil {
		return
	}
	labels["hetchy_billing_flavor"] = flavor.Code
	labels["hetchy_billing_multiplier"] = strconv.Itoa(flavor.Multiplier)
}

func (b *Bot) sandboxSnapshotForBillingFlavor(flavor billing.Flavor) string {
	if flavor.Code == billing.FlavorStandard {
		return b.cfg.Snapshot
	}
	base := strings.TrimSpace(b.cfg.SnapshotBase)
	version := strings.TrimSpace(b.cfg.SandboxSnapshotVersion)
	if base == "" || version == "" || version == "dev" || version == "unknown" {
		return b.cfg.Snapshot
	}
	return base + "-" + flavor.Code + "-" + version
}
