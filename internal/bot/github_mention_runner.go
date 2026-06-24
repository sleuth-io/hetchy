package bot

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

func (b *Bot) runGithubExternalPRUpdate(ctx context.Context, oc orgcfg.Config, ev githubMentionEvent, route githubMentionRoute, out blocks.Emitter) {
	model := normalizeClaudeModel(ClaudeModelOpus)
	ctx, _, ok := b.prepareGithubMentionRun(ctx, oc.OrgID, route.threadID, route.requestID, route.text, out)
	if !ok {
		return
	}
	b.runGithubExternalPRUpdatePrepared(ctx, oc, ev, route, model, out)
}

func (b *Bot) prepareGithubMentionRun(ctx context.Context, orgID, threadID, requestID, text string, out blocks.Emitter) (context.Context, runstore.Run, bool) {
	return b.prepareAgentRun(ctx, orgID, threadID, requestID, text, out)
}

func (b *Bot) runGithubExternalPRUpdatePrepared(ctx context.Context, oc orgcfg.Config, ev githubMentionEvent, route githubMentionRoute, model ClaudeModel, out blocks.Emitter) {
	run, _ := agentRunFromContext(ctx)
	recorder := blocks.NewRecorder(maxBlocksForRun(run))
	emitters := []blocks.Emitter{recorder, out}
	if run.ID != "" && b.runs != nil && b.runs.Enabled() {
		runEmitter := newAgentRunEmitter(b.runs, run.ID, b.workerID, liveRunFromContext(ctx))
		ctx = contextWithAgentRunEmitter(ctx, runEmitter)
		emitters = []blocks.Emitter{runEmitter, recorder, out}
	}
	emit := blocks.Tee(emitters...)
	b.markRunKind(ctx, "github_mention")

	opts, taskOptions := resolveChatTaskOptions(nil, chatTaskOptionPatch{})
	rec := convstore.Record{
		OrgID:          oc.OrgID,
		ThreadID:       route.threadID,
		History:        []string{githubPullRequestSeedText(ev)},
		ResponseBlocks: [][]blocks.Block{{}},
		GitHubOwner:    ev.Owner,
		GitHubRepo:     ev.Repo,
		PRURL:          firstNonEmpty(ev.SubjectURL, canonicalGitHubPRURL(ev.Owner, ev.Repo, ev.SubjectNumber)),
		Branch:         githubPRHeadRef(ev.PullRequest),
		CreatorID:      "github:" + ev.AuthorLogin,
		Model:          string(model),
		TaskOptions:    taskOptions,
	}
	if route.existing != nil {
		rec = cloneGithubMentionRecord(*route.existing)
		if len(rec.History) == 0 {
			rec.History = []string{githubPullRequestMentionText(ev)}
			rec.ResponseBlocks = [][]blocks.Block{{}}
		}
		rec.GitHubOwner = firstNonEmpty(rec.GitHubOwner, ev.Owner)
		rec.GitHubRepo = firstNonEmpty(rec.GitHubRepo, ev.Repo)
		rec.PRURL = firstNonEmpty(rec.PRURL, firstNonEmpty(ev.SubjectURL, canonicalGitHubPRURL(ev.Owner, ev.Repo, ev.SubjectNumber)))
		rec.Branch = firstNonEmpty(rec.Branch, githubPRHeadRef(ev.PullRequest))
		model = modelForConversation(rec, model)
		opts, taskOptions = resolveChatTaskOptions(rec.TaskOptions, chatTaskOptionPatch{})
		rec.TaskOptions = taskOptions
		rec.Model = string(model)
	}
	if title, body, missing := missingCredentialError(model, oc); missing {
		b.log.Warn("org missing agent credentials", "org", oc.OrgID, "model", model, "provider", modelProvider(model))
		emit.Error(title, body)
		b.markRunState(ctx, runstore.StateFailed, errors.New("missing agent credentials"))
		return
	}
	headBranch := strings.TrimSpace(githubPRHeadRef(ev.PullRequest))
	if headBranch == "" {
		emit.Error("Pull request branch unavailable", "GitHub did not include a head branch for this pull request, so Hetchy cannot update it.")
		appendBlocksAsNewTurn(&rec, route.text, recorder.Snapshot())
		_ = b.convs.Upsert(context.Background(), rec)
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "github_pr_head_ref", "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateFailed, errors.New("missing pull request head ref"))
		return
	}
	rec.Branch = headBranch
	baseRepo, headRepo, fork := githubMentionForkPullRequest(ev)
	if fork {
		emit.Error("Fork pull request unsupported", "Hetchy can only update pull requests whose head branch is in the same repository. Fork-based pull request updates need a separate permission model.")
		appendBlocksAsNewTurn(&rec, route.text, recorder.Snapshot())
		_ = b.convs.Upsert(context.Background(), rec)
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "fork_pr", "pr_url": rec.PRURL, "head_repo": headRepo, "base_repo": baseRepo})
		b.markRunState(ctx, runstore.StateFailed, errors.New("fork pull request unsupported"))
		return
	}

	agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, rec.AgentSlug, emit)
	if !ok {
		b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
		return
	}
	rec.AgentSlug = agent.Slug
	if err := b.convs.Upsert(context.Background(), rec); err != nil {
		b.log.Error("convstore upsert (github external PR seed)", "error", err, "org", oc.OrgID, "thread", route.threadID)
	}
	b.runGithubExternalPRUpdateWithAgent(ctx, oc, ev, route, rec, agent, opts, model, recorder, emit)
}

func githubPullRequestSeedText(ev githubMentionEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Existing GitHub pull request context for %s/%s#%d.\n", ev.Owner, ev.Repo, ev.SubjectNumber)
	if ev.SubjectURL != "" {
		fmt.Fprintf(&b, "Pull request: %s\n", ev.SubjectURL)
	}
	if ev.SubjectTitle != "" {
		fmt.Fprintf(&b, "Title: %s\n", ev.SubjectTitle)
	}
	if ev.PullRequest != nil {
		baseRef, headRef := githubPRBaseRef(ev.PullRequest), githubPRHeadRef(ev.PullRequest)
		if baseRef != "" || headRef != "" {
			fmt.Fprintf(&b, "Base: %s\nHead: %s\n", baseRef, headRef)
		}
	}
	if body := strings.TrimSpace(ev.SubjectBody); body != "" {
		fmt.Fprintf(&b, "\nPull request body:\n%s\n", truncate(body, 4000))
	}
	return b.String()
}

func cloneGithubMentionRecord(rec convstore.Record) convstore.Record {
	rec.History = append([]string(nil), rec.History...)
	rec.ResponseBlocks = cloneResponseBlocksForGithubMention(rec.ResponseBlocks)
	rec.TaskOptions = cloneTaskOptionsForGithubMention(rec.TaskOptions)
	return rec
}

func cloneResponseBlocksForGithubMention(in [][]blocks.Block) [][]blocks.Block {
	if in == nil {
		return nil
	}
	out := make([][]blocks.Block, len(in))
	for i := range in {
		out[i] = append([]blocks.Block(nil), in[i]...)
	}
	return out
}

func cloneTaskOptionsForGithubMention(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	maps.Copy(out, in)
	return out
}

func (b *Bot) runGithubExternalPRUpdateWithAgent(ctx context.Context, oc orgcfg.Config, ev githubMentionEvent, route githubMentionRoute, rec convstore.Record, agent agents.Profile, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	b.log.Info("github mention external PR update received",
		"org", oc.OrgID, "repo", ev.Owner+"/"+ev.Repo, "pr", ev.SubjectNumber,
		"thread", route.threadID, "branch", rec.Branch, "model", model)
	repo, err := b.resolveRepoForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		b.log.Warn("resolve repo for github mention failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo access lost", fmt.Sprintf("Lost access to `%s/%s` - check the GitHub App install at /settings/org -> Integrations.", rec.GitHubOwner, rec.GitHubRepo))
		appendBlocksAsNewTurn(&rec, route.text, recorder.Snapshot())
		_ = b.convs.Upsert(context.Background(), rec)
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "github_mention_repo_resolution", "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	flavor, ok := b.admitBillingForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo, emit)
	if !ok {
		appendBlocksAsNewTurn(&rec, route.text, recorder.Snapshot())
		_ = b.convs.Upsert(context.Background(), rec)
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "github_mention_billing_admission", "pr_url": rec.PRURL})
		return
	}
	sb, repo, ok := b.createGithubMentionUpdateSandbox(ctx, oc, repo, flavor, rec, route.text, route.requestID, recorder, emit)
	if !ok {
		return
	}
	rec.SandboxID = sb.ID
	rec.Branch = githubPRHeadRef(ev.PullRequest)
	b.markRunBranch(ctx, rec.Branch)
	b.markRunSandbox(ctx, sb.ID)
	setLiveRunSandboxID(ctx, sb.ID, true)
	if err := b.convs.Upsert(context.Background(), rec); err != nil {
		b.log.Error("convstore upsert (github external PR sandbox)", "error", err, "org", oc.OrgID, "thread", route.threadID)
	}
	if agent.Slug == "" {
		emit.Notify("Resuming", fmt.Sprintf("Updating pull request #%d in `%s`.", ev.SubjectNumber, repo.Slug))
	} else {
		emit.Notify("Resuming", fmt.Sprintf("Updating pull request #%d in `%s` with `%s`.", ev.SubjectNumber, repo.Slug, agent.DisplayName))
	}
	agentText, err := b.promptWithSandboxAttachments(ctx, sb, rec.OrgID, rec.ThreadID, len(rec.History), route.requestID, route.text, emit)
	if err != nil {
		b.log.Error("github mention attachment upload failed", "sandbox", sb.ID, "request_id", route.requestID, "error", err)
		emit.Error("Attachment upload failed", fmt.Sprintf("Could not copy the attached files into sandbox `%s`. Try again.", sb.ID))
		appendBlocksAsNewTurn(&rec, route.text, recorder.Snapshot())
		_ = b.convs.Upsert(context.Background(), rec)
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "github_mention_attachment_upload", "sandbox_id": sb.ID, "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	recForPersist := rec
	recForPersist.History = append(append([]string(nil), rec.History...), route.text)
	persister := newChatPersister(b.log, b.convs, recorder, recForPersist, appendAsNewTurn, 2*time.Second)
	persisterCtx, cancelPersister := context.WithCancel(ctx)
	go persister.Run(persisterCtx)
	defer func() {
		cancelPersister()
		persister.Stop()
	}()

	runEmit := newPRURLPersistingEmitter(b.log, b.convs, rec, emit)
	runCtx, housekeeping := contextWithPostPRHousekeeping(ctx)
	prURL, err := b.runFollowUpForRequest(runCtx, sb, repo, oc, rec, agent, agentText, route.requestID, opts, model, followUpModeChange, runEmit)
	if err != nil {
		b.handleFollowUpRunError(ctx, sb, &rec, route.text, recorder, route.requestID, err, runEmit)
		return
	}
	autoMergeDetail := map[string]any{}
	if prURL != "" {
		autoMergeDetail = b.handleAutoMergeAfterVerifiedPR(ctx, rec, prURL, opts, recorder, runEmit)
	}
	resultBody := prURL
	if resultBody == "" {
		resultBody = noPullRequestResultBody(strings.TrimSpace(rec.PRURL) != "")
	}
	emit.Result("Done!", resultBody)
	if err := agentRunDurabilityErr(ctx); err != nil {
		b.markRunState(ctx, runstore.StateRecovering, err)
		return
	}
	housekeeping.run(ctx)
	if prURL != "" {
		rec.PRURL = prURL
	}
	appendBlocksAsNewTurn(&rec, route.text, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert (github external PR done)", "error", err)
		b.markRunOutcome(ctx, runstore.OutcomeFailedRuntime, map[string]any{"reason": "persist_github_external_pr"})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	if rec.PRURL != "" {
		b.refreshConversationPRStateBestEffort(ctx, rec.OrgID, rec.ThreadID, rec.PRURL)
	}
	detail := mergeAutoMergeOutcomeDetail(map[string]any{"pr_url": rec.PRURL, "branch": rec.Branch}, autoMergeDetail)
	b.markCompletedRunOutcome(ctx, recorder.Snapshot(), runstore.OutcomeCompletedWithVerifiedPR, detail)
	b.markRunState(ctx, runstore.StateSucceeded, nil)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+route.requestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func (b *Bot) createGithubMentionUpdateSandbox(ctx context.Context, oc orgcfg.Config, repo repoCtx, flavor billing.Flavor, rec convstore.Record, text, requestID string, recorder *blocks.Recorder, emit blocks.Emitter) (*daytona.Sandbox, repoCtx, bool) {
	sb, updatedRepo, err := b.createFollowUpReplacementSandbox(ctx, oc, repo, flavor)
	if err == nil {
		return sb, updatedRepo, true
	}
	b.log.Error("github mention sandbox create failed", "request_id", requestID, "error", err)
	emit.Error("Sandbox create failed", "Could not create an isolated sandbox to update this pull request. Try again in a minute.")
	appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
	if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
		b.log.Error("convstore upsert (github mention sandbox create fail)", "error", uerr)
	}
	b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "github_mention_sandbox_create", "pr_url": rec.PRURL})
	b.markRunState(ctx, runstore.StateFailed, err)
	return nil, repo, false
}
