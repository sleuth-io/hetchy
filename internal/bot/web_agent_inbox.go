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
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
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

func (b *Bot) agentInboxRunForConversation(ctx context.Context, orgID string, rec convstore.Record) agentInboxRun {
	run, hasRun := b.latestRunForInbox(ctx, orgID, rec.ThreadID)
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
	if !rec.CreatedAt.IsZero() {
		return rec.CreatedAt.UTC().Format(time.RFC3339)
	}
	return formatOptionalTime(rec.UpdatedAt)
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
		return "Waiting for latest activity."
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
	summary := summarizeAgentInboxEvents(events)
	blocks := summary.blocks
	blockOrder := summary.order
	for i := len(events) - 1; i >= 0; i-- {
		var payload sseEvent
		if err := json.Unmarshal(events[i].Data, &payload); err != nil {
			continue
		}
		switch events[i].Event {
		case "heartbeat":
			if text := compactActivityText(payload.Title, payload.Delta); text != "" && activityLineIsUseful(text) {
				return text
			}
		case "block_append":
			if line := activityTextForBlock(blocks[payload.ID]); line != "" {
				return line
			}
		case "block_start":
			if text := strings.TrimSpace(payload.Title); text != "" {
				return text
			}
		case "block_done":
			if line := activityTextForBlock(blocks[payload.ID]); line != "" {
				return line
			}
		}
	}
	for i := len(blockOrder) - 1; i >= 0; i-- {
		if line := activityTextForBlock(blocks[blockOrder[i]]); line != "" {
			return line
		}
	}
	return friendlyRunCommandStep(run.CommandStep)
}

func summarizeAgentInboxEvents(events []runstore.Event) agentInboxEventSummary {
	blocks := map[string]*agentInboxActivityBlock{}
	blockOrder := make([]string, 0)
	rememberBlock := func(payload sseEvent) *agentInboxActivityBlock {
		if payload.ID == "" {
			return nil
		}
		block := blocks[payload.ID]
		if block == nil {
			block = &agentInboxActivityBlock{}
			blocks[payload.ID] = block
			blockOrder = append(blockOrder, payload.ID)
		}
		if payload.Kind != "" {
			block.kind = payload.Kind
		}
		if payload.Title != "" {
			block.title = payload.Title
		}
		if payload.Summary != "" {
			block.summary = payload.Summary
		}
		return block
	}
	for _, event := range events {
		var payload sseEvent
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		block := rememberBlock(payload)
		if block != nil && event.Event == "block_append" {
			block.body.WriteString(payload.Delta)
		}
	}
	return agentInboxEventSummary{blocks: blocks, order: blockOrder}
}

type agentInboxEventSummary struct {
	blocks map[string]*agentInboxActivityBlock
	order  []string
}

type agentInboxActivityBlock struct {
	kind    blocks.Kind
	title   string
	summary string
	body    strings.Builder
}

func activityTextForBlock(block *agentInboxActivityBlock) string {
	if block == nil {
		return ""
	}
	if block.kind == blocks.KindToolUse {
		if text := strings.TrimSpace(block.title); text != "" {
			return text
		}
		if text := strings.TrimSpace(block.summary); text != "" {
			return text
		}
	}
	if line := lastMeaningfulActivityLine(block.body.String()); line != "" {
		return line
	}
	if text := strings.TrimSpace(block.title); text != "" {
		return text
	}
	return strings.TrimSpace(block.summary)
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

func lastMeaningfulActivityLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	useful := make([]string, 0, len(lines))
	inFence := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if isMarkdownFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if activityLineIsUseful(line) {
			useful = append(useful, line)
		}
	}
	if len(useful) == 0 {
		return ""
	}
	return useful[len(useful)-1]
}

func activityLineIsUseful(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || isMarkdownFenceLine(line) {
		return false
	}
	for _, r := range line {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func isMarkdownFenceLine(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < 3 {
		return false
	}
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
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

func agentInboxCurrentStep(run runstore.Run, events []runstore.Event) string {
	if run.State == runstore.StateRecovering {
		return "Recovering"
	}
	if run.State == runstore.StatePreparing && run.SandboxID == "" {
		if step := currentStepFromCommand(run.CommandStep); step != "" {
			return step
		}
		return "Starting"
	}
	summary := summarizeAgentInboxEvents(events)
	for i := len(events) - 1; i >= 0; i-- {
		var payload sseEvent
		if err := json.Unmarshal(events[i].Data, &payload); err != nil {
			continue
		}
		switch events[i].Event {
		case "heartbeat":
			if step := currentStepFromLifecycleText(compactActivityText(payload.Title, payload.Delta)); step != "" {
				return step
			}
		case "block_start", "block_append", "block_done":
			block := summary.blocks[payload.ID]
			if step := currentStepFromBlock(block, payload); step != "" {
				if run.State == runstore.StateFinalizing && step == "Coding" {
					continue
				}
				return step
			}
		}
	}
	if run.State == runstore.StateFinalizing {
		return "Validating"
	}
	if step := currentStepFromCommand(run.CommandStep); step != "" {
		return step
	}
	switch run.State {
	case runstore.StatePreparing:
		return "Starting"
	case runstore.StateRunning, runstore.StateRecovering:
		return "Coding"
	case runstore.StateFinalizing:
		return "Validating"
	default:
		return ""
	}
}

func currentStepFromBlock(block *agentInboxActivityBlock, payload sseEvent) string {
	kind := payload.Kind
	var title, summary, body string
	if block != nil {
		if block.kind != "" {
			kind = block.kind
		}
		title = block.title
		summary = block.summary
		body = lastMeaningfulActivityLine(block.body.String())
	}
	if payload.Title != "" {
		title = payload.Title
	}
	if payload.Summary != "" {
		summary = payload.Summary
	}
	if payload.Delta != "" && body == "" {
		body = payload.Delta
	}
	return currentStepFromKindAndText(kind, title, summary, body)
}

func currentStepFromKindAndText(kind blocks.Kind, parts ...string) string {
	text := compactActivityText(parts...)
	lower := strings.ToLower(text)
	if lower == "" {
		return ""
	}
	switch kind {
	case blocks.KindSetup:
		return currentStepFromSetupText(lower)
	case blocks.KindNotify:
		return currentStepFromNotifyText(lower)
	case blocks.KindClaudeText, blocks.KindToolUse:
		if step := currentStepFromAgentText(lower); step != "" {
			return step
		}
		return "Coding"
	case blocks.KindResult:
		if currentStepTextIndicatesPR(lower) {
			return "PR"
		}
		return "Validating"
	case blocks.KindError:
		return currentStepFromLifecycleText(lower)
	default:
		return currentStepFromLifecycleText(lower)
	}
}

func currentStepFromCommand(step string) string {
	switch {
	case step == "":
		return ""
	case strings.HasPrefix(step, "bootstrap"):
		return "Bootstrap"
	case strings.HasPrefix(step, "setup-clone") || step == "detect-tar" || step == "write-script" || step == "write-env":
		return "Sandbox"
	case step == "run-script":
		return "Coding"
	default:
		if strings.Contains(step, "valid") || strings.Contains(step, "review") || strings.Contains(step, "check") || strings.Contains(step, "test") {
			return "Validating"
		}
		return ""
	}
}

func currentStepFromSetupText(text string) string {
	switch {
	case containsAny(text, "resuming sandbox", "starting sandbox"):
		return "Resuming"
	case containsAny(text, "sandbox cleanup", "cleanup complete", "archiv"):
		return "Cleanup"
	case containsAny(text, "bootstrapping repo", "verifying bootstrap", "bootstrap"):
		return "Bootstrap"
	default:
		return "Sandbox"
	}
}

func currentStepFromNotifyText(text string) string {
	switch {
	case strings.HasPrefix(text, "starting"):
		return "Starting"
	case strings.HasPrefix(text, "resuming"):
		return "Resuming"
	case strings.HasPrefix(text, "attachments"):
		return "Attachments"
	case containsAny(text, "sandbox ready", "sandbox replaced"):
		return "Sandbox"
	case containsAny(text, "starting new pr", "pull request", "pr opened", "pr created"):
		return "PR"
	case containsAny(text, "skills available", "tooling degraded", "sx install", "sx skills"):
		return "Skills"
	case containsAny(text, "bootstrap spec", "agent learned"):
		return "Learning"
	case containsAny(text, "bootstrap skipped", "bootstrap"):
		return "Bootstrap"
	default:
		return currentStepFromLifecycleText(text)
	}
}

func currentStepFromAgentText(text string) string {
	switch {
	case currentStepTextIndicatesPR(text):
		return "PR"
	case containsAny(text,
		"gh pr checks",
		"gh pr view",
		"reviewdecision",
		"statuscheckrollup",
		"status check",
		"running go test",
		"run go test",
		"go test ",
		"running npm test",
		"run npm test",
		"npm test",
		"running make test",
		"make test",
		"running pytest",
		"pytest",
		"running playwright",
		"playwright test",
		"validation proof",
		"validating",
		"verification proof",
		"verifying pr",
		"review checks",
	):
		return "Validating"
	default:
		return ""
	}
}

func currentStepFromLifecycleText(text string) string {
	switch {
	case text == "":
		return ""
	case containsAny(text, "recovering", "recovered run", "reconnecting", "reattach"):
		return "Recovering"
	case strings.HasPrefix(text, "starting"):
		return "Starting"
	case strings.HasPrefix(text, "resuming") || containsAny(text, "resuming sandbox"):
		return "Resuming"
	case containsAny(text, "cleanup", "archive", "archiv"):
		return "Cleanup"
	case containsAny(text, "bootstrap spec", "agent learned"):
		return "Learning"
	case containsAny(text, "bootstrap", "bootstrapping"):
		return "Bootstrap"
	case containsAny(text, "skills available", "sx skills", "sx install", "tooling degraded"):
		return "Skills"
	case currentStepTextIndicatesPR(text):
		return "PR"
	case containsAny(text, "sandbox", "repo checkout", "cloning", "git auth", "dependency cache", "health.sh", "setup.sh", "start.sh", "stop.sh"):
		return "Sandbox"
	case containsAny(text, "validating", "validation", "verification", "review checks", "status check"):
		return "Validating"
	default:
		return ""
	}
}

func currentStepTextIndicatesPR(text string) bool {
	return containsAny(text,
		"gh pr create",
		"/pull/",
		"pull request",
		"pr opened",
		"pr created",
		"opened pr",
		"created pr",
	)
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
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
