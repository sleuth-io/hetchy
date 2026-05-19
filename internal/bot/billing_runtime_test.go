package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

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
	addBillingFlavorLabels(nil, billing.MustFlavor(billing.FlavorMax))
	labels := map[string]string{}
	addBillingFlavorLabels(labels, billing.MustFlavor(billing.FlavorMax))
	if labels["hetchy_billing_flavor"] != billing.FlavorMax {
		t.Fatalf("flavor label = %q, want max", labels["hetchy_billing_flavor"])
	}
	if labels["hetchy_billing_multiplier"] == "" {
		t.Fatal("missing multiplier label")
	}
}

func TestResizeSandboxForBillingFlavor(t *testing.T) {
	if err := (&Bot{}).resizeSandboxForBillingFlavor(t.Context(), nil, billing.MustFlavor(billing.FlavorMax)); err != nil {
		t.Fatalf("nil sandbox resize returned error: %v", err)
	}
	if err := (&Bot{}).resizeSandboxForBillingFlavor(t.Context(), &daytona.Sandbox{}, billing.MustFlavor(billing.FlavorStandard)); err != nil {
		t.Fatalf("standard resize returned error: %v", err)
	}

	wantErr := errors.New("resize failed")
	called := false
	b := &Bot{resizeSandboxFn: func(_ context.Context, _ *daytona.Sandbox, flavor billing.Flavor) error {
		called = true
		if flavor.Code != billing.FlavorMax {
			t.Fatalf("resize flavor = %q, want max", flavor.Code)
		}
		return wantErr
	}}
	if err := b.resizeSandboxForBillingFlavor(t.Context(), &daytona.Sandbox{}, billing.MustFlavor(billing.FlavorMax)); !errors.Is(err, wantErr) {
		t.Fatalf("resize error = %v, want %v", err, wantErr)
	}
	if !called {
		t.Fatal("resizeSandboxFn was not called")
	}
}
