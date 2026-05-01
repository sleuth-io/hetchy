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
  2. Stage and commit your changes with a clear message.
  3. Push the branch to origin (gh CLI is already authenticated).
  4. Open a pull request against %s with ` + "`gh pr create`" + `, giving it a
     clear title and a markdown body describing what changed and why.
  5. The very last line of your output MUST be just the PR URL — no other
     text on that line.`

const agentFollowUpPromptTemplate = `You are continuing work in %s on branch %s.
The pull request is at %s.

Conversation so far:
%s

USER REQUEST:
%s

When you are done implementing the change:
  1. Stage and commit your changes with a clear message.
  2. Push the branch to origin — the PR will update automatically.
  3. DO NOT update the PR title — it should remain consistent with the original
     user request shown in "Conversation so far" above, not this latest change.
  4. The very last line of your output MUST be just the PR URL — no other
     text on that line.`

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, userRequest, requestID string, onUpdate func(string)) (string, error) {
	prompt := fmt.Sprintf(agentPromptTemplate,
		b.cfg.GitHubRepo, workdir, b.cfg.BaseBranch,
		userRequest, requestID, b.cfg.BaseBranch,
	)
	return b.runScript(ctx, sb, "agent-"+requestID, "agent", agentScript, map[string]string{
		"SF_REPO":        b.cfg.GitHubRepo,
		"SF_WORKDIR":     workdir,
		"SF_BASE_BRANCH": b.cfg.BaseBranch,
		"SF_PROMPT_B64":  base64.StdEncoding.EncodeToString([]byte(prompt)),
	}, onUpdate)
}

func (b *Bot) runFollowUp(ctx context.Context, conv *conversation, userRequest, requestID string, onUpdate func(string)) (string, error) {
	history := strings.Join(conv.history, "\n---\n")
	prompt := fmt.Sprintf(agentFollowUpPromptTemplate,
		workdir, conv.branch, conv.prURL,
		history, userRequest,
	)
	return b.runScript(ctx, conv.sandbox, "followup-"+requestID, "followup", followupScript, map[string]string{
		"SF_WORKDIR":    workdir,
		"SF_BRANCH":     conv.branch,
		"SF_PROMPT_B64": base64.StdEncoding.EncodeToString([]byte(prompt)),
	}, onUpdate)
}

// runScript writes scriptBody to /tmp/sf-<label>.sh inside the sandbox, runs
// it with the given env vars prefixed on the command line, and returns the
// captured stdout+stderr. It looks for a GitHub PR URL in the output and
// returns an error if none is found.
func (b *Bot) runScript(ctx context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, onUpdate func(string)) (string, error) {
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	defer func() { _ = sb.Process.DeleteSession(ctx, sessionID) }()

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
