package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/bootstrap"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

const maxFailingBootstrapAutoHealAttempts int32 = 3

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, userRequest, requestID, branch string, opts chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
	model = normalizeClaudeModel(model)
	provider := modelProvider(model)
	bootstrapResult, err := b.agentBootstrapSpec(ctx, sb, repo, oc, agent, requestID, opts, provider, emit)
	if err != nil {
		return "", err
	}
	spec := bootstrapResult.spec

	wd := repoWorkdir(repo.Slug)
	env := map[string]string{
		"SF_REPO":        repo.Slug,
		"SF_WORKDIR":     wd,
		"SF_BASE_BRANCH": repo.BaseBranch,
		"GITHUB_TOKEN":   repo.GitHubToken,
	}
	addAgentEnv(env, b.cfg, agent)
	addJobRunEnv(ctx, env)
	b.addOrgSXVaultEnv(ctx, oc.OrgID, agent, env)
	addDaytonaCacheEnv(env, b.cfg, oc, repo, repo.CacheMounted)

	// Mint the default proof-artifact batch before constructing the
	// prompt, so proof instructions can mention upload slots whenever
	// the S3 path is actually available. This must not depend on a
	// saved bootstrap spec: users can explicitly ask for screenshot
	// proof even when bootstrap generation or persistence failed. The
	// run-scoped token lets the sandbox request more slots up to
	// artifacts.MaxSlots.
	artifactPrefix := fmt.Sprintf("%s/%d/%s", oc.OrgID, repo.RepoID, requestID)
	artifactSlotCount := b.addSandboxGitHubAuthEnv(ctx, artifactPrefix, env, repo, requestID, "initial", opts.ValidateChanges)
	proofInstructions := proofInstructionsForSpec(opts, spec, artifactSlotCount)

	originalPrompt := fmt.Sprintf(agentPromptTemplate,
		repo.Slug, wd, repo.BaseBranch,
		userRequest, conditionalTasksPrompt(opts), proofInstructions, branch, repo.BaseBranch,
	)
	finalPrompt := originalPrompt
	if spec != nil {
		finalPrompt = bootstrap.MergeIntoAgentPrompt(originalPrompt, spec, bootstrap.ValidationArgs{
			OwnerRepo:         repo.Slug,
			Branch:            branch,
			ArtifactSlotCount: artifactSlotCount,
		})
	}

	env["SF_PROMPT_B64"] = base64.StdEncoding.EncodeToString([]byte(finalPrompt))
	// When we have a saved spec, ship its setup/start/stop/health scripts
	// and lessons.md
	// to agent.sh as base64 env vars. agent.sh decodes them before
	// invoking the agent and runs setup → stop → start → poll health, so
	// the validation prompt's "the app is running" assertion holds.
	// Without this step the cached spec is loaded into the prompt
	// but the agent finds a dead port and falls back to figuring out
	// how to start the app from scratch — wasting the bootstrap.
	if spec != nil && spec.SetupScript != "" && spec.StartScript != "" && spec.HealthCheck != "" {
		env["SF_SPEC_SETUP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.SetupScript))
		env["SF_SPEC_START_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StartScript))
		env["SF_SPEC_HEALTH_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.HealthCheck))
		if spec.StopScript != "" {
			env["SF_SPEC_STOP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StopScript))
		}
		if spec.LessonsMD != "" {
			env["SF_SPEC_LESSONS_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.LessonsMD))
		}
	}
	b.addRuntimeEnv(env, oc, model, requestID)
	sessionID := "agent-" + requestID
	prURL, err := b.runScriptForRequest(ctx, sb, sessionID, "agent", agentScript, env, emit)
	b.writeBackOpenAICodexAuthJSON(ctx, sb, oc, requestID)
	if err == nil && prURL != "" {
		b.markRunFinalizing(ctx)
		prURL, err = b.validateReportedPR(ctx, repo, branch, repo.BaseBranch, prURL)
	}
	if err == nil && prURL != "" && spec != nil {
		// Post-success reflection: read /tmp/hetchy-spec/improved/ to
		// see if the agent flagged any setup/start/stop/health/lessons changes that
		// would help future tasks. Best-effort — failures here never
		// affect the PR. Runs in a fresh session because runScript
		// deleted the agent's session in its defer. Deferred behind
		// the user-visible result when the caller supports it: the
		// reads take ~10-30s and the PR is already verified.
		deferOrRunPostPRHousekeeping(ctx, func(hctx context.Context) {
			b.applySpecImprovements(hctx, sb, sessionID, spec, repo, emit)

			// Promote the spec back to Validated on a clean run. Two
			// reasons this matters: (1) applySpecImprovements demotes
			// the row to Stale before saving improved scripts, so a
			// next-task verification needs a way to flip it back when
			// the new scripts work end-to-end; (2) success_count is the
			// signal AutoHeal's preamble uses to frame "this is attempt
			// N" — without an increment per success, a once-failing /
			// once-succeeded spec keeps looking like it has never run.
			// Best-effort: errors here are logged but don't fail the
			// PR.
			if mErr := b.bootstrap.MarkApplied(hctx, repo.InstallID, repo.RepoID, spec.Path,
				bootstrap.StatusValidated, spec.SuccessCount+1, spec.FailureCount); mErr != nil {
				b.log.Warn("mark spec applied", "error", mErr, "repo", repo.Slug)
			}
		})
	}
	return prURL, err
}

type agentBootstrapResult struct {
	spec *bootstrap.Spec
}

func (b *Bot) agentBootstrapSpec(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, requestID string, opts chatTaskOptions, provider modelProviderKind, emit blocks.Emitter) (agentBootstrapResult, error) {
	// ValidateChanges=false is the user's explicit "skip end-to-end
	// testing" opt-out from the new-chat UI. We honour it by not
	// running bootstrap (which can take minutes on a fresh repo) and
	// not merging the validation prompt.
	var out agentBootstrapResult
	if !opts.ValidateChanges || bootstrapSkippedFromContext(ctx) || b.bootstrap == nil || repo.InstallID == 0 || repo.RepoID == 0 {
		return out, nil
	}
	canRunBootstrap := provider == modelProviderAnthropic || hasAnthropicCredentials(oc)
	if !canRunBootstrap {
		if provider == modelProviderOpenAI {
			emit.Notify("Bootstrap skipped", "Bootstrap runs Claude Code internally; without an Anthropic credential Codex will validate the change without the saved repo bootstrap step.")
		}
		return out, nil
	}
	spec, err := b.ensureBootstrapSpec(ctx, sb, repo, oc, agent, requestID, emit)
	if err == nil {
		return agentBootstrapResult{spec: spec}, nil
	}
	if ctx.Err() != nil {
		return out, err
	}
	// Bootstrap is best-effort: a failure here logs + continues with the
	// unmodified prompt. Future tasks against this repo will retry.
	b.log.Warn("bootstrap failed; proceeding without spec",
		"request_id", requestID, "repo", repo.Slug, "error", err)
	emit.Notify("Bootstrap skipped", bootstrapSkippedMessage(err))
	return out, nil
}

// ensureBootstrapSpec returns the saved spec for repo, running the
// bootstrap loop on first encounter. Bootstrap clones into the same
// workdir agent.sh will use; agent.sh detects the existing checkout and
// skips its own clone, so the work happens once.
//
// Caller is expected to gate on whether bootstrap is appropriate (a
// GitHub App-resolved repo with a stable install + repo id); this method
// assumes those preconditions hold.
func (b *Bot) ensureBootstrapSpec(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, requestID string, emit blocks.Emitter) (*bootstrap.Spec, error) {
	spec, err := b.bootstrap.GetSpec(ctx, repo.InstallID, repo.RepoID, "")
	switch {
	case err == nil:
		if !shouldRefreshExistingBootstrapSpec(spec) {
			if failingBootstrapAutoHealCapped(false, false, spec) {
				b.notifyBootstrapAutoHealCapped(repo, requestID, spec, emit)
			}
			return spec, nil
		}
		refreshed, refreshErr := b.refreshExistingBootstrapSpec(ctx, sb, repo, oc, agent, requestID, spec, emit)
		if refreshErr != nil {
			b.log.Warn("bootstrap drift check failed; using saved spec",
				"request_id", requestID, "repo", repo.Slug, "error", refreshErr)
			emit.Notify("Bootstrap check skipped", "Hetchy could not refresh the saved repo setup spec before this run, so it will reuse the last saved version.")
			return spec, nil
		}
		return refreshed, nil
	case errors.Is(err, bootstrap.ErrNotFound):
		// Fall through and bootstrap.
	default:
		return nil, fmt.Errorf("get spec: %w", err)
	}

	emit.Notify("First-time bootstrap",
		fmt.Sprintf("`%s` is new to Hetchy — figuring out how to run it end-to-end. This one-time analysis uses Opus with high effort, so it adds a few minutes to the first task; subsequent tasks reuse the result.", repo.Slug))

	sessionID := "bootstrap-" + requestID
	if err := b.createBootstrapSession(ctx, sb, sessionID); err != nil {
		return nil, fmt.Errorf("create bootstrap session: %w", err)
	}
	defer func() {
		b.deleteSandboxSession(sb, sessionID)
	}()

	wd := repoWorkdir(repo.Slug)
	cloneEnv := map[string]string{
		"SF_REPO":        repo.Slug,
		"SF_WORKDIR":     wd,
		"SF_BASE_BRANCH": repo.BaseBranch,
		"GITHUB_TOKEN":   repo.GitHubToken,
	}
	// Feed the cache state into setup-clone too so the bootstrap path
	// benefits from the warm repo checkout. Without this the bootstrap
	// clone always falls through to a full network fetch even when the
	// cache volume is mounted, which is exactly the case we are
	// optimising for.
	addDaytonaCacheEnv(cloneEnv, b.cfg, oc, repo, repo.CacheMounted)
	if err := b.runBootstrapInlineScript(ctx, sb, sessionID, "setup-clone", setupCloneScript, cloneEnv, emit); err != nil {
		return nil, fmt.Errorf("setup-clone: %w", err)
	}

	hints, tempRoot, err := b.detectBootstrapHints(ctx, sb, sessionID, wd)
	if err != nil {
		return nil, fmt.Errorf("detect: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()

	suppliedSecrets, err := b.bootstrap.GetSecrets(ctx, repo.InstallID, repo.RepoID, "")
	if err != nil {
		return nil, fmt.Errorf("get secrets: %w", err)
	}

	authKey, authVal := claudeAuthEnv(oc)
	b.log.Info("claude auth", "method", authKey, "token", maskToken(authVal), "request_id", sessionID)
	baseEnv := map[string]string{
		authKey:                authVal,
		"GITHUB_TOKEN":         repo.GitHubToken,
		"HETCHY_CLAUDE_MODEL":  string(ClaudeModelOpus),
		"HETCHY_CLAUDE_EFFORT": "high",
	}
	addAgentEnv(baseEnv, b.cfg, agent)
	addDaytonaCacheEnv(baseEnv, b.cfg, oc, repo, repo.CacheMounted)
	runner := &botRunner{
		b:         b,
		sb:        sb,
		sessionID: sessionID,
		emit:      emit,
		baseEnv:   baseEnv,
	}
	res, err := b.runBootstrapLoop(ctx, runner, bootstrap.LoopInput{
		OwnerRepo:       repo.Slug,
		Hints:           hints,
		SuppliedSecrets: suppliedSecrets,
		RepoDir:         wd,
	})
	// On ErrLoopFailed, bootstrap.Run still returns a partial result
	// (any artifacts the agent produced + the captured transcript).
	// Persisting that as a StatusFailing row keeps the trace available
	// for AutoHeal on the next run — without this, the first failure
	// for a repo leaves nothing in the DB and the repo can never
	// auto-heal because AutoHealInput requires a non-nil PriorSpec.
	if err != nil && errors.Is(err, bootstrap.ErrLoopFailed) && res != nil {
		b.persistFailingBootstrap(ctx, res, repo, hints)
	}
	if err != nil {
		return nil, fmt.Errorf("bootstrap.Run: %w", err)
	}
	if res == nil || res.Spec == nil {
		return nil, errors.New("bootstrap produced no spec")
	}

	spec, err = b.saveBootstrapSpecResult(ctx, res, repo)
	if err != nil {
		return nil, fmt.Errorf("save spec: %w", err)
	}

	emit.Notify("Bootstrap complete",
		fmt.Sprintf("Saved a `%s` setup for `%s` (status: %s). The agent will now run with end-to-end validation.",
			spec.Kind, repo.Slug, spec.ValidationStatus))
	return spec, nil
}

func (b *Bot) refreshExistingBootstrapSpec(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, requestID string, spec *bootstrap.Spec, emit blocks.Emitter) (*bootstrap.Spec, error) {
	sessionID := "bootstrap-check-" + requestID
	if err := b.createBootstrapSession(ctx, sb, sessionID); err != nil {
		return nil, fmt.Errorf("create bootstrap check session: %w", err)
	}
	defer func() {
		b.deleteSandboxSession(sb, sessionID)
	}()

	wd := repoWorkdir(repo.Slug)
	if err := b.prepareBootstrapCheckout(ctx, sb, sessionID, repo, oc, emit); err != nil {
		return nil, err
	}
	hints, tempRoot, err := b.detectBootstrapHints(ctx, sb, sessionID, wd)
	if err != nil {
		return nil, fmt.Errorf("detect: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()

	stale := bootstrap.IsStale(hints, spec)
	generationUpgrade := bootstrap.NeedsGenerationUpgrade(spec)
	needsHeal := shouldAutoHealExistingBootstrapSpec(stale, generationUpgrade, spec)
	if !needsHeal {
		if failingBootstrapAutoHealCapped(stale, generationUpgrade, spec) {
			b.notifyBootstrapAutoHealCapped(repo, requestID, spec, emit)
		}
		return spec, nil
	}

	reason := "the repo setup spec is stale"
	if generationUpgrade {
		reason = fmt.Sprintf("the saved repo setup spec was produced by bootstrap generation %d; current generation is %d",
			spec.BootstrapGeneration, bootstrap.CurrentBootstrapGeneration)
	} else if spec.ValidationStatus == bootstrap.StatusFailing {
		reason = "the saved repo setup spec is failing"
	}
	emit.Notify("Bootstrap auto-heal", reason+"; regenerating setup/start/health before the agent runs.")

	suppliedSecrets, err := b.bootstrap.GetSecrets(ctx, repo.InstallID, repo.RepoID, "")
	if err != nil {
		return nil, fmt.Errorf("get secrets: %w", err)
	}
	runner, err := b.newBootstrapRunner(ctx, sb, sessionID, repo, oc, agent, emit)
	if err != nil {
		return nil, err
	}
	failureLog := spec.BootstrapLog
	if generationUpgrade {
		prefix := fmt.Sprintf("bootstrap generation upgrade required: saved_generation=%d current_generation=%d\n",
			spec.BootstrapGeneration, bootstrap.CurrentBootstrapGeneration)
		if failureLog != "" {
			failureLog = prefix + "\n" + failureLog
		} else {
			failureLog = prefix
		}
	}
	if failureLog == "" {
		failureLog = fmt.Sprintf("stale=%t generation_upgrade=%t validation_status=%s current_fingerprint=%s saved_fingerprint=%s",
			stale, generationUpgrade, spec.ValidationStatus, bootstrap.Fingerprint(hints), spec.SourceFingerprint)
	}
	res, err := b.runBootstrapAutoHeal(ctx, runner, bootstrap.AutoHealInput{
		OwnerRepo:       repo.Slug,
		Path:            spec.Path,
		PriorSpec:       spec,
		FailureLog:      failureLog,
		Hints:           hints,
		SuppliedSecrets: suppliedSecrets,
		RepoDir:         wd,
	})
	if err != nil && errors.Is(err, bootstrap.ErrLoopFailed) && res != nil {
		// Save the failed attempt at the current bootstrap generation so a
		// failed upgrade consumes the generation bump; future tasks then use
		// the normal failing-spec retry cap instead of bypassing forever.
		b.persistFailingBootstrap(ctx, res, repo, hints)
		if generationUpgrade {
			b.log.Warn("bootstrap generation upgrade failed",
				"repo", repo.Slug,
				"request_id", requestID,
				"saved_generation", spec.BootstrapGeneration,
				"current_generation", bootstrap.CurrentBootstrapGeneration,
				"prior_failure_count", spec.FailureCount,
				"max_attempts", maxFailingBootstrapAutoHealAttempts)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("bootstrap.AutoHeal: %w", err)
	}
	if res == nil || res.Spec == nil {
		return nil, errors.New("bootstrap auto-heal produced no spec")
	}
	refreshed, err := b.saveBootstrapSpecResult(ctx, res, repo)
	if err != nil {
		return nil, fmt.Errorf("save auto-healed spec: %w", err)
	}
	emit.Notify("Bootstrap auto-heal complete",
		fmt.Sprintf("Saved `%s` setup version %d for `%s` (status: %s).",
			refreshed.Kind, refreshed.SpecVersion, repo.Slug, refreshed.ValidationStatus))
	return refreshed, nil
}

func shouldRefreshExistingBootstrapSpec(spec *bootstrap.Spec) bool {
	if spec == nil {
		return false
	}
	if bootstrap.NeedsGenerationUpgrade(spec) {
		return true
	}
	switch spec.ValidationStatus {
	case bootstrap.StatusStale:
		return true
	case bootstrap.StatusFailing:
		return spec.FailureCount < maxFailingBootstrapAutoHealAttempts
	case bootstrap.StatusValidated, bootstrap.StatusPartial:
		return false
	default:
		return false
	}
}

func shouldAutoHealExistingBootstrapSpec(stale, generationUpgrade bool, spec *bootstrap.Spec) bool {
	if spec == nil {
		return false
	}
	if generationUpgrade {
		return true
	}
	if stale || spec.ValidationStatus == bootstrap.StatusStale {
		return true
	}
	if spec.ValidationStatus != bootstrap.StatusFailing {
		return false
	}
	return spec.FailureCount < maxFailingBootstrapAutoHealAttempts
}

func failingBootstrapAutoHealCapped(stale, generationUpgrade bool, spec *bootstrap.Spec) bool {
	return spec != nil &&
		!stale &&
		!generationUpgrade &&
		spec.ValidationStatus == bootstrap.StatusFailing &&
		spec.FailureCount >= maxFailingBootstrapAutoHealAttempts
}

func (b *Bot) notifyBootstrapAutoHealCapped(repo repoCtx, requestID string, spec *bootstrap.Spec, emit blocks.Emitter) {
	b.log.Warn("bootstrap auto-heal skipped after repeated failures",
		"repo", repo.Slug,
		"request_id", requestID,
		"failure_count", spec.FailureCount,
		"max_attempts", maxFailingBootstrapAutoHealAttempts)
	emit.Notify("Bootstrap auto-heal skipped",
		fmt.Sprintf("The saved repo setup spec is still failing after %d attempts, so Hetchy will reuse it instead of retrying auto-heal on every run.", spec.FailureCount))
}

func (b *Bot) prepareBootstrapCheckout(ctx context.Context, sb *daytona.Sandbox, sessionID string, repo repoCtx, oc orgcfg.Config, emit blocks.Emitter) error {
	wd := repoWorkdir(repo.Slug)
	cloneEnv := map[string]string{
		"SF_REPO":        repo.Slug,
		"SF_WORKDIR":     wd,
		"SF_BASE_BRANCH": repo.BaseBranch,
		"GITHUB_TOKEN":   repo.GitHubToken,
	}
	addDaytonaCacheEnv(cloneEnv, b.cfg, oc, repo, repo.CacheMounted)
	if err := b.runBootstrapInlineScript(ctx, sb, sessionID, "setup-clone", setupCloneScript, cloneEnv, emit); err != nil {
		return fmt.Errorf("setup-clone: %w", err)
	}
	return nil
}

func (b *Bot) newBootstrapRunner(ctx context.Context, sb *daytona.Sandbox, sessionID string, repo repoCtx, oc orgcfg.Config, agent agents.Profile, emit blocks.Emitter) (bootstrap.Runner, error) {
	authKey, authVal := claudeAuthEnv(oc)
	b.log.Info("claude auth", "method", authKey, "token", maskToken(authVal), "request_id", sessionID)
	baseEnv := map[string]string{
		authKey:                authVal,
		"GITHUB_TOKEN":         repo.GitHubToken,
		"HETCHY_CLAUDE_MODEL":  string(ClaudeModelOpus),
		"HETCHY_CLAUDE_EFFORT": "high",
	}
	addAgentEnv(baseEnv, b.cfg, agent)
	addDaytonaCacheEnv(baseEnv, b.cfg, oc, repo, repo.CacheMounted)
	return &botRunner{
		b:         b,
		sb:        sb,
		sessionID: sessionID,
		emit:      emit,
		baseEnv:   baseEnv,
	}, nil
}

func (b *Bot) createBootstrapSession(ctx context.Context, sb *daytona.Sandbox, sessionID string) error {
	if b.createBootstrapSessionFn != nil {
		return b.createBootstrapSessionFn(ctx, sb, sessionID)
	}
	if sb == nil || sb.Process == nil {
		return errors.New("sandbox process not configured")
	}
	return b.createSandboxSessionWithRetry(ctx, sb.ID, sb.Process, sessionID, "bootstrap")
}

func (b *Bot) createSandboxSessionWithRetry(ctx context.Context, sandboxID string, proc sandboxSessionCreator, sessionID, purpose string) error {
	sawTransient := false
	err := b.retryWithBackoff(ctx, "sandbox create session", func() error {
		err := proc.CreateSession(ctx, sessionID)
		if err == nil {
			return nil
		}
		if sawTransient && isDaytonaSessionAlreadyExists(err) {
			if b.log != nil {
				b.log.Info("daytona session already exists after retry",
					"sandbox", sandboxID, "session", sessionID, "purpose", purpose)
			}
			return nil
		}
		if isTransientError(err) {
			sawTransient = true
		}
		return err
	})
	if err != nil && b.log != nil {
		b.log.Warn("daytona create session failed",
			"sandbox", sandboxID, "session", sessionID, "purpose", purpose, "error", err)
	}
	return err
}

func (b *Bot) runBootstrapInlineScript(ctx context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, emit blocks.Emitter) error {
	if b.runInlineScriptFn != nil {
		return b.runInlineScriptFn(ctx, sb, sessionID, label, scriptBody, env, emit)
	}
	return b.runInlineScript(ctx, sb, sessionID, label, scriptBody, env, emit)
}

func (b *Bot) detectBootstrapHints(ctx context.Context, sb *daytona.Sandbox, sessionID, workdir string) (*bootstrap.Hints, string, error) {
	if b.detectViaSandboxFn != nil {
		return b.detectViaSandboxFn(ctx, sb, sessionID, workdir)
	}
	return b.detectViaSandbox(ctx, sb, sessionID, workdir)
}

func (b *Bot) runBootstrapLoop(ctx context.Context, runner bootstrap.Runner, in bootstrap.LoopInput) (*bootstrap.LoopResult, error) {
	if b.bootstrapRunFn != nil {
		return b.bootstrapRunFn(ctx, runner, in)
	}
	return bootstrap.Run(ctx, runner, in)
}

func (b *Bot) runBootstrapAutoHeal(ctx context.Context, runner bootstrap.Runner, in bootstrap.AutoHealInput) (*bootstrap.LoopResult, error) {
	if b.bootstrapAutoHealFn != nil {
		return b.bootstrapAutoHealFn(ctx, runner, in)
	}
	return bootstrap.AutoHeal(ctx, runner, in)
}

// persistFailingBootstrap saves a StatusFailing spec row from a
// partial bootstrap result so AutoHeal has prior context to bias on
// the next attempt. Best-effort: any error here just gets logged —
// we don't propagate, because the caller is already returning the
// original ErrLoopFailed.
//
// Two non-obvious behaviours:
//   - Partial scripts (whatever the agent wrote to setup.sh /
//     start.sh / health.sh before the loop tripped) are pulled out of
//     LoopResult.PartialScripts and persisted on the row, so
//     AutoHealPromptPreamble surfaces them as "Prior X.sh:" sections
//     instead of empty placeholders.
//   - failure_count is incremented at the SQL layer (not replaced),
//     so retry counters accumulate across attempts. Today the bot
//     only writes a failing row on first encounter, but a future
//     retry-on-failure path needs the column to be monotonic.
func (b *Bot) persistFailingBootstrap(ctx context.Context, res *bootstrap.LoopResult, repo repoCtx, hints *bootstrap.Hints) {
	kind := ""
	var requiredSecrets []bootstrap.Secret
	var deferred []string
	var capability bootstrap.ValidationCapability
	if res.Manifest != nil {
		kind = res.Manifest.Kind
		requiredSecrets = res.Manifest.RequiredSecrets
		deferred = res.Manifest.DeferredCapabilities
		capability = res.Manifest.ValidationCapability
	}
	failingSpec := &bootstrap.Spec{
		InstallationID:       repo.InstallID,
		RepoID:               repo.RepoID,
		SpecVersion:          1,
		BootstrapGeneration:  bootstrap.CurrentBootstrapGeneration,
		Kind:                 kind,
		SetupScript:          res.PartialScripts.Setup,
		StartScript:          res.PartialScripts.Start,
		StopScript:           res.PartialScripts.Stop,
		HealthCheck:          res.PartialScripts.Health,
		LessonsMD:            res.PartialScripts.Lessons,
		RequiredSecrets:      requiredSecrets,
		DeferredCapabilities: deferred,
		ValidationCapability: capability,
		SourceFingerprint:    bootstrap.Fingerprint(hints),
		ValidationStatus:     bootstrap.StatusFailing,
		BootstrapLog:         truncateLogTail(res.Log),
	}
	if err := b.bootstrap.SaveFailingSpec(ctx, failingSpec); err != nil {
		b.log.Warn("save failing spec", "repo", repo.Slug, "error", err)
	}
}

// truncateLogTail returns the trailing 32 KB of s, walking forward to
// the next valid UTF-8 lead byte so the column write doesn't reject
// on an invalid byte sequence (Postgres TEXT requires valid UTF-8).
// The store layer also sanitizes before DB writes; this pre-store
// normalization keeps any intermediate copies valid and is idempotent.
// Shared by ensureBootstrapSpec and persistFailingBootstrap.
func truncateLogTail(s string) string {
	const max = 32 * 1024
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= max {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return "...(truncated)...\n" + s[start:]
}

// runInlineScript writes scriptBody to the sandbox via heredoc and runs
// it with env vars prefixed, reusing an existing session. It mirrors
// runScript's prologue but stays in-process — bootstrap shares one
// session across multiple steps so the working directory and shell
// state persist across invocations.
func (b *Bot) runInlineScript(ctx context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, emit blocks.Emitter) error {
	scriptPath := "/tmp/sf-" + label + ".sh"
	body := strings.TrimRight(scriptBody, "\n")
	writeCmd := heredocWriteCmd(scriptPath, body, true)
	if _, err := b.shLines(ctx, sb.ID, sb.Process, sessionID, label+"-write", writeCmd, 30*time.Second, 0, true, func(string) {}); err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}

	// Sort env keys so log-diffing identical commands across runs lines
	// up; Go map iteration is randomised.
	var prefix strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		prefix.WriteString(k)
		prefix.WriteByte('=')
		prefix.WriteString(shellQuote(env[k]))
		prefix.WriteByte(' ')
	}
	runCmd := prefix.String() + "bash " + scriptPath

	router := newBootstrapLineRouter(emit)
	if _, err := b.shLines(ctx, sb.ID, sb.Process, sessionID, label+"-run", runCmd, 5*time.Minute, 0, false, router.Line); err != nil {
		router.Fail(label + " failed")
		return fmt.Errorf("run %s: %w", label, err)
	}
	router.Done(label + " complete")
	return nil
}

// runFollowUp resumes work in an existing sandbox. The installation
// token is freshly minted and passed per-run (not just at sandbox-create
// time) so a token rotation or a re-installed App takes effect on the
// very next follow-up rather than only on a freshly-created sandbox.
func (b *Bot) runFollowUp(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, userRequest, requestID string, opts chatTaskOptions, model ClaudeModel, mode followUpMode, emit blocks.Emitter) (string, error) {
	model = normalizeClaudeModel(model)
	mode = normalizeFollowUpMode(mode)
	changeMode := mode == followUpModeChange
	var spec *bootstrap.Spec
	if changeMode && opts.ValidateChanges && b.bootstrap != nil && repo.InstallID != 0 && repo.RepoID != 0 {
		s, err := b.bootstrap.GetSpec(ctx, repo.InstallID, repo.RepoID, "")
		switch {
		case err == nil:
			spec = s
		case errors.Is(err, bootstrap.ErrNotFound):
			// No saved spec yet; follow-up remains a normal PR update.
		default:
			b.log.Warn("get bootstrap spec for follow-up failed",
				"request_id", requestID, "repo", repo.Slug, "error", err)
		}
	}

	wd := repoWorkdir(repo.Slug)
	env := map[string]string{
		"SF_REPO":      repo.Slug,
		"SF_WORKDIR":   wd,
		"SF_BRANCH":    rec.Branch,
		"GITHUB_TOKEN": repo.GitHubToken,
	}
	addAgentEnv(env, b.cfg, agent)
	addJobRunEnv(ctx, env)

	artifactPrefix := fmt.Sprintf("%s/%d/%s/followup-%s", oc.OrgID, repo.RepoID, rec.ThreadID, requestID)
	// Follow-ups can still need fresh proof links. Issue a new
	// run-scoped batch under a follow-up prefix so keys don't collide
	// with the initial request.
	artifactSlotCount := b.addSandboxGitHubAuthEnv(ctx, artifactPrefix, env, repo, requestID, "followup", changeMode && opts.ValidateChanges)
	prompt := buildFollowUpPrompt(repo.Slug, rec, userRequest, spec, artifactSlotCount, opts, mode)
	env["SF_PROMPT_B64"] = base64.StdEncoding.EncodeToString([]byte(prompt))
	env["HETCHY_FOLLOWUP_MODE"] = string(mode)
	if !changeMode {
		env["HETCHY_SKIP_CACHE_SAVE"] = "1"
		env["HETCHY_SKIP_SX_INSTALL"] = "1"
	} else {
		b.addOrgSXVaultEnv(ctx, oc.OrgID, agent, env)
	}
	b.addRuntimeEnv(env, oc, model, requestID)
	// A follow-up lands in a stopped or archived sandbox where any
	// background processes from the original run are gone — including the
	// `start.sh &` invocation that brought the app up. Without this
	// step the agent's validation prompt assumes "the app is running"
	// against a dead port. Ship the saved spec so followup.sh can
	// re-run setup → stop → start → poll health, mirroring agent.sh. Errors
	// here are best-effort: a missing spec just means the follow-up
	// runs without a live app, same as before.
	if changeMode && spec != nil && spec.SetupScript != "" && spec.StartScript != "" && spec.HealthCheck != "" {
		env["SF_SPEC_SETUP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.SetupScript))
		env["SF_SPEC_START_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StartScript))
		env["SF_SPEC_HEALTH_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.HealthCheck))
		if spec.StopScript != "" {
			env["SF_SPEC_STOP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StopScript))
		}
		if spec.LessonsMD != "" {
			env["SF_SPEC_LESSONS_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.LessonsMD))
		}
	}
	prURL, err := b.runScriptForRequest(ctx, sb, "followup-"+requestID, "followup", followupScript, env, emit)
	b.writeBackOpenAICodexAuthJSON(ctx, sb, oc, requestID)
	if err != nil {
		return "", err
	}
	if !changeMode {
		if prURL != "" {
			b.log.Warn("non-change follow-up reported PR URL; ignoring for conversation result",
				"request_id", requestID, "mode", mode, "pr", prURL)
		}
		return "", nil
	}
	if prURL == "" {
		return "", nil
	}
	b.markRunFinalizing(ctx)
	prURL, err = b.validateReportedPR(ctx, repo, rec.Branch, "", prURL)
	if err == nil && prURL != "" && spec != nil {
		// Same deferred reflection as runAgent — see the comment there.
		deferOrRunPostPRHousekeeping(ctx, func(hctx context.Context) {
			sessionID := "reflect-followup-" + requestID
			b.applySpecImprovements(hctx, sb, sessionID, spec, repo, emit)
			if mErr := b.bootstrap.MarkApplied(hctx, repo.InstallID, repo.RepoID, spec.Path,
				bootstrap.StatusValidated, spec.SuccessCount+1, spec.FailureCount); mErr != nil {
				b.log.Warn("mark spec applied", "error", mErr, "repo", repo.Slug)
			}
		})
	}
	return prURL, err
}

func (b *Bot) runScriptForRequest(ctx context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, emit blocks.Emitter) (string, error) {
	if b.runScriptFn != nil {
		return b.runScriptFn(ctx, sb, sessionID, label, scriptBody, env, emit)
	}
	return b.runScript(ctx, sb, sessionID, label, scriptBody, env, emit)
}

// runScript writes scriptBody to /tmp/sf-<label>.sh inside the sandbox
// and runs it with the given env vars prefixed on the command line. It
// streams Block-shaped updates via emit (sandbox bootstrap goes into a
// "setup" block; the runtime JSONL output is parsed line-by-line into
// typed blocks). Returns the PR URL extracted from the final assistant
// message, or an empty string when the agent completed successfully but
// answered without creating a pull request.
func (b *Bot) runScript(ctx context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, emit blocks.Emitter) (string, error) {
	if err := b.createSandboxSessionWithRetry(ctx, sb.ID, sb.Process, sessionID, label); err != nil {
		return "", fmt.Errorf("%w: create session: %w", errAgentSetupBeforeRuntime, err)
	}
	b.markRunSession(ctx, sessionID)

	// Writing the script generates no user-visible output; pass a noop
	// line handler so it doesn't open a stray block.
	//
	// Heredoc terminator MUST sit on its own line. The embedded scripts
	// end with "\n" today, but an edit that drops the trailing newline
	// would put the terminator on the same line as the last script line
	// and the heredoc would hang waiting for a bare terminator. Trim any
	// trailing newlines so the construction is invariant to the script
	// body's exact whitespace. heredocWriteCmd derives the terminator
	// from the body's sha256 so a body line containing the terminator
	// can't silently truncate the file.
	scriptPath := "/tmp/sf-" + label + ".sh"
	body := strings.TrimRight(scriptBody, "\n")
	writeCmd := heredocWriteCmd(scriptPath, body, true)
	if _, err := b.shLines(ctx, sb.ID, sb.Process, sessionID, "write-script", writeCmd, 30*time.Second, 0, true, func(string) {}); err != nil {
		return "", fmt.Errorf("%w: write script: %w", errAgentSetupBeforeRuntime, err)
	}

	env = maps.Clone(env)
	if err := b.materializeLargeRunEnv(ctx, sb.ID, sb.Process, sessionID, label, env); err != nil {
		return "", fmt.Errorf("%w: materialize env: %w", errAgentSetupBeforeRuntime, err)
	}

	// Sort env keys so the resulting command line is deterministic; Go
	// map iteration is randomised, which makes log-diffing two runs of
	// the same script unnecessarily noisy.
	var prefix strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		prefix.WriteString(k)
		prefix.WriteByte('=')
		prefix.WriteString(shellQuote(env[k]))
		prefix.WriteByte(' ')
	}
	runID := currentAgentRunID(ctx)
	if runID == "" {
		runID = stableAgentRunID(sb.ID, sessionID, label)
	}
	runCmd := prefix.String() + framedAgentCommand(runID, scriptPath)

	// Heartbeat: emit a "still working" update every minute so the
	// user knows the agent is alive during long runs.
	stop := startHeartbeat(ctx, emit, "Still working", "Agent has been running for %v — still in progress.")
	defer stop()

	router := newAgentLineRouter(emit)
	durable := agentRunEmitterFromContext(ctx)
	framed := newHetchyFrameRouter(runID, func(line string) {
		if durable != nil {
			durable.BeginBatch()
		}
		router.Line(line)
	}, func(cursor int64) {
		if durable != nil {
			_ = durable.FlushBatch(cursor)
		} else {
			b.markRunCursor(ctx, cursor)
		}
	})
	if _, err := b.shLines(ctx, sb.ID, sb.Process, sessionID, "run-script", runCmd, 60*time.Minute, 15*time.Minute, true, framed.Line); err != nil {
		reachedAgent := router.ReachedAgent()
		router.Abort()
		if !reachedAgent {
			return "", fmt.Errorf("%w: %w", errAgentSetupBeforeRuntime, err)
		}
		return "", err
	}
	if durable != nil {
		if err := durable.Err(); err != nil {
			router.Abort()
			return "", fmt.Errorf("%w: persist agent run events: %w", errAgentRunDurability, err)
		}
	}
	if !framed.SeenBegin() {
		router.Abort()
		return "", fmt.Errorf("agent run %s produced no Hetchy begin sentinel", runID)
	}
	prURL := router.Finish()
	if durable != nil {
		if err := durable.Err(); err != nil {
			router.Abort()
			return "", fmt.Errorf("%w: persist agent run events: %w", errAgentRunDurability, err)
		}
	}
	if prURL == "" {
		// Distinguish "setup never reached the runtime" from "the runtime ran
		// but didn't post a URL". Both surface here but they need
		// different remediation, so on-call shouldn't have to tail
		// logs to tell them apart.
		if !router.ReachedAgent() {
			return "", fmt.Errorf("%w: setup script for %s exited before invoking the agent runtime — check the sandbox setup block for the failing step", errAgentSetupBeforeRuntime, label)
		}
		return "", nil
	}
	return prURL, nil
}
