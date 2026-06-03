package bot

import (
	"context"
	"errors"
	"net/http"
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
	PRState        string                `json:"pr_state,omitempty"`
	PRMerged       bool                  `json:"pr_merged,omitempty"`
	CommandStep    string                `json:"command_step,omitempty"`
	CurrentStep    string                `json:"current_step,omitempty"`
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
	PRState            string `json:"pr_state,omitempty"`
	PRMerged           bool   `json:"pr_merged,omitempty"`
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
	latestRuns := b.latestRunsForInbox(r.Context(), p.OrgID, recs)
	for _, rec := range recs {
		run, hasRun := latestRuns[rec.ThreadID]
		item := b.agentInboxRunForConversation(r.Context(), p.OrgID, rec, run, hasRun)
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
		if item.PRURL != "" && agentInboxPRIsActionable(item) {
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

func (b *Bot) agentInboxRunForConversation(ctx context.Context, orgID string, rec convstore.Record, run runstore.Run, hasRun bool) agentInboxRun {
	state := "idle"
	outcome := ""
	commandStep := ""
	currentStep := ""
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
			currentStep = agentInboxCurrentStep(run, events)
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
		PRState:        rec.PRState,
		PRMerged:       rec.PRMerged,
		CommandStep:    commandStep,
		CurrentStep:    currentStep,
		Activity:       activity,
		TurnCount:      len(rec.History),
		UpdatedAt:      agentInboxRunTimestamp(rec, run, hasRun),
		CreatedAt:      formatOptionalTime(rec.CreatedAt),
		Milestones:     milestones,
		TaskOptions:    rec.TaskOptions,
	}
}

func agentInboxRunTimestamp(rec convstore.Record, run runstore.Run, hasRun bool) string {
	if hasRun && !run.UpdatedAt.IsZero() {
		return run.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !rec.UpdatedAt.IsZero() {
		return rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return formatOptionalTime(rec.CreatedAt)
}

func (b *Bot) latestRunsForInbox(ctx context.Context, orgID string, recs []convstore.Record) map[string]runstore.Run {
	out := map[string]runstore.Run{}
	if b.runs == nil || !b.runs.Enabled() {
		return out
	}
	threadIDs := make([]string, 0, len(recs))
	seen := map[string]struct{}{}
	for _, rec := range recs {
		if rec.ThreadID == "" {
			continue
		}
		if _, ok := seen[rec.ThreadID]; ok {
			continue
		}
		seen[rec.ThreadID] = struct{}{}
		threadIDs = append(threadIDs, rec.ThreadID)
	}
	if len(threadIDs) == 0 {
		return out
	}
	runs, err := b.runs.LatestForThreads(ctx, orgID, threadIDs)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("agent inbox latest runs", "org", orgID, "threads", len(threadIDs), "error", err)
		}
		return out
	}
	return runs
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
		PRState:            run.PRState,
		PRMerged:           run.PRMerged,
		State:              run.State,
		Outcome:            run.Outcome,
		ValidationRequired: validationRequired,
		ValidationPassed:   validationPassed,
		ReviewRequired:     reviewRequired,
		ReviewPassed:       reviewPassed,
		UpdatedAt:          run.UpdatedAt,
	}
}

func agentInboxPRIsActionable(run agentInboxRun) bool {
	if strings.TrimSpace(run.PRURL) == "" || run.PRMerged {
		return false
	}
	return strings.ToLower(strings.TrimSpace(run.PRState)) != githubPRStateClosed
}
