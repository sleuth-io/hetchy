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

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

const rawDaytonaCommandLogTimeout = 30 * time.Second

func (b *Bot) handleRecoverySetupError(ctx context.Context, run runstore.Run, live *liveRun, title, body string, err error) {
	if isPermanentRecoverySandboxError(err) {
		b.finishRecoveredFailure(ctx, run, live, title, body, err)
		return
	}
	b.deferRecoveryForRetry(run, "sandbox setup", err)
}

// deferRecoveryForRetry logs a transient failure that keeps a run in
// StateRecovering so the next recovery sweep can pick it up, then writes the
// state update. The Warn log gives operators visibility into upstream outages
// that defer agent run retries.
func (b *Bot) deferRecoveryForRetry(run runstore.Run, reason string, err error) {
	b.log.Warn("agent run recovery deferred by transient outage",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"sandbox", run.SandboxID,
		"session", run.SessionID,
		"command", run.CommandID,
		"command_step", run.CommandStep,
		"reason", reason,
		"error", err,
	)
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
}

func isPermanentRecoverySandboxError(err error) bool {
	if _, ok := errors.AsType[*sdkerrors.DaytonaNotFoundError](err); ok {
		return true
	}
	if daytonaErr, ok := errors.AsType[*sdkerrors.DaytonaError](err); ok {
		return daytonaErr.StatusCode == http.StatusNotFound ||
			(daytonaErr.StatusCode == 0 && strings.Contains(daytonaErr.Message, "Sandbox failed to start"))
	}
	return false
}

func (b *Bot) ensureSandboxStarted(ctx context.Context, sb *daytona.Sandbox) error {
	if b.ensureSandboxStartedFn != nil {
		return b.ensureSandboxStartedFn(ctx, sb)
	}
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
	if b.commandLogSnapshotFn != nil {
		return b.commandLogSnapshotFn(ctx, sb, sessionID, commandID)
	}
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

func (b *Bot) sessionCommandStatus(ctx context.Context, sb *daytona.Sandbox, sessionID, commandID string) (map[string]any, error) {
	if b.sessionCommandStatusFn != nil {
		return b.sessionCommandStatusFn(ctx, sb, sessionID, commandID)
	}
	return sb.Process.GetSessionCommand(ctx, sessionID, commandID)
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
	if recoveredRunUsesFollowUpRunner(run) {
		if len(rec.History) > 0 && rec.History[len(rec.History)-1] == run.UserRequest && len(rec.ResponseBlocks) == len(rec.History) {
			rec.ResponseBlocks[len(rec.ResponseBlocks)-1] = turnBlocks
		} else {
			appendBlocksAsNewTurn(&rec, run.UserRequest, turnBlocks)
		}
	} else {
		if len(rec.History) == 0 {
			rec.History = []string{run.UserRequest}
		}
		if len(rec.ResponseBlocks) == 0 {
			rec.ResponseBlocks = [][]blocks.Block{turnBlocks}
		} else {
			rec.ResponseBlocks[0] = turnBlocks
		}
	}
	if err := b.convs.Upsert(ctx, rec); err != nil {
		return err
	}
	if prURL != "" {
		b.refreshConversationPRStateBestEffort(ctx, rec.OrgID, rec.ThreadID, prURL)
	}
	return nil
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
