package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
)

func (b *Bot) chatHandler(parentCtx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	oc, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil {
		http.Error(w, "org config not found — set it at /settings/org", http.StatusBadRequest)
		return
	}

	var body struct {
		Text      string  `json:"text"`
		SessionID string  `json:"session_id"`
		AgentSlug *string `json:"agent_slug,omitempty"`
		// Repository carries the composer repo-picker selection as
		// "owner/name". Optional — empty string falls back to the
		// org's saved default repo, matching the pre-picker behaviour
		// for clients that don't surface the field.
		Repository *string `json:"repository,omitempty"`
		Model      string  `json:"model,omitempty"`
		// Task option fields are pointers so missing (older clients,
		// non-web callers) is distinguishable from explicit false.
		// Missing request fields leave saved per-chat values alone;
		// missing saved keys default on in HandleRequest.
		Validate              *bool `json:"validate,omitempty"`
		ReviewCodeBeforePush  *bool `json:"review_code_before_push,omitempty"`
		ActionPRChecksForDone *bool `json:"action_pr_checks_for_done,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)
	optionPatch := chatTaskOptionPatch{}
	if body.Validate != nil {
		optionPatch[chatTaskValidateKey] = *body.Validate
	}
	if body.ReviewCodeBeforePush != nil {
		optionPatch[chatTaskReviewCodeBeforePushKey] = *body.ReviewCodeBeforePush
	}
	if body.ActionPRChecksForDone != nil {
		optionPatch[chatTaskActionPRChecksForDoneKey] = *body.ActionPRChecksForDone
	}
	model, ok := parseClaudeModel(body.Model)
	if !ok {
		http.Error(w, "invalid model: pick one from the model dropdown", http.StatusBadRequest)
		return
	}
	if modelProvider(model) == modelProviderOpenAI {
		// Saved-credentials check happens up front so the user gets a
		// clean 400 instead of the runtime tripping over a missing
		// OPENAI_API_KEY mid-stream. Codex execution is wired in a
		// follow-up PR; surface that limitation explicitly rather than
		// silently downgrading the chosen model.
		if oc.OpenAIAPIKey == "" && oc.OpenAICodexOAuthToken == "" {
			http.Error(w, "OpenAI Codex isn't configured yet — paste an API key or subscription token in /settings/org?tab=integrations.", http.StatusBadRequest)
			return
		}
		http.Error(w, "OpenAI Codex execution isn't wired into the chat runtime yet — pick Opus, Sonnet, or Haiku for now.", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Nanosecond precision (base 36 to keep the resulting branch
	// suffix short) so two concurrent requests don't generate the
	// same `feature/sf-<id>` branch name. Millisecond precision was
	// realistic to collide under load.
	requestID := strconv.FormatInt(time.Now().UnixNano(), 36)
	if sessionID == "" {
		sessionID = requestID
	}

	if b.runs != nil && b.runs.Enabled() {
		if active, err := b.runs.ActiveForThread(r.Context(), p.OrgID, sessionID); err == nil {
			b.log.Info("chat post rejected due active durable run",
				"org", p.OrgID, "thread", sessionID, "run_id", active.ID, "state", active.State)
			http.Error(w, "this chat already has a turn in flight; reload to reattach", http.StatusConflict)
			return
		} else if !errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("active run lookup for chat post", "org", p.OrgID, "thread", sessionID, "error", err)
			http.Error(w, "could not check active chat run", http.StatusInternalServerError)
			return
		}
	}

	// Atomically claim the in-flight slot. RegisterIfAbsent collapses
	// the prior Get-then-Register TOCTOU where two concurrent POSTs
	// could each observe an empty slot, both call Register, and the
	// second Close()s the first run mid-stream. On a losing call we
	// reject with 409 — the reload-to-reattach UX path uses
	// /chat/stream, not a fresh POST.
	run, registered := b.live.RegisterIfAbsent(parentCtx, p.OrgID, sessionID)
	if !registered {
		http.Error(w, "this chat already has a turn in flight; reload to reattach", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var emitter blocks.Emitter = newLiveEmitter(run)
	if b.runs != nil && b.runs.Enabled() {
		emitter = noopEmitter{}
	}

	go func() {
		defer b.live.Done(p.OrgID, sessionID, run)
		runCtx := contextWithLiveRun(run.Context(), run)
		b.HandleRequest(runCtx, oc, text, requestID, sessionID, p.UserID, optionPatch, body.AgentSlug, body.Repository, model, emitter)
	}()

	sub := run.Subscribe()
	defer run.Unsubscribe(sub)
	b.streamLiveSubscription(w, flusher, r.Context(), sub)
}

func (b *Bot) chatCancelHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	run := b.live.Get(p.OrgID, sessionID)
	if run == nil {
		if b.runs != nil && b.runs.Enabled() {
			active, err := b.runs.ActiveForThread(r.Context(), p.OrgID, sessionID)
			if err == nil {
				if err := b.cancelDurableRun(r.Context(), active, p.UserID); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						http.Error(w, "no live run", http.StatusNotFound)
						return
					}
					b.log.Warn("durable chat cancel failed", "org", p.OrgID, "thread", sessionID, "user", p.UserID, "run_id", active.ID, "error", err)
					http.Error(w, "could not cancel live run", http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				return
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				b.log.Warn("active run lookup for chat cancel", "org", p.OrgID, "thread", sessionID, "user", p.UserID, "error", err)
				http.Error(w, "could not check active chat run", http.StatusInternalServerError)
				return
			}
		}
		http.Error(w, "no live run", http.StatusNotFound)
		return
	}
	cancelled := run.Cancel()
	sandboxID, cleanupOnCancel := run.CancelCleanupSandboxID()
	b.log.Info("chat cancel requested",
		"org", p.OrgID,
		"thread", sessionID,
		"user", p.UserID,
		"sandbox", sandboxID,
		"cleanup_on_cancel", cleanupOnCancel,
		"cancelled", cancelled,
	)
	if !cancelled {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	durableCancelled := false
	if b.runs != nil && b.runs.Enabled() {
		active, err := b.runs.ActiveForThread(r.Context(), p.OrgID, sessionID)
		if err == nil {
			if err := b.cancelDurableRun(r.Context(), active, p.UserID); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					b.log.Warn("durable chat cancel failed",
						"org", p.OrgID,
						"thread", sessionID,
						"user", p.UserID,
						"run_id", active.ID,
						"error", err,
					)
				}
			} else {
				durableCancelled = true
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("active run lookup for live chat cancel",
				"org", p.OrgID,
				"thread", sessionID,
				"user", p.UserID,
				"error", err,
			)
		}
	}
	// Any org member may stop a runaway in-flight turn. We log the actor
	// above for auditability. Only fresh-run sandboxes are cleaned up from
	// this handler; follow-up runs reuse the conversation sandbox and must
	// remain available for the next message.
	if sandboxID != "" && cleanupOnCancel && !durableCancelled {
		cleanup := b.cleanupSandboxByID
		if b.cleanupSandboxByIDFn != nil {
			cleanup = b.cleanupSandboxByIDFn
		}
		go cleanup(sandboxID, "cancel requested")
	}
	w.WriteHeader(http.StatusAccepted)
}

// chatStreamHandler is the reattach endpoint. Hit by chat.html on
// page load: if a live run is in flight for this (org, session) the
// browser receives the full event history (replayed) followed by
// the live event stream until the run ends. If no run is active,
// returns 404 — the client falls back to /api/conversations to
// render the persisted snapshot.
func (b *Bot) chatStreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if sessionID == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	run := b.live.Get(p.OrgID, sessionID)
	var replay []liveEvent
	afterSeq := parseSSEAfterSeq(r.URL.Query().Get("after_seq"))
	lastSeq := afterSeq
	activeDurableRun := false
	if b.runs != nil && b.runs.Enabled() {
		latest, err := b.runs.LatestForThread(r.Context(), p.OrgID, sessionID)
		if err == nil && (!isTerminalRunState(latest.State) || run != nil || afterSeq > 0) {
			activeDurableRun = !isTerminalRunState(latest.State)
			if run == nil && !isTerminalRunState(latest.State) {
				run = b.recoverRunForReattach(r.Context(), latest)
			}
			events, err := b.runs.EventsAfter(r.Context(), latest.ID, afterSeq)
			if err != nil {
				b.log.Warn("list run events for stream", "org", p.OrgID, "thread", sessionID, "run", latest.ID, "error", err)
			} else {
				replay = make([]liveEvent, 0, len(events))
				for _, ev := range events {
					replay = append(replay, liveEvent{Event: ev.Event, Data: ev.Data, Seq: ev.Seq})
					lastSeq = ev.Seq
				}
				b.log.Info("chat stream replayed durable run events",
					"org", p.OrgID,
					"thread", sessionID,
					"run_id", latest.ID,
					"state", latest.State,
					"after_seq", afterSeq,
					"events", len(events),
					"live_attached", run != nil,
				)
			}
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("latest run lookup for stream", "org", p.OrgID, "thread", sessionID, "error", err)
		}
	}
	if run == nil && len(replay) == 0 {
		if activeDurableRun {
			b.log.Info("chat stream active durable run not attached yet",
				"org", p.OrgID, "thread", sessionID, "after_seq", afterSeq)
			w.Header().Set("Retry-After", "2")
			http.Error(w, "active run is reconnecting", http.StatusConflict)
			return
		}
		b.log.Info("chat stream reattach found no live or durable run", "org", p.OrgID, "thread", sessionID)
		http.Error(w, "no live run", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	for _, ev := range replay {
		if err := writeLiveEvent(w, ev); err != nil {
			return
		}
		flusher.Flush()
	}
	if run == nil {
		return
	}
	sub := run.SubscribeAfter(lastSeq)
	defer run.Unsubscribe(sub)
	b.streamLiveSubscription(w, flusher, r.Context(), sub)
}

func parseSSEAfterSeq(raw string) int64 {
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func writeLiveEvent(w http.ResponseWriter, ev liveEvent) error {
	if ev.Seq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", ev.Seq); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, ev.Data)
	return err
}

// streamLiveSubscription drains a liveSubscription to the SSE
// response. Sends the catch-up history first, then live events
// until the request context is cancelled or the run closes. Heart-
// beats every keepaliveLiveInterval to beat proxy idle timeouts.
func (b *Bot) streamLiveSubscription(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, sub *liveSubscription) {
	write := func(ev liveEvent) error {
		if err := writeLiveEvent(w, ev); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// Replay history. The subscription was created under the run's
	// mutex, so the history slice is a stable snapshot — no race
	// with concurrent Emits.
	for _, ev := range sub.history {
		if err := write(ev); err != nil {
			return
		}
	}

	keepalive := time.NewTicker(keepaliveLiveInterval)
	defer keepalive.Stop()
	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				return
			}
			if err := write(ev); err != nil {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}
