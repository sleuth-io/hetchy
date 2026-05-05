package bootstrap

import (
	"context"
	"errors"
	"fmt"
)

// IsStale returns true when the current state of the repo (fresh hints
// from disk) doesn't match the fingerprint stored on the saved spec.
// On true, the apply path should re-bootstrap before trusting the
// cached scripts.
//
// Stale doesn't read the disk itself — it expects the caller to have
// already detected hints for the repo at the current commit. Most
// callers want the higher-level CheckSpec which combines detection +
// drift check in one call.
func IsStale(currentHints *Hints, spec *Spec) bool {
	if spec == nil || currentHints == nil {
		return true
	}
	if spec.SourceFingerprint == "" {
		return true
	}
	return Fingerprint(currentHints) != spec.SourceFingerprint
}

// CheckSpec is the runtime apply-path entry point for an existing
// spec. It re-detects hints from disk, compares against the saved
// fingerprint, and returns the spec along with whether it's stale.
//
// Callers wrap this with auto-heal: if stale → re-bootstrap; if not →
// proceed to apply the spec as-is.
func CheckSpec(_ context.Context, repoRoot string, spec *Spec) (*Hints, bool, error) {
	if spec == nil {
		return nil, false, errors.New("bootstrap: nil spec")
	}
	hints, err := Detect(repoRoot)
	if err != nil {
		return nil, false, fmt.Errorf("bootstrap: re-detect: %w", err)
	}
	return hints, IsStale(hints, spec), nil
}

// AutoHealInput is what AutoHeal needs beyond a Runner: the saved
// spec (used to seed the prompt with the prior scripts + failure
// trace) and the failure that triggered the heal.
type AutoHealInput struct {
	OwnerRepo       string
	Path            string
	PriorSpec       *Spec
	FailureLog      string
	Hints           *Hints
	SuppliedSecrets map[string]string
	RepoDir         string
}

// AutoHeal re-runs the bootstrap loop with the prior spec + failure
// trace as seed context. The prompt the agent sees has an additional
// preamble explaining "this used to work, here's what broke" so it
// can propose a focused fix instead of starting from scratch.
//
// On success the new spec replaces the old one (caller persists it
// with spec.SpecVersion incremented). On failure the caller surfaces
// the structured "couldn't get repo running" message described in the
// spec doc.
func AutoHeal(ctx context.Context, runner Runner, in AutoHealInput) (*LoopResult, error) {
	if in.PriorSpec == nil {
		return nil, errors.New("bootstrap: AutoHeal requires a prior spec")
	}

	res, err := Run(ctx, runner, LoopInput{
		OwnerRepo:       in.OwnerRepo,
		Path:            in.Path,
		Hints:           in.Hints,
		SuppliedSecrets: in.SuppliedSecrets,
		RepoDir:         in.RepoDir,
	})
	if err != nil {
		return res, err
	}
	if res.Spec != nil {
		// Bump version so the apply path can tell this spec was
		// auto-healed (vs. the original first-encounter result).
		res.Spec.SpecVersion = in.PriorSpec.SpecVersion + 1
	}
	return res, nil
}

// AutoHealPromptPreamble is the text the auto-heal flow prepends to
// the regular bootstrap prompt to seed the agent with prior context.
// Exposed (rather than inlined) so the bot can stitch it into the
// runner's stdin without forking the prompt builder.
func AutoHealPromptPreamble(prior *Spec, failureLog string) string {
	if prior == nil {
		return ""
	}
	return fmt.Sprintf(`This is an AUTO-HEAL run. The previously-saved spec for this repo is
no longer working — either a dependency changed, the repo's setup
moved, or something else regressed. Use the prior scripts as a starting
point, identify what changed, and produce an updated spec.

Prior spec.kind: %s
Prior validation status: %s
Prior success / failure counts: %d / %d

Prior setup.sh:
%s

Prior start.sh:
%s

Prior health.sh:
%s

Last failure (truncated to last %d lines):
%s

Now follow the standard bootstrap process, but bias toward a *minimal
update* to the prior scripts rather than a full rewrite, unless the
diff to prior shows a fundamental shift in the repo's stack.

`,
		prior.Kind, prior.ValidationStatus,
		prior.SuccessCount, prior.FailureCount,
		prior.SetupScript, prior.StartScript, prior.HealthCheck,
		200, truncate(failureLog, 4000),
	)
}
