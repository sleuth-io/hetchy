package bot

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

//go:embed scripts/agent.sh
var agentScript string

//go:embed scripts/followup.sh
var followupScript string

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

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, anthropicAPIKey, sxKey, userRequest, requestID string, emit blocks.Emitter) (string, error) {
	prompt := fmt.Sprintf(agentPromptTemplate,
		repo.Slug, workdir, repo.BaseBranch,
		userRequest, requestID, repo.BaseBranch,
	)
	env := map[string]string{
		"SF_REPO":           repo.Slug,
		"SF_WORKDIR":        workdir,
		"SF_BASE_BRANCH":    repo.BaseBranch,
		"SF_PROMPT_B64":     base64.StdEncoding.EncodeToString([]byte(prompt)),
		"ANTHROPIC_API_KEY": anthropicAPIKey,
		"GITHUB_TOKEN":      repo.GitHubToken,
	}
	if sxKey != "" {
		env["SX_KEY"] = sxKey
	}
	return b.runScript(ctx, sb, "agent-"+requestID, "agent", agentScript, env, emit)
}

// runFollowUp resumes work in an existing sandbox. The installation
// token is freshly minted and passed per-run (not just at sandbox-create
// time) so a token rotation or a re-installed App takes effect on the
// very next follow-up rather than only on a freshly-created sandbox.
func (b *Bot) runFollowUp(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, anthropicAPIKey string, rec convstore.Record, userRequest, requestID string, emit blocks.Emitter) (string, error) {
	history := strings.Join(rec.History, "\n---\n")
	prompt := fmt.Sprintf(agentFollowUpPromptTemplate,
		workdir, rec.Branch, rec.PRURL,
		history, userRequest,
	)
	return b.runScript(ctx, sb, "followup-"+requestID, "followup", followupScript, map[string]string{
		"SF_WORKDIR":        workdir,
		"SF_BRANCH":         rec.Branch,
		"SF_PROMPT_B64":     base64.StdEncoding.EncodeToString([]byte(prompt)),
		"ANTHROPIC_API_KEY": anthropicAPIKey,
		"GITHUB_TOKEN":      repo.GitHubToken,
	}, emit)
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
	scriptPath := "/tmp/sf-" + label + ".sh"
	writeCmd := fmt.Sprintf("cat > %s << 'SFEOF'\n%sSFEOF\nchmod +x %s", scriptPath, scriptBody, scriptPath)
	if _, err := b.shLines(ctx, sb, sessionID, "write-script", writeCmd, 15*time.Second, func(string) {}); err != nil {
		return "", err
	}

	var prefix strings.Builder
	for k, v := range env {
		prefix.WriteString(k)
		prefix.WriteByte('=')
		prefix.WriteString(shellQuote(v))
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
