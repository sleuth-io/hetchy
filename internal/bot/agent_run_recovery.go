package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

const recoverySweepInterval = 30 * time.Second
const rawDaytonaCommandLogTimeout = 30 * time.Second

func (b *Bot) runRecoveryLoop(ctx context.Context) {
	if b.runs == nil || !b.runs.Enabled() || b.daytona == nil {
		return
	}
	b.recoverExpiredRuns(ctx)
	t := time.NewTicker(recoverySweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.recoverExpiredRuns(ctx)
		}
	}
}

func (b *Bot) recoverExpiredRuns(ctx context.Context) {
	runs, err := b.runs.ListExpired(ctx, 5)
	if err != nil {
		b.log.Warn("agent run recovery list failed", "error", err)
		return
	}
	for _, run := range runs {
		claimed, err := b.runs.Claim(ctx, run.ID, b.workerID, agentRunLeaseDuration)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				b.log.Debug("agent run recovery claim lost race", "run_id", run.ID, "org", run.OrgID, "thread", run.ThreadID)
			} else {
				b.log.Warn("agent run recovery claim failed", "run_id", run.ID, "org", run.OrgID, "thread", run.ThreadID, "error", err)
			}
			continue
		}
		go b.recoverAgentRun(ctx, claimed)
	}
}

func (b *Bot) recoverAgentRun(ctx context.Context, run runstore.Run) {
	ctx = context.WithoutCancel(ctx)
	var live *liveRun
	var registeredLive bool
	if b.live != nil {
		live, registeredLive = b.live.RegisterIfAbsent(ctx, run.OrgID, run.ThreadID)
		if live != nil && run.SandboxID != "" {
			live.SetSandboxID(run.SandboxID, run.RunKind != "followup")
		}
		if registeredLive {
			defer b.live.Done(run.OrgID, run.ThreadID, live)
		}
	}
	if run.SandboxID == "" || run.SessionID == "" || run.CommandID == "" {
		err := errors.New("run has no recoverable Daytona command")
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", "The interrupted run did not reach a recoverable sandbox command.", err)
		return
	}

	sb, err := b.daytona.Get(ctx, run.SandboxID)
	if err != nil {
		b.handleRecoverySetupError(ctx, run, live, "Agent failed", "The interrupted sandbox no longer exists, so this run cannot be recovered.", err)
		return
	}
	if err := b.ensureSandboxStarted(ctx, sb); err != nil {
		b.handleRecoverySetupError(ctx, run, live, "Agent failed", "The interrupted sandbox could not be restarted because it no longer exists.", err)
		return
	}

	existingEvents, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	em := newRecoveredAgentRunEmitter(b.runs, run, b.workerID, live, existingEvents)
	router := newAgentLineRouter(em)
	frameState := replayFrameState{}
	replayCursor := int64(0)

	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()
	for {
		b.runs.TouchLease(context.Background(), run.ID, b.workerID, agentRunLeaseDuration)

		logText, err := b.commandLogSnapshot(ctx, sb, run.SessionID, run.CommandID)
		if err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}

		if int64(len(logText)) < replayCursor {
			frameState = replayFrameState{}
			replayCursor = 0
		}
		replayText := logText[replayCursor:]
		res, nextFrameState := replayHetchyFramedLogState(run.ID, replayText, replayCursor, run.LogCursor, frameState, em, func(line string) {
			em.BeginBatch()
			router.Line(line)
		}, func(cursor int64) {
			_ = em.FlushBatch(cursor)
		})
		frameState = nextFrameState
		if err := em.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		if !res.SeenBegin {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, errMissingHetchyFrame(run.ID).Error(), b.workerID)
			return
		}
		run.LogCursor = max(run.LogCursor, res.Cursor)
		replayCursor = res.Cursor

		status, err := sb.Process.GetSessionCommand(ctx, run.SessionID, run.CommandID)
		if err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		if code, ok := sessionCommandExitCode(status); ok {
			b.finalizeRecoveredRun(ctx, sb, run, router, em, code, live)
			return
		}

		<-poll.C
	}
}

func (b *Bot) handleRecoverySetupError(ctx context.Context, run runstore.Run, live *liveRun, title, body string, err error) {
	if isPermanentRecoverySandboxError(err) {
		b.finishRecoveredFailure(ctx, run, live, title, body, err)
		return
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
}

func isPermanentRecoverySandboxError(err error) bool {
	var notFound *sdkerrors.DaytonaNotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var daytonaErr *sdkerrors.DaytonaError
	if errors.As(err, &daytonaErr) {
		return daytonaErr.StatusCode == http.StatusNotFound ||
			(daytonaErr.StatusCode == 0 && strings.Contains(daytonaErr.Message, "Sandbox failed to start"))
	}
	return false
}

func (b *Bot) finalizeRecoveredRun(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, router *agentLineRouter, replayEm *agentRunEmitter, exitCode int64, live *liveRun) {
	if exitCode != 0 {
		router.Abort()
		if err := replayEm.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		err := fmt.Errorf("agent command exited %d during recovery", exitCode)
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", fmt.Sprintf("The recovered agent command exited with status %d.", exitCode), err)
		return
	}
	prURL := router.Finish()
	if err := replayEm.Err(); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	if prURL == "" {
		err := errors.New("recovered agent command finished without a PR URL")
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", "The recovered agent command finished without posting a PR URL.", err)
		return
	}

	validatedPR, branch, err := b.validateRecoveredPR(ctx, run, prURL)
	if err != nil {
		body := fmt.Sprintf("The recovered agent command reported a PR URL, but GitHub did not verify it for branch `%s`.", branch)
		if !errors.Is(err, errReportedPRNotVerified) {
			body = "The recovered agent command reported a PR URL, but Hetchy could not validate it: `" + err.Error() + "`"
		}
		b.finishRecoveredFailure(ctx, run, live, "PR not verified", body, err)
		return
	}

	body := prURL
	if validatedPR != "" {
		body = validatedPR
		prURL = validatedPR
	}
	if run.RunKind != "followup" {
		body += "\n\nReply here to make further changes to this PR."
	}
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	if !recoveredRunHasTerminalBlock(events, blocks.KindResult) {
		em := b.recoveredTerminalEmitter(run, live, events)
		em.Result("Done!", body)
		if err := em.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		events, err = b.runs.EventsAfter(ctx, run.ID, 0)
		if err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
	}
	if err := b.projectRecoveredConversation(ctx, run, prURL, events); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateSucceeded, "", b.workerID)
	b.deleteSandboxSession(sb, run.SessionID)
	b.cleanupSandbox(ctx, sb, "recovered successful run")
}

func (b *Bot) finishRecoveredFailure(ctx context.Context, run runstore.Run, live *liveRun, title, body string, cause error) {
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	if !recoveredRunHasTerminalBlock(events, blocks.KindError) {
		em := b.recoveredTerminalEmitter(run, live, events)
		em.Error(title, body)
		if err := em.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		events, err = b.runs.EventsAfter(ctx, run.ID, 0)
		if err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
	}
	if err := b.projectRecoveredConversation(ctx, run, "", events); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	lastErr := ""
	if cause != nil {
		lastErr = cause.Error()
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateFailed, lastErr, b.workerID)
}

func (b *Bot) recoveredTerminalEmitter(run runstore.Run, live *liveRun, events []runstore.Event) *agentRunEmitter {
	return newAgentRunEmitterAfterEvents(b.runs, run.ID, b.workerID, live, events)
}

func (b *Bot) validateRecoveredPR(ctx context.Context, run runstore.Run, prURL string) (string, string, error) {
	rec, err := b.convs.Get(ctx, run.OrgID, run.ThreadID)
	if err != nil {
		return "", run.Branch, err
	}
	repo, err := b.resolveRepo(ctx, run.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		return "", run.Branch, err
	}
	branch := run.Branch
	if branch == "" {
		branch = rec.Branch
	}
	if branch == "" && run.RequestID != "" {
		branch = "feature/sf-" + run.RequestID
	}
	validated, err := b.validateReportedPR(ctx, repo, branch, repo.BaseBranch, prURL)
	return validated, branch, err
}

func recoveredRunHasTerminalBlock(events []runstore.Event, kind blocks.Kind) bool {
	for _, block := range blocksFromRunEvents(events) {
		if block.Kind == kind && block.Status != blocks.StatusStreaming {
			return true
		}
	}
	return false
}

func (b *Bot) ensureSandboxStarted(ctx context.Context, sb *daytona.Sandbox) error {
	if sb == nil {
		return errors.New("nil sandbox")
	}
	if err := sb.RefreshData(ctx); err != nil {
		b.log.Debug("sandbox refresh failed, proceeding with stale state", "sandbox", sb.ID, "error", err)
	}
	if strings.EqualFold(string(sb.State), "started") {
		return nil
	}
	start := b.startFn
	if start == nil {
		start = func(ctx context.Context, sb *daytona.Sandbox, timeout time.Duration) error {
			return sb.StartWithTimeout(ctx, timeout)
		}
	}
	return start(ctx, sb, 5*time.Minute)
}

func (b *Bot) commandLogSnapshot(ctx context.Context, sb *daytona.Sandbox, sessionID, commandID string) (string, error) {
	logs, err := sb.Process.GetSessionCommandLogs(ctx, sessionID, commandID)
	if err == nil && logs != nil {
		switch {
		case logs.Output != "":
			return logs.Output, nil
		case logs.Stdout != "" && logs.Stderr == "":
			return logs.Stdout, nil
		default:
			return combineCommandOutput(logs.Stdout, logs.Stderr), nil
		}
	}
	raw, rawErr := rawDaytonaCommandLogs(ctx, sb, sessionID, commandID)
	if rawErr != nil {
		if err != nil {
			return "", fmt.Errorf("get command logs: %w; raw fallback: %w", err, rawErr)
		}
		return "", rawErr
	}
	return raw, nil
}

func rawDaytonaCommandLogs(ctx context.Context, sb *daytona.Sandbox, sessionID, commandID string) (string, error) {
	if sb == nil || sb.ToolboxClient == nil {
		return "", errors.New("sandbox has no toolbox client")
	}
	cfg := sb.ToolboxClient.GetConfig()
	if len(cfg.Servers) == 0 {
		return "", errors.New("toolbox client has no server URL")
	}
	base := strings.TrimRight(cfg.Servers[0].URL, "/")
	u := base + "/process/session/" + url.PathEscape(sessionID) + "/command/" + url.PathEscape(commandID) + "/logs"
	fetchCtx, cancel := context.WithTimeout(ctx, rawDaytonaCommandLogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain, application/json")
	for k, v := range cfg.DefaultHeader {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("raw command logs status %s: %s", resp.Status, truncate(string(body), 300))
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		var decoded struct {
			Output string `json:"output"`
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if err := json.Unmarshal(body, &decoded); err == nil {
			if decoded.Output != "" {
				return decoded.Output, nil
			}
			return combineCommandOutput(decoded.Stdout, decoded.Stderr), nil
		}
	}
	return string(body), nil
}

func combineCommandOutput(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	case strings.HasSuffix(stdout, "\n"):
		return stdout + stderr
	default:
		return stdout + "\n" + stderr
	}
}

func sessionCommandExitCode(status map[string]any) (int64, bool) {
	v, ok := status["exitCode"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

func (b *Bot) projectRecoveredConversation(ctx context.Context, run runstore.Run, prURL string, events []runstore.Event) error {
	rec, err := b.convs.Get(ctx, run.OrgID, run.ThreadID)
	if errors.Is(err, convstore.ErrNotFound) {
		rec = convstore.Record{
			OrgID:    run.OrgID,
			ThreadID: run.ThreadID,
			History:  []string{run.UserRequest},
		}
	} else if err != nil {
		return err
	}
	turnBlocks := blocksFromRunEvents(events)
	rec.SandboxID = run.SandboxID
	if run.Branch != "" {
		rec.Branch = run.Branch
	}
	if prURL != "" {
		rec.PRURL = prURL
	}
	switch run.RunKind {
	case "followup":
		if len(rec.History) > 0 && rec.History[len(rec.History)-1] == run.UserRequest && len(rec.ResponseBlocks) == len(rec.History) {
			rec.ResponseBlocks[len(rec.ResponseBlocks)-1] = turnBlocks
		} else {
			appendBlocksAsNewTurn(&rec, run.UserRequest, turnBlocks)
		}
	default:
		if len(rec.History) == 0 {
			rec.History = []string{run.UserRequest}
		}
		if len(rec.ResponseBlocks) == 0 {
			rec.ResponseBlocks = [][]blocks.Block{turnBlocks}
		} else {
			rec.ResponseBlocks[0] = turnBlocks
		}
	}
	return b.convs.Upsert(ctx, rec)
}

func blocksFromRunEvents(events []runstore.Event) []blocks.Block {
	byID := map[string]*blocks.Block{}
	order := []string{}
	for _, ev := range events {
		var payload sseEvent
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			continue
		}
		switch ev.Event {
		case "block_start":
			if payload.ID == "" {
				continue
			}
			if _, exists := byID[payload.ID]; !exists {
				order = append(order, payload.ID)
			}
			byID[payload.ID] = &blocks.Block{
				ID:        payload.ID,
				Kind:      payload.Kind,
				Title:     payload.Title,
				Status:    blocks.StatusStreaming,
				Meta:      payload.Meta,
				StartedAt: payload.StartedAt,
			}
		case "block_append":
			if b := byID[payload.ID]; b != nil {
				b.Body += payload.Delta
			}
		case "block_done":
			if b := byID[payload.ID]; b != nil {
				b.Status = payload.Status
				if b.Status == "" {
					b.Status = blocks.StatusDone
				}
				b.Summary = payload.Summary
				b.EndedAt = payload.EndedAt
			}
		}
	}
	out := make([]blocks.Block, 0, len(order))
	for _, id := range order {
		if b := byID[id]; b != nil {
			out = append(out, *b)
		}
	}
	return out
}
