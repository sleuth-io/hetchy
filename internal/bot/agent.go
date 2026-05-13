package bot

import (
	"context"
	_ "embed"
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
	"github.com/hetchyhq/hetchy/internal/artifacts"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

//go:embed scripts/agent.sh
var agentScriptBody string

//go:embed scripts/followup.sh
var followupScriptBody string

//go:embed scripts/setup-clone.sh
var setupCloneScript string

//go:embed scripts/claude-watchdog.sh
var claudeWatchdogScript string

// agentScript and followupScript are the on-the-wire script bodies the
// bot writes to the sandbox. They are claude-watchdog.sh prepended to
// the user-visible scripts/agent.sh and scripts/followup.sh — the
// prepend wires the run_claude_with_watchdog function into the same
// shell scope. We do the join here (vs. having each script `source` a
// separately-deployed file) so runScript only has to push one file per
// invocation and there's no chance of a half-deployed pair.
var agentScript = claudeWatchdogScript + "\n" + agentScriptBody

var followupScript = claudeWatchdogScript + "\n" + followupScriptBody

// The no-hard-wrap rule on bullet 5 also covers the Validation section
// appended by bootstrap.MergeIntoAgentPrompt — see the matching note at
// internal/bootstrap/validate.go:121.
const agentPromptTemplate = `You are working inside a fresh sandbox. The repo %s has been cloned
to %s and %s is checked out. Your task is the user request below.

USER REQUEST:
%s%s

When you are done implementing the change:
  1. Create a new branch named feature/sf-%s.
  2. Run ` + "`make format`" + ` to format the code.
  3. Stage and commit your changes with a clear message.
  4. Push the branch to origin (gh CLI is already authenticated).
  5. Open a pull request against %s with ` + "`gh pr create`" + `, giving it a clear title and a markdown body describing what changed and why. Write each paragraph or bullet of the PR body as one long line — do NOT insert hard line breaks; let GitHub reflow the text for the reader's viewport.
  6. The very last line of your output MUST be just the PR URL — no other text on that line.`

const agentFollowUpPromptTemplate = `You are continuing work in %s on branch %s.
The pull request is at %s.

Conversation so far:
%s

USER REQUEST:
%s%s

When you are done implementing the change:
  1. Run ` + "`make format`" + ` to format the code.
  2. Stage and commit your changes with a clear message.
  3. Push the branch to origin — the PR will update automatically.
  4. DO NOT update the PR title — it should remain consistent with the original
     user request shown in "Conversation so far" above, not this latest change.
  5. If you edit the PR body (e.g. to add a Validation section), write each paragraph or bullet as one long line — do NOT insert hard line breaks; let GitHub reflow the text for the reader's viewport.
  6. The very last line of your output MUST be just the PR URL — no other text on that line.`

func conditionalTasksPrompt(opts chatTaskOptions) string {
	var tasks []string
	if opts.ReviewCodeBeforePush {
		tasks = append(tasks, "- Review code before push: before pushing or opening the PR, launch a Claude Code sub-agent/task to review the branch diff against its base branch. Use the sub-agent for an independent code review focused on bugs, regressions, missing tests, security issues, and maintainability problems. If the reviewer uses severity levels, fix every issue above LOW severity; otherwise fix every concrete actionable issue it reports. Commit and push only after those fixes are in place.")
	}
	if opts.ActionPRChecksForDone {
		tasks = append(tasks, "- Action PR checks for done: after opening or updating the PR, you are not done. Use `gh` to inspect the PR's status checks and automated review activity, then wait for running checks to complete. If any check fails, fix it, commit, push, and wait again. If an automated AI review is running, wait for it to finish; if the reviewer uses severity levels, fix every issue above LOW severity, and if it does not use severity levels, fix every concrete actionable issue it reports. Commit, push, and check again. Only finish when all checks pass and automated AI reviews contain no issues above LOW severity or no remaining actionable findings.")
	}
	if len(tasks) == 0 {
		return ""
	}
	return "\n\nADDITIONAL CHAT TASKS ENABLED FOR THIS RUN:\n" + strings.Join(tasks, "\n")
}

// repoCtx carries the resolved per-request repository details into the
// sandbox: the slug "owner/name", the default branch the agent should
// branch off, and a freshly-minted GitHub App installation token. The
// token is scoped to a single repo (RepoID is set when minting upstream)
// so a compromised sandbox can only push to the one repo it's working
// on, not the whole installation.
type repoCtx struct {
	Slug         string
	BaseBranch   string
	GitHubToken  string
	InstallID    int64
	RepoID       int64
	TokenExpires time.Time
}

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, userRequest, requestID string, opts chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
	model = normalizeClaudeModel(model)
	var spec *bootstrap.Spec
	// ValidateChanges=false is the user's explicit "skip end-to-end
	// testing" opt-out from the new-chat UI. We honour it by not
	// running bootstrap (which can take minutes on a fresh repo) and
	// not merging the validation prompt.
	if opts.ValidateChanges && b.bootstrap != nil && repo.InstallID != 0 && repo.RepoID != 0 {
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
			emit.Notify("Bootstrap skipped",
				"Couldn't auto-bootstrap this repo for end-to-end validation — running the agent without a validation spec. Check server logs for details.")
		} else {
			spec = s
		}
	}

	env := map[string]string{
		"SF_REPO":             repo.Slug,
		"SF_WORKDIR":          workdir,
		"SF_BASE_BRANCH":      repo.BaseBranch,
		"GITHUB_TOKEN":        repo.GitHubToken,
		"HETCHY_CLAUDE_MODEL": string(model),
	}
	addAgentEnv(env, b.cfg, agent)

	// Mint the default proof-artifact batch before constructing the
	// prompt, so validation instructions can mention upload slots only
	// when the S3 path is actually available. The run-scoped token lets
	// the sandbox request more slots up to artifacts.MaxSlots.
	var slotsManifest []artifacts.Slot
	if opts.ValidateChanges && spec != nil {
		prefix := fmt.Sprintf("%s/%d/%s", oc.OrgID, repo.RepoID, requestID)
		s, err := b.addArtifactRunEnv(ctx, prefix, env)
		switch {
		case err == nil:
			slotsManifest = s
		case errors.Is(err, errArtifactSlotsDisabled):
			// No S3 upload path configured; BuildValidationPrompt will
			// tell the agent to mark artifact proof incomplete rather
			// than write broken local-file links.
		default:
			b.log.Warn("artifact slot minting failed",
				"request_id", requestID, "error", err)
		}
	}

	originalPrompt := fmt.Sprintf(agentPromptTemplate,
		repo.Slug, workdir, repo.BaseBranch,
		userRequest, conditionalTasksPrompt(opts), requestID, repo.BaseBranch,
	)
	finalPrompt := originalPrompt
	if spec != nil {
		finalPrompt = bootstrap.MergeIntoAgentPrompt(originalPrompt, spec, bootstrap.ValidationArgs{
			OwnerRepo:         repo.Slug,
			Branch:            "feature/sf-" + requestID,
			ArtifactSlotCount: len(slotsManifest),
		})
	}

	env["SF_PROMPT_B64"] = base64.StdEncoding.EncodeToString([]byte(finalPrompt))
	// When we have a saved spec, ship its setup/start/health scripts
	// to agent.sh as base64 env vars. agent.sh decodes them before
	// invoking claude and runs setup → start (bg) → poll health, so
	// the validation prompt's "the app is running" assertion holds.
	// Without this step the cached spec is loaded into the prompt
	// but the agent finds a dead port and falls back to figuring out
	// how to start the app from scratch — wasting the bootstrap.
	if spec != nil && spec.SetupScript != "" && spec.StartScript != "" && spec.HealthCheck != "" {
		env["SF_SPEC_SETUP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.SetupScript))
		env["SF_SPEC_START_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StartScript))
		env["SF_SPEC_HEALTH_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.HealthCheck))
	}
	authKey, authVal := claudeAuthEnv(oc)
	b.log.Info("claude auth", "method", authKey, "token", maskToken(authVal), "request_id", requestID)
	env[authKey] = authVal
	if oc.SXKey != "" {
		env["SX_KEY"] = oc.SXKey
	}
	sessionID := "agent-" + requestID
	prURL, err := b.runScript(ctx, sb, sessionID, "agent", agentScript, env, emit)
	if err == nil {
		b.markRunFinalizing(ctx)
		prURL, err = b.validateReportedPR(ctx, repo, "feature/sf-"+requestID, repo.BaseBranch, prURL)
	}
	if err == nil && spec != nil {
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
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("create bootstrap session: %w", err)
	}
	defer func() {
		b.deleteSandboxSession(sb, sessionID)
	}()

	cloneEnv := map[string]string{
		"SF_REPO":        repo.Slug,
		"SF_WORKDIR":     workdir,
		"SF_BASE_BRANCH": repo.BaseBranch,
		"GITHUB_TOKEN":   repo.GitHubToken,
	}
	if err := b.runInlineScript(ctx, sb, sessionID, "setup-clone", setupCloneScript, cloneEnv, emit); err != nil {
		return nil, fmt.Errorf("setup-clone: %w", err)
	}

	hints, tempRoot, err := b.detectViaSandbox(ctx, sb, sessionID, workdir)
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
	runner := &botRunner{
		b:         b,
		sb:        sb,
		sessionID: sessionID,
		emit:      emit,
		baseEnv: map[string]string{
			authKey:                authVal,
			"GITHUB_TOKEN":         repo.GitHubToken,
			"HETCHY_CLAUDE_MODEL":  string(ClaudeModelOpus),
			"HETCHY_CLAUDE_EFFORT": "high",
		},
	}
	res, err := bootstrap.Run(ctx, runner, bootstrap.LoopInput{
		OwnerRepo:       repo.Slug,
		Hints:           hints,
		SuppliedSecrets: suppliedSecrets,
		RepoDir:         workdir,
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

	res.Spec.InstallationID = repo.InstallID
	res.Spec.RepoID = repo.RepoID
	res.Spec.BootstrapLog = truncateLogTail(res.Log)
	if err := b.bootstrap.SaveSpec(ctx, res.Spec); err != nil {
		return nil, fmt.Errorf("save spec: %w", err)
	}

	for _, sec := range res.Spec.RequiredSecrets {
		if err := b.bootstrap.DeclareRequiredSecret(ctx, repo.InstallID, repo.RepoID, "", sec.Name); err != nil {
			b.log.Warn("declare required secret",
				"repo", repo.Slug, "name", sec.Name, "error", err)
		}
	}

	emit.Notify("Bootstrap complete",
		fmt.Sprintf("Saved a `%s` setup for `%s` (status: %s). The agent will now run with end-to-end validation.",
			res.Spec.Kind, repo.Slug, res.Spec.ValidationStatus))
	return res.Spec, nil
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
// Shared by ensureBootstrapSpec and persistFailingBootstrap.
func truncateLogTail(s string) string {
	const max = 32 * 1024
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

// claudeAuthEnv picks the env-var name + value to inject into the
// sandbox so the `claude` binary authenticates correctly. It prefers
// the subscription OAuth token over an API key when both are set:
// Claude Code's own precedence puts ANTHROPIC_API_KEY ahead of
// CLAUDE_CODE_OAUTH_TOKEN, so injecting both would silently fall back
// to the API key, which is not what an org that pasted a subscription
// token expects. Callers must have already verified that at least one
// of the two is non-empty (HandleRequest does this).
func claudeAuthEnv(oc orgcfg.Config) (name, value string) {
	if oc.ClaudeCodeOAuthToken != "" {
		return "CLAUDE_CODE_OAUTH_TOKEN", oc.ClaudeCodeOAuthToken
	}
	return "ANTHROPIC_API_KEY", oc.AnthropicAPIKey
}

// maskToken returns exactly 8 asterisks so logs confirm a token is set without
// revealing any characters or length information. Returns "(empty)" when s is
// empty so callers can distinguish a missing token from a present one.
func maskToken(s string) string {
	if len(s) == 0 {
		return "(empty)"
	}
	return "********"
}

func addAgentEnv(env map[string]string, cfg Config, agent agents.Profile) {
	env["HETCHY_AGENT_SLUG"] = agent.Slug
	env["HETCHY_AGENT_NAME"] = agent.DisplayName
	env["HETCHY_AGENT_SX_BOT"] = agent.SXBot
	env["HETCHY_AGENT_PERSONA_ASSET"] = agent.PersonaAsset
	env["HETCHY_AGENT_PROMPT_B64"] = base64.StdEncoding.EncodeToString([]byte(agent.PersonaPrompt))
	if cfg.SXPublicVaultURL != "" {
		env["HETCHY_SX_PUBLIC_VAULT_URL"] = cfg.SXPublicVaultURL
	}
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

	env := map[string]string{
		"SF_WORKDIR":          workdir,
		"SF_BRANCH":           rec.Branch,
		"GITHUB_TOKEN":        repo.GitHubToken,
		"HETCHY_CLAUDE_MODEL": string(model),
	}
	addAgentEnv(env, b.cfg, agent)

	var slotsManifest []artifacts.Slot
	if opts.ValidateChanges && spec != nil {
		// Follow-ups can still need fresh proof links. Issue a new
		// run-scoped batch under a follow-up prefix so keys don't
		// collide with the initial request.
		prefix := fmt.Sprintf("%s/%d/%s/followup-%s", oc.OrgID, repo.RepoID, rec.ThreadID, requestID)
		s, err := b.addArtifactRunEnv(ctx, prefix, env)
		switch {
		case err == nil:
			slotsManifest = s
		case errors.Is(err, errArtifactSlotsDisabled):
			// No S3 upload path configured; validation prompt will
			// require an explicit incomplete-artifact note.
		default:
			b.log.Warn("artifact slot minting failed (followup)",
				"request_id", requestID, "error", err)
		}
	}
	prompt := buildFollowUpPrompt(repo.Slug, rec, userRequest, spec, len(slotsManifest), opts)
	env["SF_PROMPT_B64"] = base64.StdEncoding.EncodeToString([]byte(prompt))
	authKey, authVal := claudeAuthEnv(oc)
	b.log.Info("claude auth", "method", authKey, "token", maskToken(authVal), "request_id", requestID)
	env[authKey] = authVal
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
	prURL, err := b.runScript(ctx, sb, "followup-"+requestID, "followup", followupScript, env, emit)
	if err != nil {
		return "", err
	}
	b.markRunFinalizing(ctx)
	return b.validateReportedPR(ctx, repo, rec.Branch, "", prURL)
}

func buildFollowUpPrompt(ownerRepo string, rec convstore.Record, userRequest string, spec *bootstrap.Spec, artifactSlotCount int, opts chatTaskOptions) string {
	history := strings.Join(rec.History, "\n---\n")
	prompt := fmt.Sprintf(agentFollowUpPromptTemplate,
		workdir, rec.Branch, rec.PRURL,
		history, userRequest, conditionalTasksPrompt(opts),
	)
	if spec == nil {
		return prompt
	}
	return bootstrap.MergeIntoAgentPrompt(prompt, spec, bootstrap.ValidationArgs{
		OwnerRepo:         ownerRepo,
		Branch:            rec.Branch,
		ArtifactSlotCount: artifactSlotCount,
	})
}

// runScript writes scriptBody to /tmp/sf-<label>.sh inside the sandbox
// and runs it with the given env vars prefixed on the command line. It
// streams Block-shaped updates via emit (sandbox bootstrap goes into a
// "setup" block; the Claude stream-json output is parsed line-by-line
// into typed blocks). Returns the PR URL extracted from the final
// assistant message in the Claude stream.
func (b *Bot) runScript(ctx context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, emit blocks.Emitter) (string, error) {
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
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
	if _, err := b.shLines(ctx, sb.ID, sb.Process, sessionID, "run-script", runCmd, 45*time.Minute, 15*time.Minute, true, framed.Line); err != nil {
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
		// Distinguish "setup never reached claude" from "claude ran
		// but didn't post a URL". Both surface here but they need
		// different remediation, so on-call shouldn't have to tail
		// logs to tell them apart.
		if !router.ReachedAgent() {
			return "", fmt.Errorf("setup script for %s exited before invoking claude — check the sandbox setup block for the failing step", label)
		}
		return "", fmt.Errorf("claude finished the %s run without posting a PR URL — check the agent transcript blocks", label)
	}
	return prURL, nil
}

const sandboxEnvFileThreshold = 8 * 1024

var sandboxEnvFileKeys = map[string]string{
	"HETCHY_AGENT_PROMPT_B64": "HETCHY_AGENT_PROMPT_B64_FILE",
	"SF_PROMPT_B64":           "SF_PROMPT_B64_FILE",
	"SF_SPEC_HEALTH_B64":      "SF_SPEC_HEALTH_B64_FILE",
	"SF_SPEC_SETUP_B64":       "SF_SPEC_SETUP_B64_FILE",
	"SF_SPEC_START_B64":       "SF_SPEC_START_B64_FILE",
}

func (b *Bot) materializeLargeRunEnv(ctx context.Context, sandboxID string, proc sandboxProcess, sessionID, label string, env map[string]string) error {
	var keys []string
	for key := range sandboxEnvFileKeys {
		if val, ok := env[key]; ok && len(val) > sandboxEnvFileThreshold {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return nil
	}

	dir := "/tmp/hetchy-env/" + label
	var cmd strings.Builder
	cmd.WriteString("rm -rf -- ")
	cmd.WriteString(shellQuote(dir))
	cmd.WriteByte('\n')
	cmd.WriteString("mkdir -p -- ")
	cmd.WriteString(shellQuote(dir))

	fileVars := make(map[string]string, len(keys))
	for _, key := range keys {
		val := env[key]
		path := dir + "/" + strings.ToLower(key) + ".b64"
		fileVars[key] = path
		cmd.WriteByte('\n')
		cmd.WriteString(heredocWriteCmd(path, val, false))
		b.log.Info("sandbox env file write",
			"sandbox", sandboxID,
			"session", sessionID,
			"label", label,
			"key", key,
			"bytes", len(val),
		)
	}
	if _, err := b.shLines(ctx, sandboxID, proc, sessionID, "write-env", cmd.String(), 60*time.Second, 0, true, func(string) {}); err != nil {
		return fmt.Errorf("write env files: %w", err)
	}
	for _, key := range keys {
		fileKey := sandboxEnvFileKeys[key]
		delete(env, key)
		env[fileKey] = fileVars[key]
	}
	return nil
}
