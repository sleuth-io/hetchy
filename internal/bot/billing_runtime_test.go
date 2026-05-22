package bot

import (
	"errors"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/billing"
)

func TestAdmitBillingForRunDisabled(t *testing.T) {
	flavor, ok := (&Bot{}).admitBillingForRun(t.Context(), "org_1", "owner", "repo", newCaptureEmitter())
	if !ok {
		t.Fatal("admitBillingForRun disabled returned ok=false")
	}
	if flavor.Code != billing.FlavorStandard {
		t.Fatalf("flavor = %q, want standard", flavor.Code)
	}
}

func TestEmitBillingAdmissionError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantTitle string
		wantBody  string
	}{
		{
			name:      "insufficient",
			err:       billing.InsufficientCreditsError{Needed: 2, Available: 1},
			wantTitle: "Usage limit reached",
			wantBody:  "1 credit available",
		},
		{
			name:      "flavor",
			err:       billing.FlavorNotAllowedError{Flavor: billing.FlavorMax, MaxFlavor: billing.FlavorStandard},
			wantTitle: "Sandbox flavor unavailable",
			wantBody:  "only allows",
		},
		{
			name:      "auto topup",
			err:       billing.ErrAutoTopupNotConfigured,
			wantTitle: "Auto top-up unavailable",
			wantBody:  "Stripe charging is not configured",
		},
		{
			name:      "generic",
			err:       errors.New("boom"),
			wantTitle: "Billing check failed",
			wantBody:  "could not verify",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emit := newCaptureEmitter()
			emitBillingAdmissionError(emit, tc.err)
			if len(emit.Calls) != 1 {
				t.Fatalf("calls = %v, want one error call", emit.Calls)
			}
			if !strings.Contains(emit.Calls[0], tc.wantTitle) || !strings.Contains(emit.Calls[0], tc.wantBody) {
				t.Fatalf("call = %q, want title %q and body %q", emit.Calls[0], tc.wantTitle, tc.wantBody)
			}
		})
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1); got != "" {
		t.Fatalf("plural(1) = %q, want empty", got)
	}
	if got := plural(2); got != "s" {
		t.Fatalf("plural(2) = %q, want s", got)
	}
}

func TestFinishBillingRunDisabled(t *testing.T) {
	(&Bot{}).finishBillingRun(t.Context(), "run_1", "completed")
	(&Bot{billing: billing.NewService(nil, nil)}).finishBillingRun(t.Context(), "", "completed")
	(&Bot{billing: billing.NewService(nil, nil)}).finishBillingRun(t.Context(), "run_1", "running")
}

func TestAddBillingFlavorLabels(t *testing.T) {
	addBillingFlavorLabels(nil, billing.MustFlavor(billing.FlavorPlus))
	labels := map[string]string{}
	addBillingFlavorLabels(labels, billing.MustFlavor(billing.FlavorPlus))
	if labels["hetchy_billing_flavor"] != billing.FlavorPlus {
		t.Fatalf("flavor label = %q, want plus", labels["hetchy_billing_flavor"])
	}
	if labels["hetchy_billing_multiplier"] == "" {
		t.Fatal("missing multiplier label")
	}
}

func TestSandboxSnapshotForBillingFlavor(t *testing.T) {
	b := &Bot{cfg: Config{
		SnapshotBase:           "universal-coding",
		Snapshot:               "universal-coding-abc123",
		SandboxSnapshotVersion: "abc123",
	}}
	if got := b.sandboxSnapshotForBillingFlavor(billing.MustFlavor(billing.FlavorStandard)); got != "universal-coding-abc123" {
		t.Fatalf("standard snapshot = %q", got)
	}
	if got := b.sandboxSnapshotForBillingFlavor(billing.MustFlavor(billing.FlavorPlus)); got != "universal-coding-plus-abc123" {
		t.Fatalf("plus snapshot = %q", got)
	}

	dev := &Bot{cfg: Config{Snapshot: "universal-coding:local", SnapshotBase: "universal-coding", SandboxSnapshotVersion: "dev"}}
	if got := dev.sandboxSnapshotForBillingFlavor(billing.MustFlavor(billing.FlavorPlus)); got != "universal-coding:local" {
		t.Fatalf("dev plus fallback snapshot = %q", got)
	}
}
