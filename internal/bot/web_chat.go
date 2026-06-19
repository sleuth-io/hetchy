package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
)

type chatPostBody struct {
	Text           string  `json:"text"`
	Message        string  `json:"message,omitempty"`
	ID             string  `json:"id,omitempty"`
	ConversationID string  `json:"conversation_id,omitempty"`
	SessionID      string  `json:"session_id"`
	AgentSlug      *string `json:"agent_slug,omitempty"`
	Agent          *string `json:"agent,omitempty"`
	// Repository carries the composer repo-picker selection as
	// "owner/name". Nil falls back to the org's saved default repo
	// for clients that don't surface the field; an explicit empty
	// string means "No repository" and suppresses the default.
	Repository *string `json:"repository,omitempty"`
	Model      string  `json:"model,omitempty"`
	// Task option fields are pointers so missing (older clients,
	// non-web callers) is distinguishable from explicit false.
	// Missing request fields leave saved per-chat values alone;
	// missing saved keys default on in HandleRequest.
	Validate              *bool `json:"validate,omitempty"`
	ReviewCodeBeforePush  *bool `json:"review_code_before_push,omitempty"`
	ActionPRChecksForDone *bool `json:"action_pr_checks_for_done,omitempty"`
	AutoMerge             *bool `json:"auto_merge,omitempty"`
	Attachments           []convstore.Attachment
}

type chatPostJSONBody struct {
	Text                  string                   `json:"text"`
	Message               string                   `json:"message"`
	ID                    string                   `json:"id"`
	ConversationID        string                   `json:"conversation_id"`
	SessionID             string                   `json:"session_id"`
	AgentSlug             *string                  `json:"agent_slug,omitempty"`
	Agent                 *string                  `json:"agent,omitempty"`
	Repository            *string                  `json:"repository,omitempty"`
	Model                 string                   `json:"model,omitempty"`
	TaskOptions           map[string]bool          `json:"task_options,omitempty"`
	Validate              *bool                    `json:"validate,omitempty"`
	ReviewCodeBeforePush  *bool                    `json:"review_code_before_push,omitempty"`
	ActionPRChecksForDone *bool                    `json:"action_pr_checks_for_done,omitempty"`
	AutoMerge             *bool                    `json:"auto_merge,omitempty"`
	Attachments           []chatPostJSONAttachment `json:"attachments,omitempty"`
}

type chatPostJSONAttachment struct {
	ID          string `json:"id,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Data        string `json:"data,omitempty"`
	DataBase64  string `json:"data_base64,omitempty"`
	Source      string `json:"source,omitempty"`
}

func (b *Bot) startConversationTurn(parentCtx context.Context, w http.ResponseWriter, r *http.Request, pathConversationID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	oc, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil {
		http.Error(w, "org config not found — set it at /settings/org", http.StatusBadRequest)
		return
	}

	body, ok := parseChatPostBody(w, r)
	if !ok {
		return
	}
	text := firstNonEmpty(body.Message, body.Text)
	if strings.TrimSpace(text) == "" {
		if len(body.Attachments) == 0 {
			http.Error(w, "empty text", http.StatusBadRequest)
			return
		}
		text = "Use the attached file(s) as context."
	}
	sessionID := strings.TrimSpace(firstNonEmpty(pathConversationID, body.ConversationID, body.ID, body.SessionID))
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
	if body.AutoMerge != nil {
		optionPatch[chatTaskAutoMergeKey] = *body.AutoMerge
	}
	model, ok := parseClaudeModel(body.Model)
	if !ok {
		http.Error(w, "invalid model: pick one from the model dropdown", http.StatusBadRequest)
		return
	}
	if modelProvider(model) == modelProviderOpenAI {
		// Saved-credentials check happens up front so the user gets a
		// clean 400 instead of the runtime tripping over a missing
		// Codex credential mid-stream.
		if !hasOpenAICredentials(oc) {
			http.Error(w, "OpenAI Codex isn't configured yet — paste an API key or subscription token in /settings/org?tab=integrations.", http.StatusBadRequest)
			return
		}
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
	// the conversation events endpoint, not a fresh POST.
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
		requestedAgent := body.AgentSlug
		if requestedAgent == nil {
			requestedAgent = body.Agent
		}
		b.HandleRequest(runCtx, oc, text, requestID, sessionID, p.UserID, optionPatch, requestedAgent, body.Repository, model, emitter, body.Attachments...)
	}()

	sub := run.Subscribe()
	defer run.Unsubscribe(sub)
	b.streamLiveSubscription(w, flusher, r.Context(), sub)
}

func parseChatPostBody(w http.ResponseWriter, r *http.Request) (chatPostBody, bool) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.EqualFold(mediaType, "multipart/form-data") {
		return parseMultipartChatPostBody(w, r)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return chatPostBody{}, false
	}
	body, err := parseChatPostJSON(data)
	if err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return chatPostBody{}, false
	}
	return body, true
}

func parseChatPostJSON(data []byte) (chatPostBody, error) {
	var raw chatPostJSONBody
	if err := json.Unmarshal(data, &raw); err != nil {
		return chatPostBody{}, err
	}
	body := chatPostBody{
		Text:                  raw.Text,
		Message:               raw.Message,
		ID:                    raw.ID,
		ConversationID:        raw.ConversationID,
		SessionID:             raw.SessionID,
		AgentSlug:             raw.AgentSlug,
		Agent:                 raw.Agent,
		Repository:            raw.Repository,
		Model:                 raw.Model,
		Validate:              raw.Validate,
		ReviewCodeBeforePush:  raw.ReviewCodeBeforePush,
		ActionPRChecksForDone: raw.ActionPRChecksForDone,
		AutoMerge:             raw.AutoMerge,
	}
	if body.Validate == nil {
		body.Validate = optionalBoolFromMap(raw.TaskOptions, chatTaskValidateKey)
	}
	if body.ReviewCodeBeforePush == nil {
		body.ReviewCodeBeforePush = optionalBoolFromMap(raw.TaskOptions, chatTaskReviewCodeBeforePushKey)
	}
	if body.ActionPRChecksForDone == nil {
		body.ActionPRChecksForDone = optionalBoolFromMap(raw.TaskOptions, chatTaskActionPRChecksForDoneKey)
	}
	if body.AutoMerge == nil {
		body.AutoMerge = optionalBoolFromMap(raw.TaskOptions, chatTaskAutoMergeKey)
	}
	attachments, err := decodeJSONAttachments(raw.Attachments)
	if err != nil {
		return chatPostBody{}, err
	}
	body.Attachments = attachments
	return body, nil
}

func parseMultipartChatPostBody(w http.ResponseWriter, r *http.Request) (chatPostBody, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxPromptAttachmentTotal+(1*1024*1024)))
	if err := r.ParseMultipartForm(maxPromptAttachmentBytes); err != nil {
		http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
		return chatPostBody{}, false
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	values := map[string][]string{}
	if r.MultipartForm != nil {
		values = r.MultipartForm.Value
	}
	body := chatPostBody{
		Text:                  firstFormValue(values, "text"),
		Message:               firstFormValue(values, "message"),
		ID:                    firstFormValue(values, "id"),
		ConversationID:        firstFormValue(values, "conversation_id"),
		SessionID:             firstFormValue(values, "session_id"),
		AgentSlug:             optionalFormString(values, "agent_slug"),
		Agent:                 optionalFormString(values, "agent"),
		Repository:            optionalFormString(values, "repository"),
		Model:                 firstFormValue(values, "model"),
		Validate:              optionalFormBool(values, "validate"),
		ReviewCodeBeforePush:  optionalFormBool(values, "review_code_before_push"),
		ActionPRChecksForDone: optionalFormBool(values, "action_pr_checks_for_done"),
		AutoMerge:             optionalFormBool(values, "auto_merge"),
	}
	if payload := firstFormValue(values, "payload"); payload != "" {
		payloadBody, err := parseChatPostJSON([]byte(payload))
		if err != nil {
			http.Error(w, "invalid payload JSON: "+err.Error(), http.StatusBadRequest)
			return chatPostBody{}, false
		}
		body = payloadBody
	}
	if r.MultipartForm != nil {
		attachments, err := readMultipartAttachments(r.MultipartForm.File["attachments"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return chatPostBody{}, false
		}
		body.Attachments = attachments
	}
	return body, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func optionalBoolFromMap(values map[string]bool, key string) *bool {
	if values == nil {
		return nil
	}
	v, ok := values[key]
	if !ok {
		return nil
	}
	return &v
}

func decodeJSONAttachments(raw []chatPostJSONAttachment) ([]convstore.Attachment, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxPromptAttachments {
		return nil, fmt.Errorf("too many attachments: maximum is %d", maxPromptAttachments)
	}
	out := make([]convstore.Attachment, 0, len(raw))
	var total int64
	for _, in := range raw {
		encoded := strings.TrimSpace(firstNonEmpty(in.DataBase64, in.Data))
		if encoded == "" {
			return nil, fmt.Errorf("%s is missing data", firstNonEmpty(in.Filename, "attachment"))
		}
		data, err := decodeAttachmentBase64(encoded)
		if err != nil {
			return nil, fmt.Errorf("%s has invalid base64 data: %w", firstNonEmpty(in.Filename, "attachment"), err)
		}
		if len(data) > maxPromptAttachmentBytes {
			return nil, fmt.Errorf("%s is too large: maximum is %d MB", firstNonEmpty(in.Filename, "attachment"), maxPromptAttachmentBytes/(1024*1024))
		}
		total += int64(len(data))
		if total > maxPromptAttachmentTotal {
			return nil, fmt.Errorf("attachments are too large: maximum total is %d MB", maxPromptAttachmentTotal/(1024*1024))
		}
		name := strings.TrimSpace(in.Filename)
		if name == "" {
			name = "attachment"
		}
		contentType := detectAttachmentContentType(strings.TrimSpace(in.ContentType), data)
		id := strings.TrimSpace(in.ID)
		if id == "" {
			id = convstore.NewAttachmentID()
		}
		source := strings.TrimSpace(in.Source)
		if source == "" {
			source = "api"
		}
		out = append(out, convstore.Attachment{
			ID:          id,
			Filename:    name,
			ContentType: contentType,
			SizeBytes:   int64(len(data)),
			Data:        data,
			Source:      source,
		})
	}
	return out, nil
}

func decodeAttachmentBase64(s string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		data, err := enc.DecodeString(s)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func firstFormValue(values map[string][]string, key string) string {
	if vals := values[key]; len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func optionalFormString(values map[string][]string, key string) *string {
	vals, ok := values[key]
	if !ok || len(vals) == 0 {
		return nil
	}
	v := vals[0]
	return &v
}

func optionalFormBool(values map[string][]string, key string) *bool {
	vals, ok := values[key]
	if !ok || len(vals) == 0 {
		return nil
	}
	v := strings.EqualFold(vals[0], "true") || vals[0] == "1" || strings.EqualFold(vals[0], "on")
	return &v
}

func readMultipartAttachments(files []*multipart.FileHeader) ([]convstore.Attachment, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > maxPromptAttachments {
		return nil, fmt.Errorf("too many attachments: maximum is %d", maxPromptAttachments)
	}
	var total int64
	out := make([]convstore.Attachment, 0, len(files))
	for _, fh := range files {
		if fh.Size > maxPromptAttachmentBytes {
			return nil, fmt.Errorf("%s is too large: maximum is %d MB", fh.Filename, maxPromptAttachmentBytes/(1024*1024))
		}
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("open attachment %s: %w", fh.Filename, err)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxPromptAttachmentBytes+1))
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", fh.Filename, err)
		}
		if len(data) > maxPromptAttachmentBytes {
			return nil, fmt.Errorf("%s is too large: maximum is %d MB", fh.Filename, maxPromptAttachmentBytes/(1024*1024))
		}
		total += int64(len(data))
		if total > maxPromptAttachmentTotal {
			return nil, fmt.Errorf("attachments are too large: maximum total is %d MB", maxPromptAttachmentTotal/(1024*1024))
		}
		name := strings.TrimSpace(fh.Filename)
		if name == "" {
			name = "attachment"
		}
		contentType := strings.TrimSpace(fh.Header.Get("Content-Type"))
		contentType = detectAttachmentContentType(contentType, data)
		out = append(out, convstore.Attachment{
			ID:          convstore.NewAttachmentID(),
			Filename:    name,
			ContentType: contentType,
			SizeBytes:   int64(len(data)),
			Data:        data,
			Source:      "web",
		})
	}
	return out, nil
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

// chatStreamHandler is the reattach endpoint. If a live run is in
// flight for this (org, session), the browser receives the full event
// history followed by the live event stream until the run ends. If no
// run is active, returns 404 so the client can render the persisted
// snapshot from /api/v1/conversations.
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
