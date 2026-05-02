package bot

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

//go:embed scripts/agent.sh
var agentScript string

//go:embed scripts/followup.sh
var followupScript string

var prURLRe = regexp.MustCompile(`https://github\.com/[^\s]+/pull/\d+`)

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

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, oc orgcfg.Config, userRequest, requestID string, onUpdate func(string)) (string, error) {
	branch := oc.GitHubBaseBranch
	if branch == "" {
		branch = "main"
	}
	prompt := fmt.Sprintf(agentPromptTemplate,
		oc.GitHubRepo, workdir, branch,
		userRequest, requestID, branch,
	)
	return b.runScript(ctx, sb, "agent-"+requestID, "agent", agentScript, map[string]string{
		"SF_REPO":           oc.GitHubRepo,
		"SF_WORKDIR":        workdir,
		"SF_BASE_BRANCH":    branch,
		"SF_PROMPT_B64":     base64.StdEncoding.EncodeToString([]byte(prompt)),
		"ANTHROPIC_API_KEY": oc.AnthropicAPIKey,
		"GITHUB_TOKEN":      oc.GitHubToken,
	}, onUpdate)
}

// runFollowUp resumes work in an existing sandbox. Anthropic/GitHub creds
// are passed per-run (not just at sandbox-create time) so a key rotation
// in /settings/org takes effect on the very next follow-up rather than
// only on a freshly-created sandbox.
func (b *Bot) runFollowUp(ctx context.Context, sb *daytona.Sandbox, oc orgcfg.Config, rec convstore.Record, userRequest, requestID string, onUpdate func(string)) (string, error) {
	history := strings.Join(rec.History, "\n---\n")
	prompt := fmt.Sprintf(agentFollowUpPromptTemplate,
		workdir, rec.Branch, rec.PRURL,
		history, userRequest,
	)
	return b.runScript(ctx, sb, "followup-"+requestID, "followup", followupScript, map[string]string{
		"SF_WORKDIR":        workdir,
		"SF_BRANCH":         rec.Branch,
		"SF_PROMPT_B64":     base64.StdEncoding.EncodeToString([]byte(prompt)),
		"ANTHROPIC_API_KEY": oc.AnthropicAPIKey,
		"GITHUB_TOKEN":      oc.GitHubToken,
	}, onUpdate)
}

// runScript writes scriptBody to /tmp/sf-<label>.sh inside the sandbox,
// runs it with the given env vars prefixed on the command line, and
// returns the captured stdout+stderr. It looks for a GitHub PR URL in the
// output and returns an error if none is found.
func (b *Bot) runScript(ctx context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, onUpdate func(string)) (string, error) {
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	defer func() {
		_ = b.retryWithBackoff(ctx, "delete session", func() error {
			return sb.Process.DeleteSession(ctx, sessionID)
		})
	}()

	scriptPath := "/tmp/sf-" + label + ".sh"
	writeCmd := fmt.Sprintf("cat > %s << 'SFEOF'\n%sSFEOF\nchmod +x %s", scriptPath, scriptBody, scriptPath)
	if _, err := b.sh(ctx, sb, sessionID, "write-script", writeCmd, 15*time.Second, onUpdate); err != nil {
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

	out, err := b.sh(ctx, sb, sessionID, "run-script", runCmd, 20*time.Minute, onUpdate)
	if err != nil {
		return "", err
	}

	match := prURLRe.FindString(out)
	if match == "" {
		tail := out
		if len(tail) > 1500 {
			tail = tail[len(tail)-1500:]
		}
		return "", fmt.Errorf("no PR URL found in %s output. Tail:\n%s", label, tail)
	}
	return match, nil
}
