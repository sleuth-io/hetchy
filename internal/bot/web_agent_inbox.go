package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

const (
	agentInboxLimitDefault = 80
	agentInboxLimitMax     = 100
	agentInboxQueryMax     = 256
)

type agentInboxResponse struct {
	GeneratedAt  string                  `json:"generated_at"`
	Counts       agentInboxCounts        `json:"counts"`
	Runs         []agentInboxRun         `json:"runs"`
	PullRequests []agentInboxPullRequest `json:"pull_requests"`
}

type agentInboxCounts struct {
	Running       int `json:"running"`
	NeedsInput    int `json:"needs_input"`
	Failed        int `json:"failed"`
	ReadyPRs      int `json:"ready_prs"`
	Agents        int `json:"agents"`
	Conversations int `json:"conversations"`
}

type agentInboxRun struct {
	ID             string                `json:"id"`
	ConversationID string                `json:"conversation_id"`
	Title          string                `json:"title"`
	State          string                `json:"state"`
	Outcome        string                `json:"outcome,omitempty"`
	Status         string                `json:"status"`
	AgentSlug      string                `json:"agent_slug"`
	CreatorID      string                `json:"creator_id,omitempty"`
	Repository     string                `json:"repository,omitempty"`
	Branch         string                `json:"branch,omitempty"`
	PRURL          string                `json:"pr_url,omitempty"`
	PRNumber       string                `json:"pr_number,omitempty"`
	CommandStep    string                `json:"command_step,omitempty"`
	Activity       string                `json:"activity,omitempty"`
	TurnCount      int                   `json:"turn_count"`
	UpdatedAt      string                `json:"updated_at"`
	CreatedAt      string                `json:"created_at,omitempty"`
	Milestones     []agentInboxMilestone `json:"milestones"`
	TaskOptions    map[string]bool       `json:"task_options,omitempty"`
}

type agentInboxMilestone struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	State string `json:"state"`
}

type agentInboxPullRequest struct {
	ConversationID     string `json:"conversation_id"`
	Title              string `json:"title"`
	URL                string `json:"url"`
	Number             string `json:"number,omitempty"`
	AgentSlug          string `json:"agent_slug"`
	Repository         string `json:"repository,omitempty"`
	State              string `json:"state"`
	Outcome            string `json:"outcome,omitempty"`
	ValidationRequired bool   `json:"validation_required"`
	ValidationPassed   bool   `json:"validation_passed"`
	ReviewRequired     bool   `json:"review_required"`
	ReviewPassed       bool   `json:"review_passed"`
	UpdatedAt          string `json:"updated_at"`
}

func (b *Bot) agentInboxHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	q := r.URL.Query()
	limit := parseClampedInt(q.Get("limit"), agentInboxLimitDefault, 1, agentInboxLimitMax)
	queryStr := strings.TrimSpace(q.Get("q"))
	if runes := []rune(queryStr); len(runes) > agentInboxQueryMax {
		queryStr = string(runes[:agentInboxQueryMax])
	}
	creatorID := q.Get("creator_id")
	agentSlug := q.Get("agent_slug")

	recs, err := b.convs.Search(r.Context(), p.OrgID, convstore.SearchOptions{
		CreatorID:       creatorID,
		FilterCreatorID: q.Has("creator_id"),
		AgentSlug:       agentSlug,
		FilterAgentSlug: q.Has("agent_slug"),
		Query:           queryStr,
		Limit:           limit,
		Offset:          0,
	})
	if err != nil {
		b.log.Error("agent inbox search conversations", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	resp := agentInboxResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Runs:        make([]agentInboxRun, 0, len(recs)),
	}
	agentSlugs := map[string]struct{}{}
	for _, rec := range recs {
		item := b.agentInboxRunForConversation(r.Context(), p.OrgID, rec)
		resp.Runs = append(resp.Runs, item)
		resp.Counts.Conversations++
		if item.AgentSlug != "" {
			agentSlugs[item.AgentSlug] = struct{}{}
		}
		switch item.Status {
		case "running":
			resp.Counts.Running++
		case "needs_input":
			resp.Counts.NeedsInput++
		case "failed":
			resp.Counts.Failed++
		}
		if item.PRURL != "" {
			pr := agentInboxPRForRun(item)
			resp.PullRequests = append(resp.PullRequests, pr)
			if pr.ValidationPassed && pr.ReviewPassed {
				resp.Counts.ReadyPRs++
			}
		}
	}
	resp.Counts.Agents = len(agentSlugs)
	writeJSON(w, resp)
}

func (b *Bot) agentInboxRunForConversation(ctx context.Context, orgID string, rec convstore.Record) agentInboxRun {
	run, hasRun := b.latestRunForInbox(ctx, orgID, rec.ThreadID)
	state := "idle"
	outcome := ""
	commandStep := ""
	activity := ""
	milestones := agentInboxMilestones(rec, run, false)
	if hasRun {
		state = run.State
		outcome = run.Outcome
		commandStep = run.CommandStep
		milestones = agentInboxMilestones(rec, run, true)
		if !isTerminalRunState(run.State) {
			events, err := b.runs.EventsAfterLimit(ctx, run.ID, 0, 5000)
			if err != nil {
				b.log.Warn("agent inbox run events", "org", orgID, "thread", rec.ThreadID, "run", run.ID, "error", err)
			}
			activity = agentInboxActivity(run, events)
		}
	}
	if b.live != nil && b.live.Get(orgID, rec.ThreadID) != nil && agentInboxStatus(state, outcome) != "running" {
		state = runstore.StateRunning
	}
	if activity == "" {
		activity = agentInboxStateLabel(state, outcome)
	}
	repo := ""
	if rec.GitHubOwner != "" && rec.GitHubRepo != "" {
		repo = rec.GitHubOwner + "/" + rec.GitHubRepo
	}
	id := rec.ThreadID
	if hasRun && run.ID != "" {
		id = run.ID
	}
	return agentInboxRun{
		ID:             id,
		ConversationID: rec.ThreadID,
		Title:          conversationTitle(rec),
		State:          state,
		Outcome:        outcome,
		Status:         agentInboxStatus(state, outcome),
		AgentSlug:      rec.AgentSlug,
		CreatorID:      rec.CreatorID,
		Repository:     repo,
		Branch:         firstNonEmpty(run.Branch, rec.Branch),
		PRURL:          rec.PRURL,
		PRNumber:       pullRequestNumber(rec.PRURL),
		CommandStep:    commandStep,
		Activity:       activity,
		TurnCount:      len(rec.History),
		UpdatedAt:      rec.UpdatedAt.UTC().Format(time.RFC3339),
		CreatedAt:      formatOptionalTime(rec.CreatedAt),
		Milestones:     milestones,
		TaskOptions:    rec.TaskOptions,
	}
}

func (b *Bot) latestRunForInbox(ctx context.Context, orgID, threadID string) (runstore.Run, bool) {
	if b.runs == nil || !b.runs.Enabled() {
		return runstore.Run{}, false
	}
	run, err := b.runs.LatestForThread(ctx, orgID, threadID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("agent inbox latest run", "org", orgID, "thread", threadID, "error", err)
		}
		return runstore.Run{}, false
	}
	return run, true
}

func agentInboxPRForRun(run agentInboxRun) agentInboxPullRequest {
	validationRequired := chatTaskOptionEnabled(run.TaskOptions, chatTaskValidateKey)
	reviewRequired := chatTaskOptionEnabled(run.TaskOptions, chatTaskReviewCodeBeforePushKey) ||
		chatTaskOptionEnabled(run.TaskOptions, chatTaskActionPRChecksForDoneKey)
	verified := run.Outcome == runstore.OutcomeCompletedWithVerifiedPR
	legacySucceeded := run.Outcome == "" && run.State == runstore.StateSucceeded
	validationPassed := !validationRequired || verified || legacySucceeded
	reviewPassed := !reviewRequired || verified || legacySucceeded
	return agentInboxPullRequest{
		ConversationID:     run.ConversationID,
		Title:              run.Title,
		URL:                run.PRURL,
		Number:             run.PRNumber,
		AgentSlug:          run.AgentSlug,
		Repository:         run.Repository,
		State:              run.State,
		Outcome:            run.Outcome,
		ValidationRequired: validationRequired,
		ValidationPassed:   validationPassed,
		ReviewRequired:     reviewRequired,
		ReviewPassed:       reviewPassed,
		UpdatedAt:          run.UpdatedAt,
	}
}

func agentInboxStatus(state, outcome string) string {
	switch state {
	case runstore.StatePreparing, runstore.StateRunning, runstore.StateRecovering, runstore.StateFinalizing:
		return "running"
	case runstore.StateFailed:
		if outcome == runstore.OutcomeCompletedNoPR {
			return "needs_input"
		}
		return "failed"
	case runstore.StateCancelled:
		return "cancelled"
	case runstore.StateSucceeded:
		return "done"
	default:
		return "done"
	}
}

func agentInboxStateLabel(state, outcome string) string {
	switch agentInboxStatus(state, outcome) {
	case "running":
		return "Run is active."
	case "needs_input":
		return "Run finished without a pull request."
	case "failed":
		return "Run failed."
	case "cancelled":
		return "Run was stopped."
	default:
		return "Run is complete."
	}
}

func agentInboxActivity(run runstore.Run, events []runstore.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		var payload sseEvent
		if err := json.Unmarshal(events[i].Data, &payload); err != nil {
			continue
		}
		switch events[i].Event {
		case "heartbeat":
			if text := compactActivityText(payload.Title, payload.Delta); text != "" {
				return text
			}
		case "block_append":
			if line := lastNonEmptyLine(payload.Delta); line != "" {
				return line
			}
		case "block_start":
			if payload.Title != "" {
				return payload.Title
			}
		case "block_done":
			if payload.Summary != "" {
				return payload.Summary
			}
		}
	}
	return friendlyRunCommandStep(run.CommandStep)
}

func compactActivityText(parts ...string) string {
	var clean []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, ": ")
}

func lastNonEmptyLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func friendlyRunCommandStep(step string) string {
	switch step {
	case "setup-clone-write", "setup-clone-run", "detect-tar":
		return "Preparing the repository."
	case "bootstrap-write", "bootstrap-write-bootstrap", "bootstrap-run-bootstrap":
		return "Bootstrapping the repository."
	case "write-script", "write-env":
		return "Preparing the sandbox command."
	case "run-script":
		return "Running the agent."
	default:
		step = strings.TrimSpace(step)
		if step == "" {
			return ""
		}
		return strings.ReplaceAll(step, "-", " ") + "."
	}
}

func agentInboxMilestones(rec convstore.Record, run runstore.Run, hasRun bool) []agentInboxMilestone {
	labels := []agentInboxMilestone{
		{Key: "start", Label: "Start", State: "pending"},
		{Key: "bootstrap", Label: "Bootstrap", State: "pending"},
		{Key: "run", Label: "Run", State: "pending"},
		{Key: "pr", Label: "PR", State: "pending"},
		{Key: "checks", Label: "Checks", State: "pending"},
	}
	if !hasRun {
		if len(rec.History) > 0 {
			labels[0].State = "done"
		}
		if rec.PRURL != "" {
			labels[1].State = "done"
			labels[2].State = "done"
			labels[3].State = "done"
			labels[4].State = "done"
		}
		return labels
	}
	for i := range labels {
		labels[i].State = "done"
	}
	if run.SandboxID == "" && run.State == runstore.StatePreparing {
		labels[1].State = "current"
		for i := 2; i < len(labels); i++ {
			labels[i].State = "pending"
		}
		return labels
	}
	if !isTerminalRunState(run.State) {
		switch {
		case strings.HasPrefix(run.CommandStep, "bootstrap"):
			labels[1].State = "current"
			for i := 2; i < len(labels); i++ {
				labels[i].State = "pending"
			}
		case rec.PRURL == "":
			labels[2].State = "current"
			labels[3].State = "pending"
			labels[4].State = "pending"
		default:
			labels[4].State = "current"
		}
		return labels
	}
	if rec.PRURL == "" {
		labels[3].State = "pending"
		labels[4].State = "pending"
	}
	if run.State != runstore.StateSucceeded {
		labels[4].State = "pending"
	}
	return labels
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

var pullRequestURLNumberRe = regexp.MustCompile(`/pull/(\d+)(?:[/?#]|$)`)

func pullRequestNumber(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host != "github.com" {
		return ""
	}
	m := pullRequestURLNumberRe.FindStringSubmatch(u.Path)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
