package bot

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/runstore"
	"github.com/hetchyhq/hetchy/internal/webui"
)

func TestChatTemplate_ComposerControls(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Chat, map[string]any{
		"Email":       "u@x",
		"DisplayName": "Test User",
		"GravatarURL": "https://example.com/avatar.png",
		"UserID":      "user_test",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`id="tools-btn"`,
		`id="agent-selector-btn"`,
		`id="agent-popover"`,
		`id="repo-selector-btn"`,
		`id="repo-popover"`,
		`id="repo-search"`,
		`id="repo-options"`,
		`class="tools-divider"`,
		`class="tools-checkmark"`,
		`id="validate-checkbox"`,
		`id="review-before-push-checkbox"`,
		`id="action-pr-checks-checkbox"`,
		`class="tools-help"`,
		`sub-agent to review`,
		`automated AI reviews`,
		`id="model-btn"`,
		`onclick="handleComposerAction()"`,
		`class="stop-icon"`,
		`src="/assets/chat_bootstrap.js`,
		`href="/assets/chat.css`,
		`src="/assets/chat_core.js`,
		`src="/assets/chat_stream.js`,
		`src="/assets/chat_init.js`,
		`data-current-user-id="user_test"`,
		`id="toast-stack"`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("chat template missing %q", w)
		}
	}
	if strings.Contains(body, `id="agent-btn"`) {
		t.Errorf("chat template should not render the old standalone agent button")
	}
	if strings.Contains(body, `src="/assets/chat.js`) {
		t.Errorf("chat template should not render the removed monolithic chat.js")
	}

	script := readChatScripts(t)
	for _, w := range []string{
		`is-checked`,
		`function stopRun()`,
		`/chat/cancel`,
		`setRunState(true)`,
		`streamTurnWithReconnect`,
		`after_seq`,
		`Connection lost. Retrying`,
		`r.status === 409`,
		`Run is still active. Reconnecting`,
		`waitingTitle: 'Reconnecting'`,
		`stopRequested`,
		`conversationHasServerState`,
		`renderPendingMetadata(displayText, attachmentsForTurn)`,
		`conversationAgentIsMutable()`,
		`setPayload('agent_slug', selectedAgentSlug)`,
		`document.body.dataset.currentUserId`,
		`taskOptionKeys`,
		`applyConversationTaskOptions(detail)`,
		`setPayload('review_code_before_push', taskOptions[taskOptionKeys.reviewBeforePush])`,
		`setPayload('action_pr_checks_for_done', taskOptions[taskOptionKeys.actionPRChecks])`,
		`agentStorageKey`,
		`localStorage.setItem(agentStorageKey`,
		`applyConversationAgent(detail)`,
		`applyConversationRepo(detail)`,
		`loadRepos`,
		`repoStorageKey`,
		`localStorage.setItem(repoStorageKey`,
		`setPayload('repository', selectedRepoSlug)`,
		`blk-awaiting-next`,
		`markBlockAwaitingNext(ref.el)`,
		`payload.meta.tag === 'sandbox_ready'`,
		`value: 'opus'`,
		`value: 'sonnet'`,
		`value: 'haiku'`,
		`model: selectedModel`,
		`applyConversationModel(detail)`,
		`setModelPickerLocked(true)`,
		`attachFilesBtn.addEventListener('mouseenter'`,
	} {
		if !strings.Contains(script, w) {
			t.Errorf("chat asset missing %q", w)
		}
	}
}

func TestParseMultipartChatPostBodyReadsAttachments(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	fields := map[string]string{
		"text":                      "use this log",
		"session_id":                "thread-1",
		"model":                     "sonnet",
		"validate":                  "false",
		"review_code_before_push":   "true",
		"action_pr_checks_for_done": "false",
		"agent_slug":                "bob",
		"repository":                "hetchyhq/hetchy",
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	part, err := writer.CreateFormFile("attachments", "logs.json")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := part.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/chat", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	body, ok := parseChatPostBody(rec, req)
	if !ok {
		t.Fatalf("parse failed with status=%d body=%q", rec.Code, rec.Body.String())
	}
	if body.Text != "use this log" || body.SessionID != "thread-1" || body.Model != "sonnet" {
		t.Fatalf("unexpected parsed body: %+v", body)
	}
	if body.AgentSlug == nil || *body.AgentSlug != "bob" {
		t.Fatalf("agent slug = %v, want bob", body.AgentSlug)
	}
	if body.Repository == nil || *body.Repository != "hetchyhq/hetchy" {
		t.Fatalf("repository = %v, want hetchyhq/hetchy", body.Repository)
	}
	if body.Validate == nil || *body.Validate {
		t.Fatalf("validate = %v, want false", body.Validate)
	}
	if body.ReviewCodeBeforePush == nil || !*body.ReviewCodeBeforePush {
		t.Fatalf("review = %v, want true", body.ReviewCodeBeforePush)
	}
	if body.ActionPRChecksForDone == nil || *body.ActionPRChecksForDone {
		t.Fatalf("checks = %v, want false", body.ActionPRChecksForDone)
	}
	if len(body.Attachments) != 1 {
		t.Fatalf("attachments len = %d, want 1", len(body.Attachments))
	}
	a := body.Attachments[0]
	if a.Filename != "logs.json" || string(a.Data) != `{"ok":true}` || a.Source != "web" {
		t.Fatalf("attachment = %+v data=%q", a, string(a.Data))
	}
	if a.ContentType == "" || a.SizeBytes != int64(len(a.Data)) {
		t.Fatalf("attachment metadata = %+v", a)
	}
}

func TestParseJSONChatPostBody(t *testing.T) {
	bodyJSON := []byte(`{
		"text":"ship it",
		"session_id":"thread-json",
		"model":"haiku",
		"validate":true,
		"review_code_before_push":false,
		"action_pr_checks_for_done":true,
		"agent_slug":"alice",
		"repository":"hetchyhq/hetchy"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	body, ok := parseChatPostBody(rec, req)
	if !ok {
		t.Fatalf("parse failed with status=%d body=%q", rec.Code, rec.Body.String())
	}
	if body.Text != "ship it" || body.SessionID != "thread-json" || body.Model != "haiku" {
		t.Fatalf("unexpected parsed body: %+v", body)
	}
	if body.AgentSlug == nil || *body.AgentSlug != "alice" {
		t.Fatalf("agent slug = %v, want alice", body.AgentSlug)
	}
	if body.Repository == nil || *body.Repository != "hetchyhq/hetchy" {
		t.Fatalf("repository = %v, want hetchyhq/hetchy", body.Repository)
	}
	if body.Validate == nil || !*body.Validate {
		t.Fatalf("validate = %v, want true", body.Validate)
	}
	if body.ReviewCodeBeforePush == nil || *body.ReviewCodeBeforePush {
		t.Fatalf("review = %v, want false", body.ReviewCodeBeforePush)
	}
	if body.ActionPRChecksForDone == nil || !*body.ActionPRChecksForDone {
		t.Fatalf("checks = %v, want true", body.ActionPRChecksForDone)
	}
	if len(body.Attachments) != 0 {
		t.Fatalf("attachments len = %d, want 0", len(body.Attachments))
	}
}

func TestParseChatPostBodyRejectsInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"text":`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if _, ok := parseChatPostBody(rec, req); ok {
		t.Fatal("parseChatPostBody returned ok for invalid JSON")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Fatalf("body = %q, want invalid JSON error", rec.Body.String())
	}
}

func TestChatTemplate_LoadsSplitScriptsInOrder(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Chat, map[string]any{
		"Email":       "u@x",
		"DisplayName": "Test User",
		"GravatarURL": "https://example.com/avatar.png",
		"UserID":      "user_test",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	last := -1
	for _, name := range chatScriptAssets {
		needle := `src="/assets/` + name
		idx := strings.Index(body, needle)
		if idx < 0 {
			t.Fatalf("chat template missing %s", name)
		}
		if idx <= last {
			t.Fatalf("chat script %s loaded out of order", name)
		}
		last = idx
	}
	if strings.Contains(body, `src="/assets/chat.js`) {
		t.Fatal("chat template still references removed chat.js")
	}
}

func TestParseSSEAfterSeq(t *testing.T) {
	cases := map[string]int64{
		"":     0,
		" 42 ": 42,
		"-1":   0,
		"abc":  0,
		"1.5":  0,
	}
	for in, want := range cases {
		if got := parseSSEAfterSeq(in); got != want {
			t.Errorf("parseSSEAfterSeq(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestChatStreamHandlerReplaysClosedLiveRun(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.chatStreamHandler)))

	run, ok := b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-1")
	if !ok {
		t.Fatal("expected live run registration")
	}
	run.Emit(liveEvent{Event: "notify", Data: []byte(`{"text":"hello"}`), Seq: 3})
	run.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat/stream?session=thread-1&after_seq=2", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"id: 3\n",
		"event: notify\n",
		`data: {"text":"hello"}`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("stream response missing %q: %q", want, rec.Body.String())
		}
	}
}

func TestChatStreamHandlerReplaysDurableRunEvents(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	ev := runEventForTest(t, "block_start", sseEvent{ID: "p2", Kind: blocks.KindResult, Title: "Done"})
	ev.Seq = 2
	b.runs = &fakeRunStore{
		enabled:   true,
		latestRun: runstore.Run{ID: "run_1", OrgID: "org_test", ThreadID: "thread-1", State: runstore.StateSucceeded},
		events:    []runstore.Event{ev},
	}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.chatStreamHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat/stream?session=thread-1&after_seq=1", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"id: 2\n",
		"event: block_start\n",
		`"title":"Done"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("stream response missing %q: %q", want, rec.Body.String())
		}
	}
}

func TestChatStreamHandlerRejectsBadRequests(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.chatStreamHandler)))

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodPost, path: "/chat/stream?session=thread-1", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/chat/stream", want: http.StatusBadRequest},
		{method: http.MethodGet, path: "/chat/stream?session=missing", want: http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s %s status = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestWriteLiveEventIncludesSequenceID(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeLiveEvent(rec, liveEvent{
		Event: "block_start",
		Data:  []byte(`{"id":"blk_1"}`),
		Seq:   42,
	})
	if err != nil {
		t.Fatalf("writeLiveEvent: %v", err)
	}
	want := "id: 42\nevent: block_start\ndata: {\"id\":\"blk_1\"}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestChatCancelHandlerCancelsRunAndSchedulesCleanup(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassOrg:   "org_test",
		BypassEmail: "test@hetchy.local",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{
		log:  discardLogger(),
		auth: a,
		live: newLiveRegistry(),
	}
	handler := a.Middleware(a.RequireOrg(http.HandlerFunc(b.chatCancelHandler)))

	run, ok := b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-1")
	if !ok {
		t.Fatal("expected live run registration")
	}
	run.SetSandboxID("sandbox-1", true)

	type cleanupCall struct {
		sandboxID string
		reason    string
	}
	cleanupCh := make(chan cleanupCall, 1)
	b.cleanupSandboxByIDFn = func(sandboxID, reason string) {
		cleanupCh <- cleanupCall{sandboxID: sandboxID, reason: reason}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"thread-1"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !run.Cancelled() {
		t.Fatal("run should be cancelled")
	}
	select {
	case got := <-cleanupCh:
		if got.sandboxID != "sandbox-1" || got.reason != "cancel requested" {
			t.Fatalf("cleanup = %+v, want sandbox-1/cancel requested", got)
		}
	case <-stopAfter(t):
		t.Fatal("cleanup was not scheduled")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"thread-1"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second cancel status = %d, want %d; body=%q", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	select {
	case got := <-cleanupCh:
		t.Fatalf("second cancel should not schedule cleanup, got %+v", got)
	case <-time.After(20 * time.Millisecond):
	}

	followUpRun, ok := b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-2")
	if !ok {
		t.Fatal("expected follow-up live run registration")
	}
	followUpRun.SetSandboxID("sandbox-2", false)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"thread-2"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("follow-up status = %d, want %d; body=%q", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !followUpRun.Cancelled() {
		t.Fatal("follow-up run should be cancelled")
	}
	select {
	case got := <-cleanupCh:
		t.Fatalf("follow-up cancel should not cleanup sandbox, got %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestChatCancelHandlerCancelsDurableRunWithoutLiveRun(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	b.runs = &fakeRunStore{
		enabled:   true,
		activeRun: runstore.Run{ID: "run_1", OrgID: "org_test", ThreadID: "thread-1", RunKind: "followup", State: runstore.StateRunning, UserRequest: "stop me"},
		cancelRun: runstore.Run{ID: "run_1", OrgID: "org_test", ThreadID: "thread-1", RunKind: "followup", State: runstore.StateRunning, UserRequest: "stop me"},
	}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.chatCancelHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"thread-1"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	store := b.runs.(*fakeRunStore)
	if len(store.cancelPending) != 3 {
		t.Fatalf("cancel pending events = %d, want 3", len(store.cancelPending))
	}
	if store.cancelRun.State != runstore.StateCancelled || store.cancelRun.LastError != "cancel requested" {
		t.Fatalf("cancelled run = %+v", store.cancelRun)
	}
}

func TestChatCancelHandlerRejectsMissingRunAndWrongMethod(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassOrg:   "org_test",
		BypassEmail: "test@hetchy.local",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{
		log:  discardLogger(),
		auth: a,
		live: newLiveRegistry(),
	}
	handler := a.Middleware(a.RequireOrg(http.HandlerFunc(b.chatCancelHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"missing"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing run status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/chat/cancel", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
