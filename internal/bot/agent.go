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

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, userRequest, requestID, branch string, opts chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
	model = normalizeClaudeModel(model)
	provider := modelProvider(model)
	var spec *bootstrap.Spec
	// ValidateChanges=false is the user's explicit "skip end-to-end
	// testing" opt-out from the new-chat UI. We honour it by not
	// running bootstrap (which can take minutes on a fresh repo) and
	// not merging the validation prompt.
	canRunBootstrap := provider == modelProviderAnthropic || hasAnthropicCredentials(oc)
	if opts.ValidateChanges && !bootstrapSkippedFromContext(ctx) && b.bootstrap != nil && repo.InstallID != 0 && repo.RepoID != 0 && canRunBootstrap {
		s, err := b.ensureBootstrapSpec(ctx, sb, repo, oc, requestID, emit)
		if err != nil {
			if ctx.Err() != nil {
				return "", err
			}
			// Bootstrap is best-effort: a failure here logs + continues
			// with the unmodified prompt. Future tasks against this repo
			// will retry. Hard-failing would block users on every repo
			// we don't yet have a spec for, even when the change in
			// flight has nothing to do with running the app.
			b.log.Warn("bootstrap failed; proceeding without spec",
				"request_id", requestID, "repo", repo.Slug, "error", err)
			emit.Notify("Bootstrap skipped", bootstrapSkippedMessage(err))
		} else {
			spec = s
		}
	} else if opts.ValidateChanges && provider == modelProviderOpenAI && !canRunBootstrap && b.bootstrap != nil && repo.InstallID != 0 && repo.RepoID != 0 {
		emit.Notify("Bootstrap skipped", "Claude credentials are not configured, so Codex will validate the change without the saved repo bootstrap step.")
	}

	wd := repoWorkdir(repo.Slug)
	env := map[string]string{
		"SF_REPO":        repo.Slug,
		"SF_WORKDIR":     wd,
		"SF_BASE_BRANCH": repo.BaseBranch,
		"GITHUB_TOKEN":   repo.GitHubToken,
	}
	addAgentEnv(env, b.cfg, agent)
	addDaytonaCacheEnv(env, b.cfg, oc, repo, repo.CacheMounted)

	// Mint the default proof-artifact batch before constructing the
	// prompt, so proof instructions can mention upload slots whenever
	// the S3 path is actually available. This must not depend on a
	// saved bootstrap spec: users can explicitly ask for screenshot
	// proof even when bootstrap generation or persistence failed. The
	// run-scoped token lets the sandbox request more slots up to
	// artifacts.MaxSlots.
	artifactSlotCount := 0
	if opts.ValidateChanges && repo.RepoID != 0 {
		prefix := fmt.Sprintf("%s/%d/%s", oc.OrgID, repo.RepoID, requestID)
		s, err := b.addArtifactRunEnv(ctx, prefix, env)
		switch {
		case err == nil:
			artifactSlotCount = len(s)
		case errors.Is(err, errArtifactSlotsDisabled):
			// No S3 upload path configured; BuildValidationPrompt will
			// tell the agent to mark artifact proof incomplete rather
			// than write broken local-file links.
		default:
			b.log.Warn("artifact slot minting failed",
				"request_id", requestID, "error", err)
		}
	}
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
	// When we have a saved spec, ship its setup/start/health scripts
	// to agent.sh as base64 env vars. agent.sh decodes them before
	// invoking the agent and runs setup → start (bg) → poll health, so
	// the validation prompt's "the app is running" assertion holds.
	// Without this step the cached spec is loaded into the prompt
	// but the agent finds a dead port and falls back to figuring out
	// how to start the app from scratch — wasting the bootstrap.
	if spec != nil && spec.SetupScript != "" && spec.StartScript != "" && spec.HealthCheck != "" {
		env["SF_SPEC_SETUP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.SetupScript))
		env["SF_SPEC_START_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StartScript))
		env["SF_SPEC_HEALTH_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.HealthCheck))
	}
	b.addRuntimeEnv(env, oc, model, requestID)
	if oc.SXKey != "" {
		env["SX_KEY"] = oc.SXKey
	}
	sessionID := "agent-" + requestID
	prURL, err := b.runScriptForRequest(ctx, sb, sessionID, "agent", agentScript, env, emit)
	if err == nil && prURL != "" {
		b.markRunFinalizing(ctx)
		prURL, err = b.validateReportedPR(ctx, repo, branch, repo.BaseBranch, prURL)
	}
	if err == nil && prURL != "" && spec != nil {
		// Post-success reflection: read /tmp/hetchy-spec/improved/ to
		// see if the agent flagged any setup/start/health changes that
		// would help future tasks. Best-effort — failures here never
		// affect the PR. Runs in a fresh session because runScript
		// deleted the agent's session in its defer.
		b.applySpecImprovements(ctx, sb, sessionID, spec, repo, emit)

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
		if mErr := b.bootstrap.MarkApplied(ctx, repo.InstallID, repo.RepoID, spec.Path,
			bootstrap.StatusValidated, spec.SuccessCount+1, spec.FailureCount); mErr != nil {
			b.log.Warn("mark spec applied", "error", mErr, "repo", repo.Slug)
		}
	}
	return prURL, err
}

// ensureBootstrapSpec returns the saved spec for repo, running the
// bootstrap loop on first encounter. Bootstrap clones into the same
// workdir agent.sh will use; agent.sh detects the existing checkout and
// skips its own clone, so the work happens once.
//
// Caller is expected to gate on whether bootstrap is appropriate (a
// GitHub App-resolved repo with a stable install + repo id); this method
// assumes those preconditions hold.
func (b *Bot) ensureBootstrapSpec(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, requestID string, emit blocks.Emitter) (*bootstrap.Spec, error) {
	spec, err := b.bootstrap.GetSpec(ctx, repo.InstallID, repo.RepoID, "")
	switch {
	case err == nil:
		// Spec exists; treat it as fresh and return it. The drift-
		// detection logic itself is fully implemented in
		// bootstrap.CheckSpec / IsStale (see drift.go) — the gap is
		// the wiring call from this branch. We deliberately don't
		// wire it yet because the only detect path we have today
		// re-clones the repo, which is multi-minute and runs on
		// every task. Wire here when a cheaper "has anything
		// material changed since last bootstrap?" signal lands
		// (e.g. a repo-tree hash from the GitHub App webhook).
		return spec, nil
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

func (b *Bot) createBootstrapSession(ctx context.Context, sb *daytona.Sandbox, sessionID string) error {
	if b.createBootstrapSessionFn != nil {
		return b.createBootstrapSessionFn(ctx, sb, sessionID)
	}
	if sb == nil || sb.Process == nil {
		return errors.New("sandbox process not configured")
	}
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		if b.log != nil {
			b.log.Warn("daytona create session failed",
				"sandbox", sb.ID, "session", sessionID, "purpose", "bootstrap", "error", err)
		}
		return err
	}
	return nil
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
	if res.Manifest != nil {
		kind = res.Manifest.Kind
		requiredSecrets = res.Manifest.RequiredSecrets
		deferred = res.Manifest.DeferredCapabilities
	}
	failingSpec := &bootstrap.Spec{
		InstallationID:       repo.InstallID,
		RepoID:               repo.RepoID,
		SpecVersion:          1,
		Kind:                 kind,
		SetupScript:          res.PartialScripts.Setup,
		StartScript:          res.PartialScripts.Start,
		HealthCheck:          res.PartialScripts.Health,
		RequiredSecrets:      requiredSecrets,
		DeferredCapabilities: deferred,
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
func (b *Bot) runFollowUp(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, userRequest, requestID string, opts chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
	model = normalizeClaudeModel(model)
	var spec *bootstrap.Spec
	if opts.ValidateChanges && b.bootstrap != nil && repo.InstallID != 0 && repo.RepoID != 0 {
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

	artifactSlotCount := 0
	if opts.ValidateChanges && repo.RepoID != 0 {
		// Follow-ups can still need fresh proof links. Issue a new
		// run-scoped batch under a follow-up prefix so keys don't
		// collide with the initial request.
		prefix := fmt.Sprintf("%s/%d/%s/followup-%s", oc.OrgID, repo.RepoID, rec.ThreadID, requestID)
		s, err := b.addArtifactRunEnv(ctx, prefix, env)
		switch {
		case err == nil:
			artifactSlotCount = len(s)
		case errors.Is(err, errArtifactSlotsDisabled):
			// No S3 upload path configured; validation prompt will
			// require an explicit incomplete-artifact note.
		default:
			b.log.Warn("artifact slot minting failed (followup)",
				"request_id", requestID, "error", err)
		}
	}
	prompt := buildFollowUpPrompt(repo.Slug, rec, userRequest, spec, artifactSlotCount, opts)
	env["SF_PROMPT_B64"] = base64.StdEncoding.EncodeToString([]byte(prompt))
	b.addRuntimeEnv(env, oc, model, requestID)
	if oc.SXKey != "" {
		env["SX_KEY"] = oc.SXKey
	}
	// A follow-up lands in an unarchived sandbox where any background
	// processes from the original run are gone — including the
	// `start.sh &` invocation that brought the app up. Without this
	// step the agent's validation prompt assumes "the app is running"
	// against a dead port. Ship the saved spec so followup.sh can
	// re-run setup → start → poll health, mirroring agent.sh. Errors
	// here are best-effort: a missing spec just means the follow-up
	// runs without a live app, same as before.
	if spec != nil && spec.SetupScript != "" && spec.StartScript != "" && spec.HealthCheck != "" {
		env["SF_SPEC_SETUP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.SetupScript))
		env["SF_SPEC_START_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StartScript))
		env["SF_SPEC_HEALTH_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.HealthCheck))
	}
	prURL, err := b.runScriptForRequest(ctx, sb, "followup-"+requestID, "followup", followupScript, env, emit)
	if err != nil {
		return "", err
	}
	if prURL == "" {
		return "", nil
	}
	b.markRunFinalizing(ctx)
	return b.validateReportedPR(ctx, repo, rec.Branch, "", prURL)
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
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		if b.log != nil {
			b.log.Warn("daytona create session failed",
				"sandbox", sb.ID, "session", sessionID, "label", label, "error", err)
		}
		return "", fmt.Errorf("create session: %w", err)
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
	if _, err := b.shLines(ctx, sb.ID, sb.Process, sessionID, "write-script", writeCmd, 15*time.Second, 0, true, func(string) {}); err != nil {
		return "", err
	}

	env = maps.Clone(env)
	if err := b.materializeLargeRunEnv(ctx, sb.ID, sb.Process, sessionID, label, env); err != nil {
		return "", err
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
		router.Abort()
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
			return "", fmt.Errorf("setup script for %s exited before invoking the agent runtime — check the sandbox setup block for the failing step", label)
		}
		return "", nil
	}
	return prURL, nil
}
