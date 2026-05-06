package bot

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/screenshots"
)

// screenshotSlotsPerRequest caps how many upload slots the bot mints
// per task. Three is plenty — most validations need 1-2 screenshots
// (one light + one dark, or one before + one after) and the cap
// prevents a runaway prompt from issuing dozens of presigns.
const screenshotSlotsPerRequest = 3

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

const agentPromptTemplate = `You are working inside a fresh sandbox. The repo %s has been cloned
to %s and %s is checked out. Your task is the user request below.

USER REQUEST:
%s

When you are done implementing the change:
  1. Create a new branch named feature/sf-%s.
  2. Run ` + "`make format`" + ` to format the code.
  3. Stage and commit your changes with a clear message.
  4. Push the branch to origin (gh CLI is already authenticated).
  5. Open a pull request against %s with ` + "`gh pr create`" + `, giving it a
     clear title and a markdown body describing what changed and why.
  6. The very last line of your output MUST be just the PR URL — no other
     text on that line.`

const agentFollowUpPromptTemplate = `You are continuing work in %s on branch %s.
The pull request is at %s.

Conversation so far:
%s

USER REQUEST:
%s

When you are done implementing the change:
  1. Run ` + "`make format`" + ` to format the code.
  2. Stage and commit your changes with a clear message.
  3. Push the branch to origin — the PR will update automatically.
  4. DO NOT update the PR title — it should remain consistent with the original
     user request shown in "Conversation so far" above, not this latest change.
  5. The very last line of your output MUST be just the PR URL — no other
     text on that line.`

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

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, userRequest, requestID string, validate bool, emit blocks.Emitter) (string, error) {
	var spec *bootstrap.Spec
	// validate=false is the user's explicit "skip end-to-end testing"
	// opt-out from the new-chat UI. We honour it by not running
	// bootstrap (which can take minutes on a fresh repo) and not
	// merging the validation prompt — the agent then behaves the
	// way it did before this pipeline existed: make the change,
	// open the PR, done. Slack and follow-ups always pass true.
	if validate && b.bootstrap != nil && repo.InstallID != 0 && repo.RepoID != 0 {
		s, err := b.ensureBootstrapSpec(ctx, sb, repo, oc, requestID, emit)
		if err != nil {
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

	// Mint screenshot upload slots before constructing the prompt, so
	// the validation prompt can include the slot count + matching
	// instructions only when we actually have a place for the agent
	// to PUT. Errors here downgrade to "no screenshot pipeline" — the
	// run still produces a PR, just without embedded screenshots.
	var slotsManifest []screenshots.Slot
	if validate && spec != nil && b.screenshots != nil {
		prefix := fmt.Sprintf("%s/%d/%s", oc.OrgID, repo.RepoID, requestID)
		s, err := b.screenshots.MintSlots(ctx, prefix, screenshotSlotsPerRequest)
		if err != nil {
			b.log.Warn("screenshot slot minting failed",
				"request_id", requestID, "error", err)
		} else {
			slotsManifest = s
		}
	}

	originalPrompt := fmt.Sprintf(agentPromptTemplate,
		repo.Slug, workdir, repo.BaseBranch,
		userRequest, requestID, repo.BaseBranch,
	)
	finalPrompt := originalPrompt
	if spec != nil {
		finalPrompt = bootstrap.MergeIntoAgentPrompt(originalPrompt, spec, bootstrap.ValidationArgs{
			OwnerRepo:           repo.Slug,
			Branch:              "feature/sf-" + requestID,
			ScreenshotSlotCount: len(slotsManifest),
		})
	}

	env := map[string]string{
		"SF_REPO":        repo.Slug,
		"SF_WORKDIR":     workdir,
		"SF_BASE_BRANCH": repo.BaseBranch,
		"SF_PROMPT_B64":  base64.StdEncoding.EncodeToString([]byte(finalPrompt)),
		"GITHUB_TOKEN":   repo.GitHubToken,
	}
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
	if len(slotsManifest) > 0 {
		// JSON-encode the slot manifest as a single env var. The
		// agent parses it with `jq` (already in the sandbox) per the
		// instructions in the validation prompt.
		raw, err := json.Marshal(slotsManifest)
		if err != nil {
			// Marshalling a fixed-shape struct can't realistically
			// fail; log and proceed without slots rather than
			// aborting the whole task on this corner.
			b.log.Warn("screenshot slot marshal failed", "request_id", requestID, "error", err)
		} else {
			env["HETCHY_SCREENSHOT_SLOTS"] = string(raw)
		}
	}
	authKey, authVal := claudeAuthEnv(oc)
	env[authKey] = authVal
	if oc.SXKey != "" {
		env["SX_KEY"] = oc.SXKey
	}
	return b.runScript(ctx, sb, "agent-"+requestID, "agent", agentScript, env, emit)
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
		// Spec exists; treat it as fresh and return it. Drift detection
		// (bootstrap.CheckSpec / IsStale) is implemented but deliberately
		// not wired here — running it on every task would re-fingerprint
		// against the live repo, which today means another sandbox-side
		// detect pass and the multi-minute cost that goes with it. When
		// we have a cheaper "has anything material changed since last
		// bootstrap?" signal (e.g. a repo-tree hash from the App
		// webhook), this is where it would gate a re-run.
		return spec, nil
	case errors.Is(err, bootstrap.ErrNotFound):
		// Fall through and bootstrap.
	default:
		return nil, fmt.Errorf("get spec: %w", err)
	}

	emit.Notify("First-time bootstrap",
		fmt.Sprintf("`%s` is new to Hetchy — figuring out how to run it end-to-end. This adds a few minutes to the first task; subsequent tasks reuse the result.", repo.Slug))

	sessionID := "bootstrap-" + requestID
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("create bootstrap session: %w", err)
	}
	defer func() {
		_ = sb.Process.DeleteSession(ctx, sessionID)
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
	runner := &botRunner{
		b:         b,
		sb:        sb,
		sessionID: sessionID,
		emit:      emit,
		baseEnv: map[string]string{
			authKey:        authVal,
			"GITHUB_TOKEN": repo.GitHubToken,
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
		RequiredSecrets:      requiredSecrets,
		DeferredCapabilities: deferred,
		SourceFingerprint:    bootstrap.Fingerprint(hints),
		ValidationStatus:     bootstrap.StatusFailing,
		FailureCount:         1,
		BootstrapLog:         truncateLogTail(res.Log),
	}
	if err := b.bootstrap.SaveSpec(ctx, failingSpec); err != nil {
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
	if _, err := b.shLines(ctx, sb, sessionID, label+"-write", writeCmd, 30*time.Second, func(string) {}); err != nil {
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
	if _, err := b.shLines(ctx, sb, sessionID, label+"-run", runCmd, 5*time.Minute, router.Line); err != nil {
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

// runFollowUp resumes work in an existing sandbox. The installation
// token is freshly minted and passed per-run (not just at sandbox-create
// time) so a token rotation or a re-installed App takes effect on the
// very next follow-up rather than only on a freshly-created sandbox.
func (b *Bot) runFollowUp(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, rec convstore.Record, userRequest, requestID string, emit blocks.Emitter) (string, error) {
	history := strings.Join(rec.History, "\n---\n")
	prompt := fmt.Sprintf(agentFollowUpPromptTemplate,
		workdir, rec.Branch, rec.PRURL,
		history, userRequest,
	)
	env := map[string]string{
		"SF_WORKDIR":    workdir,
		"SF_BRANCH":     rec.Branch,
		"SF_PROMPT_B64": base64.StdEncoding.EncodeToString([]byte(prompt)),
		"GITHUB_TOKEN":  repo.GitHubToken,
	}
	authKey, authVal := claudeAuthEnv(oc)
	env[authKey] = authVal
	// A follow-up lands in an unarchived sandbox where any background
	// processes from the original run are gone — including the
	// `start.sh &` invocation that brought the app up. Without this
	// step the agent's validation prompt assumes "the app is running"
	// against a dead port. Ship the saved spec so followup.sh can
	// re-run setup → start → poll health, mirroring agent.sh. Errors
	// here are best-effort: a missing spec just means the follow-up
	// runs without a live app, same as before.
	if b.bootstrap != nil && repo.InstallID != 0 && repo.RepoID != 0 {
		spec, err := b.bootstrap.GetSpec(ctx, repo.InstallID, repo.RepoID, "")
		if err == nil && spec.SetupScript != "" && spec.StartScript != "" && spec.HealthCheck != "" {
			env["SF_SPEC_SETUP_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.SetupScript))
			env["SF_SPEC_START_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.StartScript))
			env["SF_SPEC_HEALTH_B64"] = base64.StdEncoding.EncodeToString([]byte(spec.HealthCheck))
		}
	}
	return b.runScript(ctx, sb, "followup-"+requestID, "followup", followupScript, env, emit)
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
	defer func() {
		_ = b.retryWithBackoff(ctx, "delete session", func() error {
			return sb.Process.DeleteSession(ctx, sessionID)
		})
	}()

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
	if _, err := b.shLines(ctx, sb, sessionID, "write-script", writeCmd, 15*time.Second, func(string) {}); err != nil {
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
	runCmd := prefix.String() + "bash " + scriptPath

	router := newAgentLineRouter(emit)
	if _, err := b.shLines(ctx, sb, sessionID, "run-script", runCmd, 20*time.Minute, router.Line); err != nil {
		router.Abort()
		return "", err
	}
	prURL := router.Finish()
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
