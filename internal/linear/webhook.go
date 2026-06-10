package linear

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Webhook event constants for the agent integration.
const (
	// WebhookTypeAgentSession is the payload `type` for agent session
	// lifecycle events.
	WebhookTypeAgentSession = "AgentSessionEvent"
	// AgentSessionActionCreated fires when a user mentions or
	// delegates an issue to the agent, creating a new session.
	AgentSessionActionCreated = "created"
	// AgentSessionActionPrompted fires when the user sends a follow-up
	// message inside an existing session.
	AgentSessionActionPrompted = "prompted"
)

// WebhookTimestampTolerance bounds how stale a webhook delivery may be
// before it's rejected as a possible replay. Linear recommends
// verifying webhookTimestamp is recent.
const WebhookTimestampTolerance = 5 * time.Minute

// WebhookEnvelope is the minimal common shape of every Linear webhook
// payload — enough to route by type/workspace before decoding further.
type WebhookEnvelope struct {
	Type             string `json:"type"`
	Action           string `json:"action"`
	OrganizationID   string `json:"organizationId"`
	WebhookID        string `json:"webhookId"`
	WebhookTimestamp int64  `json:"webhookTimestamp"` // unix millis
}

// TimestampFresh reports whether the delivery timestamp is within
// tolerance of now. A zero timestamp fails closed.
func (e WebhookEnvelope) TimestampFresh(now time.Time, tolerance time.Duration) bool {
	if e.WebhookTimestamp == 0 {
		return false
	}
	ts := time.UnixMilli(e.WebhookTimestamp)
	delta := now.Sub(ts)
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

// AgentSessionEvent is the decoded payload of an AgentSessionEvent
// webhook. Field coverage is intentionally partial — only what the
// integration consumes.
type AgentSessionEvent struct {
	WebhookEnvelope
	AgentSession  AgentSession   `json:"agentSession"`
	AgentActivity *AgentActivity `json:"agentActivity"`
	// PromptContext is Linear's pre-formatted description of the
	// session: issue data, comments, and guidance. Present on
	// `created` events.
	PromptContext string `json:"promptContext"`
}

// AgentSession identifies the session plus the issue and comment it
// hangs off.
type AgentSession struct {
	ID      string        `json:"id"`
	Issue   *SessionIssue `json:"issue"`
	Comment *struct {
		ID   string `json:"id"`
		Body string `json:"body"`
	} `json:"comment"`
}

// SessionIssue is the slim issue view delivered inside a session event.
type SessionIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// AgentActivity is the activity attached to a `prompted` event — the
// user's new message in the session thread.
type AgentActivity struct {
	ID      string          `json:"id"`
	Content ActivityContent `json:"content"`
}

// ParseAgentSessionEvent decodes body as an AgentSessionEvent.
func ParseAgentSessionEvent(body []byte) (AgentSessionEvent, error) {
	var ev AgentSessionEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return AgentSessionEvent{}, fmt.Errorf("linear: parse agent session event: %w", err)
	}
	return ev, nil
}

// PromptText returns the best-available task text for the event: the
// pre-formatted promptContext when present, the prompted activity's
// body for follow-ups, then the triggering comment, then the issue
// title + description.
func (ev AgentSessionEvent) PromptText() string {
	if s := strings.TrimSpace(ev.PromptContext); s != "" {
		return s
	}
	if ev.AgentActivity != nil {
		if s := strings.TrimSpace(ev.AgentActivity.Content.Body); s != "" {
			return s
		}
	}
	if ev.AgentSession.Comment != nil {
		if s := strings.TrimSpace(ev.AgentSession.Comment.Body); s != "" {
			return s
		}
	}
	if issue := ev.AgentSession.Issue; issue != nil {
		title := strings.TrimSpace(issue.Title)
		desc := strings.TrimSpace(issue.Description)
		switch {
		case title != "" && desc != "":
			return title + "\n\n" + desc
		case title != "":
			return title
		case desc != "":
			return desc
		}
	}
	return ""
}
