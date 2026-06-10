package linear

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// graphQLStub records the requests a test sends and plays back canned
// responses in order.
type graphQLStub struct {
	t         *testing.T
	responses []string
	requests  []map[string]any
}

func (s *graphQLStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			s.t.Fatalf("read request: %v", err)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			s.t.Fatalf("unmarshal request: %v", err)
		}
		s.requests = append(s.requests, req)
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok-123" {
			s.t.Errorf("Authorization = %q, want Bearer tok-123", auth)
		}
		resp := `{"data":{}}`
		if len(s.responses) > 0 {
			resp = s.responses[0]
			s.responses = s.responses[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}
}

func (s *graphQLStub) variables(i int) map[string]any {
	if i >= len(s.requests) {
		s.t.Fatalf("no request %d recorded (have %d)", i, len(s.requests))
	}
	vars, _ := s.requests[i]["variables"].(map[string]any)
	return vars
}

func newStubClient(t *testing.T, responses ...string) (*Client, *graphQLStub) {
	t.Helper()
	stub := &graphQLStub{t: t, responses: responses}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	return NewClientWithEndpoint("tok-123", srv.URL), stub
}

func TestIdentity(t *testing.T) {
	cli, _ := newStubClient(t,
		`{"data":{"viewer":{"id":"app-user"},"organization":{"id":"ws-1","name":"Acme"}}}`)
	id, err := cli.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if id.AppUserID != "app-user" || id.WorkspaceID != "ws-1" || id.WorkspaceName != "Acme" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestIdentityMissingFields(t *testing.T) {
	cli, _ := newStubClient(t, `{"data":{"viewer":{"id":""},"organization":{"id":""}}}`)
	if _, err := cli.Identity(context.Background()); err == nil {
		t.Fatal("want error on empty identity")
	}
}

func TestCreateActivity(t *testing.T) {
	cli, stub := newStubClient(t, `{"data":{"agentActivityCreate":{"success":true}}}`)
	err := cli.CreateActivity(context.Background(), "sess-1",
		ActivityContent{Type: "thought", Body: "working"}, true)
	if err != nil {
		t.Fatalf("CreateActivity: %v", err)
	}
	vars := stub.variables(0)
	input, _ := vars["input"].(map[string]any)
	if input["agentSessionId"] != "sess-1" {
		t.Fatalf("agentSessionId = %v", input["agentSessionId"])
	}
	if input["ephemeral"] != true {
		t.Fatalf("ephemeral = %v, want true", input["ephemeral"])
	}
	content, _ := input["content"].(map[string]any)
	if content["type"] != "thought" || content["body"] != "working" {
		t.Fatalf("content = %v", content)
	}
}

func TestCreateActivityNotSuccessful(t *testing.T) {
	cli, _ := newStubClient(t, `{"data":{"agentActivityCreate":{"success":false}}}`)
	if err := cli.CreateActivity(context.Background(), "sess-1", ActivityContent{Type: "thought"}, false); err == nil {
		t.Fatal("want error when success=false")
	}
}

func TestCreateActivityGraphQLError(t *testing.T) {
	cli, _ := newStubClient(t, `{"errors":[{"message":"boom"}]}`)
	err := cli.CreateActivity(context.Background(), "sess-1", ActivityContent{Type: "thought"}, false)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want graphql boom", err)
	}
}

func TestAddExternalURLs(t *testing.T) {
	cli, stub := newStubClient(t, `{"data":{"agentSessionUpdate":{"success":true}}}`)
	err := cli.AddExternalURLs(context.Background(), "sess-1",
		[]ExternalURL{{Label: "Hetchy run", URL: "https://app.hetchy.ai/?session=x"}})
	if err != nil {
		t.Fatalf("AddExternalURLs: %v", err)
	}
	vars := stub.variables(0)
	if vars["id"] != "sess-1" {
		t.Fatalf("id = %v", vars["id"])
	}
}

func TestAddExternalURLsEmptyIsNoop(t *testing.T) {
	cli, stub := newStubClient(t)
	if err := cli.AddExternalURLs(context.Background(), "sess-1", nil); err != nil {
		t.Fatalf("AddExternalURLs: %v", err)
	}
	if len(stub.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(stub.requests))
	}
}

func TestMoveIssueToStartedPicksLowestPosition(t *testing.T) {
	cli, stub := newStubClient(t,
		`{"data":{"issue":{"id":"iss-1","state":{"id":"s0","type":"unstarted"},"team":{"states":{"nodes":[{"id":"s2","position":2},{"id":"s1","position":1}]}}}}}`,
		`{"data":{"issueUpdate":{"success":true}}}`)
	if err := cli.MoveIssueToStarted(context.Background(), "iss-1"); err != nil {
		t.Fatalf("MoveIssueToStarted: %v", err)
	}
	if len(stub.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(stub.requests))
	}
	vars := stub.variables(1)
	input, _ := vars["input"].(map[string]any)
	if input["stateId"] != "s1" {
		t.Fatalf("stateId = %v, want s1 (lowest position)", input["stateId"])
	}
}

func TestMoveIssueToStartedNotSuccessful(t *testing.T) {
	cli, _ := newStubClient(t,
		`{"data":{"issue":{"id":"iss-1","state":{"id":"s0","type":"unstarted"},"team":{"states":{"nodes":[{"id":"s1","position":1}]}}}}}`,
		`{"data":{"issueUpdate":{"success":false}}}`)
	if err := cli.MoveIssueToStarted(context.Background(), "iss-1"); err == nil {
		t.Fatal("want error when issueUpdate success=false")
	}
}

func TestMoveIssueToStartedSkipsAlreadyStarted(t *testing.T) {
	cli, stub := newStubClient(t,
		`{"data":{"issue":{"id":"iss-1","state":{"id":"s9","type":"started"},"team":{"states":{"nodes":[{"id":"s1","position":1}]}}}}}`)
	if err := cli.MoveIssueToStarted(context.Background(), "iss-1"); err != nil {
		t.Fatalf("MoveIssueToStarted: %v", err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %d, want 1 (no mutation)", len(stub.requests))
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	secret := "whsec"
	body := []byte(`{"type":"AgentSessionEvent"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if !VerifyWebhookSignature(secret, body, sig) {
		t.Fatal("valid signature rejected")
	}
	if VerifyWebhookSignature(secret, body, "deadbeef") {
		t.Fatal("invalid signature accepted")
	}
	if VerifyWebhookSignature("", body, sig) {
		t.Fatal("empty secret accepted")
	}
	if VerifyWebhookSignature(secret, body, "") {
		t.Fatal("empty signature accepted")
	}
}

func TestAuthorizeURL(t *testing.T) {
	u := AuthorizeURL("cid", "https://app/cb", "state-1", []string{"read", "app:mentionable"})
	for _, want := range []string{"actor=app", "client_id=cid", "state=state-1", "response_type=code"} {
		if !strings.Contains(u, want) {
			t.Errorf("authorize url missing %q: %s", want, u)
		}
	}
}

func TestExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code-1" {
			t.Errorf("unexpected form: %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"tok-xyz"}`))
	}))
	defer srv.Close()
	tok, err := ExchangeCode(context.Background(), nil, srv.URL, "cid", "csec", "https://app/cb", "code-1")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tok != "tok-xyz" {
		t.Fatalf("token = %q", tok)
	}
}

func TestExchangeCodeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	if _, err := ExchangeCode(context.Background(), nil, srv.URL, "cid", "csec", "https://app/cb", "code-1"); err == nil {
		t.Fatal("want error on 400")
	}
}

func TestRevokeToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := RevokeToken(context.Background(), nil, srv.URL, "tok-1"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("auth header = %q", gotAuth)
	}
}

func TestParseAgentSessionEventAndPromptText(t *testing.T) {
	body := []byte(`{
		"type":"AgentSessionEvent","action":"created","organizationId":"ws-1",
		"webhookTimestamp": ` + timestampNowMillis() + `,
		"agentSession":{"id":"sess-1","issue":{"id":"iss-1","identifier":"ENG-42","title":"Fix login","description":"It breaks","url":"https://linear.app/acme/issue/ENG-42"}},
		"promptContext":"full formatted context"
	}`)
	ev, err := ParseAgentSessionEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Action != AgentSessionActionCreated || ev.AgentSession.ID != "sess-1" {
		t.Fatalf("event = %+v", ev)
	}
	if got := ev.PromptText(); got != "full formatted context" {
		t.Fatalf("PromptText = %q, want promptContext", got)
	}
	if !ev.TimestampFresh(time.Now(), WebhookTimestampTolerance) {
		t.Fatal("fresh timestamp rejected")
	}
}

func TestPromptTextFallbacks(t *testing.T) {
	prompted := AgentSessionEvent{
		AgentActivity: &AgentActivity{Content: ActivityContent{Type: "prompt", Body: "also fix signup"}},
	}
	if got := prompted.PromptText(); got != "also fix signup" {
		t.Fatalf("PromptText = %q", got)
	}
	issueOnly := AgentSessionEvent{
		AgentSession: AgentSession{Issue: &SessionIssue{Title: "Fix login", Description: "It breaks"}},
	}
	if got := issueOnly.PromptText(); got != "Fix login\n\nIt breaks" {
		t.Fatalf("PromptText = %q", got)
	}
	if got := (AgentSessionEvent{}).PromptText(); got != "" {
		t.Fatalf("PromptText = %q, want empty", got)
	}
}

func TestIsStopSignal(t *testing.T) {
	if (AgentSessionEvent{}).IsStopSignal() {
		t.Fatal("no activity should not be a stop signal")
	}
	topLevel := AgentSessionEvent{AgentActivity: &AgentActivity{Signal: SignalStop}}
	if !topLevel.IsStopSignal() {
		t.Fatal("top-level signal not detected")
	}
	inContent := AgentSessionEvent{AgentActivity: &AgentActivity{Content: ActivityContent{Signal: SignalStop}}}
	if !inContent.IsStopSignal() {
		t.Fatal("content-level signal not detected")
	}
	prompt := AgentSessionEvent{AgentActivity: &AgentActivity{Content: ActivityContent{Type: "prompt", Body: "more work"}}}
	if prompt.IsStopSignal() {
		t.Fatal("ordinary prompt treated as stop")
	}
}

func TestDirective(t *testing.T) {
	activity := AgentSessionEvent{
		AgentActivity: &AgentActivity{Content: ActivityContent{Body: "follow-up"}},
		AgentSession: AgentSession{Comment: &struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		}{Body: "original comment"}},
	}
	if got := activity.Directive(); got != "follow-up" {
		t.Fatalf("Directive = %q, want activity body to win", got)
	}
	commentOnly := AgentSessionEvent{
		AgentSession: AgentSession{Comment: &struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		}{Body: "original comment"}},
	}
	if got := commentOnly.Directive(); got != "original comment" {
		t.Fatalf("Directive = %q", got)
	}
	if got := (AgentSessionEvent{}).Directive(); got != "" {
		t.Fatalf("Directive = %q, want empty", got)
	}
}

func TestTimestampFresh(t *testing.T) {
	now := time.Now()
	stale := WebhookEnvelope{WebhookTimestamp: now.Add(-10 * time.Minute).UnixMilli()}
	if stale.TimestampFresh(now, WebhookTimestampTolerance) {
		t.Fatal("stale timestamp accepted")
	}
	zero := WebhookEnvelope{}
	if zero.TimestampFresh(now, WebhookTimestampTolerance) {
		t.Fatal("zero timestamp accepted")
	}
}

func timestampNowMillis() string {
	b, _ := json.Marshal(time.Now().UnixMilli())
	return string(b)
}
