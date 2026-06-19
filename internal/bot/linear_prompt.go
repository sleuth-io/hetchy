package bot

import (
	"context"
	"regexp"
	"strings"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/linear"
)

// linearLeadingMention strips the bot mention off the front of a
// Linear comment ("@hetchy please fix…" → "please fix…").
var linearLeadingMention = regexp.MustCompile(`^@[\w][\w.-]*[:,]?\s*`)

// linearAgentPhrase captures the words following "with/using (the) …"
// in a directive so they can be tried against the org's agent roster:
// "fix this … with the Skills.new Bot" routes to that agent.
var linearAgentPhrase = regexp.MustCompile(`(?i)\b(?:with|using)\s+(?:the\s+)?([A-Za-z0-9][A-Za-z0-9._' -]{0,60})`)

// linearDirective returns the user's instruction with the bot mention
// stripped: the prompted activity body for follow-ups, else the
// comment the agent was mentioned in.
func linearDirective(ev linear.AgentSessionEvent) string {
	return strings.TrimSpace(linearLeadingMention.ReplaceAllString(ev.Directive(), ""))
}

// linearPromptText composes the agent prompt for a session event from
// structured fields. Created sessions get the issue header +
// description + the user's directive; prompted follow-ups get just the
// new message (the conversation already has the issue context).
// Linear's promptContext is the fallback only when nothing structured
// is available — it's an XML-ish context-packing blob that reads
// terribly as the visible "user request" in the chat transcript.
func linearPromptText(ev linear.AgentSessionEvent, directive string) string {
	if ev.Action == linear.AgentSessionActionPrompted {
		if directive != "" {
			return directive
		}
		return strings.TrimSpace(ev.PromptText())
	}
	var parts []string
	if issue := ev.AgentSession.Issue; issue != nil {
		header := "Linear issue"
		if id := strings.TrimSpace(issue.Identifier); id != "" {
			header += " " + id
		}
		if title := strings.TrimSpace(issue.Title); title != "" {
			header += ": " + title
		}
		if header != "Linear issue" {
			parts = append(parts, header)
		}
		if desc := strings.TrimSpace(issue.Description); desc != "" {
			parts = append(parts, desc)
		}
		if url := strings.TrimSpace(issue.URL); url != "" {
			parts = append(parts, "Issue link: "+url)
		}
	}
	if directive != "" {
		parts = append(parts, "Request from the Linear thread:\n"+directive)
	}
	if len(parts) == 0 {
		return strings.TrimSpace(ev.PromptText())
	}
	return strings.Join(parts, "\n\n")
}

// extractLinearAgent resolves an agent referenced by name in the
// user's directive ("… with the Skills.new Bot"). Candidates are tried
// longest-first so trailing prose after the agent name doesn't defeat
// the match, and " bot"/" agent" suffixes are stripped the same way
// Slack's phrase routing does. Returns "" when nothing resolves — the
// phrase was ordinary prose, not an agent request.
func (b *Bot) extractLinearAgent(ctx context.Context, orgID, directive string) string {
	if strings.TrimSpace(directive) == "" {
		return ""
	}
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	for _, m := range linearAgentPhrase.FindAllStringSubmatch(directive, -1) {
		words := strings.Fields(m[1])
		if len(words) > 6 {
			words = words[:6]
		}
		for i := len(words); i >= 1; i-- {
			candidate := strings.TrimRight(strings.Join(words[:i], " "), ".,;:!?'")
			for _, name := range slackAgentPhraseCandidates(candidate) {
				if agent, err := store.Resolve(ctx, orgID, name); err == nil {
					return agent.Slug
				}
			}
		}
	}
	return ""
}
