package bot

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

const (
	appDataLimitDefault = 80
	appDataLimitMax     = 100
	appDataQueryMax     = 256
)

type appDataResponse struct {
	GeneratedAt  string               `json:"generated_at"`
	Counts       appDataCounts        `json:"counts"`
	Runs         []appDataRun         `json:"runs"`
	PullRequests []appDataPullRequest `json:"pull_requests"`
}

type appDataCounts struct {
	Running       int `json:"running"`
	NeedsInput    int `json:"needs_input"`
	Failed        int `json:"failed"`
	ReadyPRs      int `json:"ready_prs"`
	Agents        int `json:"agents"`
	Conversations int `json:"conversations"`
}

type appDataRun struct {
	ID             string             `json:"id"`
	ConversationID string             `json:"conversation_id"`
	Title          string             `json:"title"`
	State          string             `json:"state"`
	Outcome        string             `json:"outcome,omitempty"`
	Status         string             `json:"status"`
	ResultLabel    string             `json:"result_label,omitempty"`
	RunKind        string             `json:"run_kind,omitempty"`
	TriggerSource  string             `json:"trigger_source,omitempty"`
	JobID          string             `json:"job_id,omitempty"`
	JobExecutionID string             `json:"job_execution_id,omitempty"`
	AgentSlug      string             `json:"agent_slug"`
	CreatorID      string             `json:"creator_id,omitempty"`
	Repository     string             `json:"repository,omitempty"`
	Branch         string             `json:"branch,omitempty"`
	PRURL          string             `json:"pr_url,omitempty"`
	PRNumber       string             `json:"pr_number,omitempty"`
	PRState        string             `json:"pr_state,omitempty"`
	PRMerged       bool               `json:"pr_merged,omitempty"`
	CommandStep    string             `json:"command_step,omitempty"`
	CurrentStep    string             `json:"current_step,omitempty"`
	Activity       string             `json:"activity,omitempty"`
	TurnCount      int                `json:"turn_count"`
	UpdatedAt      string             `json:"updated_at"`
	CreatedAt      string             `json:"created_at,omitempty"`
	Milestones     []appDataMilestone `json:"milestones"`
	TaskOptions    map[string]bool    `json:"task_options,omitempty"`
}

type appDataMilestone struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	State string `json:"state"`
}

type appDataPullRequest struct {
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

func (b *Bot) appDataHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	q := r.URL.Query()
	limit := parseClampedInt(q.Get("limit"), appDataLimitDefault, 1, appDataLimitMax)
	queryStr := strings.TrimSpace(q.Get("q"))
	if runes := []rune(queryStr); len(runes) > appDataQueryMax {
		queryStr = string(runes[:appDataQueryMax])
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
		b.log.Error("app data search conversations", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	resp := appDataResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Runs:        make([]appDataRun, 0, len(recs)),
	}
	agentSlugs := map[string]struct{}{}
	latestRuns := b.latestRunsForAppData(r.Context(), p.OrgID, recs)
	for _, rec := range recs {
		run, hasRun := latestRuns[rec.ThreadID]
		item := b.appDataRunForConversation(r.Context(), p.OrgID, rec, run, hasRun)
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
		if item.PRURL != "" && appDataPRIsActionable(item) {
			pr := appDataPRForRun(item)
			resp.PullRequests = append(resp.PullRequests, pr)
			if pr.ValidationPassed && pr.ReviewPassed {
				resp.Counts.ReadyPRs++
			}
		}
	}
	resp.Counts.Agents = len(agentSlugs)
	writeJSON(w, resp)
}

func (b *Bot) appDataRunForConversation(ctx context.Context, orgID string, rec convstore.Record, run runstore.Run, hasRun bool) appDataRun {
	state := "idle"
	outcome := ""
	runKind := ""
	commandStep := ""
	currentStep := ""
	activity := ""
	milestones := appDataMilestones(rec, run, false)
	if hasRun {
		state = run.State
		outcome = run.Outcome
		runKind = run.RunKind
		commandStep = run.CommandStep
		milestones = appDataMilestones(rec, run, true)
		if !isTerminalRunState(run.State) {
			events, err := b.runs.EventsAfterLimit(ctx, run.ID, 0, 5000)
			if err != nil {
				b.log.Warn("app data run events", "org", orgID, "thread", rec.ThreadID, "run", run.ID, "error", err)
			}
			activity = appDataActivity(run, events)
			currentStep = appDataCurrentStep(run, events)
		}
	}
	if b.live != nil && b.live.Get(orgID, rec.ThreadID) != nil && appDataStatus(state, outcome) != "running" {
		state = runstore.StateRunning
	}
	if activity == "" {
		activity = appDataStateLabel(state, outcome)
	}
	repo := ""
	if rec.GitHubOwner != "" && rec.GitHubRepo != "" {
		repo = rec.GitHubOwner + "/" + rec.GitHubRepo
	}
	id := rec.ThreadID
	if hasRun && run.ID != "" {
		id = run.ID
	}
	return appDataRun{
		ID:             id,
		ConversationID: rec.ThreadID,
		Title:          conversationTitle(rec),
		State:          state,
		Outcome:        outcome,
		Status:         appDataStatus(state, outcome),
		ResultLabel:    appDataResultLabel(state, outcome, runKind, rec.PRURL, rec.PRState, rec.PRMerged),
		RunKind:        runKind,
		TriggerSource:  run.TriggerSource,
		JobID:          run.JobID,
		JobExecutionID: run.JobExecutionID,
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
		UpdatedAt:      appDataRunTimestamp(rec, run, hasRun),
		CreatedAt:      formatOptionalTime(rec.CreatedAt),
		Milestones:     milestones,
		TaskOptions:    chatTaskOptionsForAPI(rec.TaskOptions),
	}
}

func appDataRunTimestamp(rec convstore.Record, run runstore.Run, hasRun bool) string {
	if hasRun && !run.UpdatedAt.IsZero() {
		return run.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !rec.UpdatedAt.IsZero() {
		return rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return formatOptionalTime(rec.CreatedAt)
}

func (b *Bot) latestRunsForAppData(ctx context.Context, orgID string, recs []convstore.Record) map[string]runstore.Run {
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
		b.log.Warn("app data latest runs", "org", orgID, "threads", len(threadIDs), "error", err)
		return out
	}
	return runs
}

func appDataPRForRun(run appDataRun) appDataPullRequest {
	validationRequired := chatTaskOptionEnabled(run.TaskOptions, chatTaskValidateKey)
	reviewRequired := chatTaskOptionEnabled(run.TaskOptions, chatTaskReviewCodeBeforePushKey) ||
		chatTaskOptionEnabled(run.TaskOptions, chatTaskActionPRChecksForDoneKey)
	verified := run.Outcome == runstore.OutcomeCompletedWithVerifiedPR
	legacySucceeded := run.Outcome == "" && run.State == runstore.StateSucceeded
	validationPassed := !validationRequired || verified || legacySucceeded
	reviewPassed := !reviewRequired || verified || legacySucceeded
	return appDataPullRequest{
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

func appDataPRIsActionable(run appDataRun) bool {
	if strings.TrimSpace(run.PRURL) == "" || run.PRMerged {
		return false
	}
	return strings.ToLower(strings.TrimSpace(run.PRState)) != githubPRStateClosed
}
