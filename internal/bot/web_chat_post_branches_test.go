package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

// principalRequest builds a POST request whose context already carries an
// org-scoped principal, so startConversationTurn can be exercised directly
// without threading it through the auth middleware.
func principalRequest(method, target, contentType, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user_test",
		Email:  "test@hetchy.local",
		OrgID:  "org_test",
		Role:   "admin",
	})
	return req.WithContext(ctx)
}

// nonFlushResponseWriter is an http.ResponseWriter that deliberately does
// NOT implement http.Flusher, so startConversationTurn takes its
// "streaming unsupported" branch.
type nonFlushResponseWriter struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (n *nonFlushResponseWriter) Header() http.Header {
	if n.header == nil {
		n.header = http.Header{}
	}
	return n.header
}

func (n *nonFlushResponseWriter) Write(b []byte) (int, error) { return n.buf.Write(b) }

func (n *nonFlushResponseWriter) WriteHeader(code int) { n.code = code }

func TestStartConversationTurn_RejectsNonPost(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	rec := httptest.NewRecorder()
	req := principalRequest(http.MethodGet, "/api/v1/conversations", "application/json", "")
	b.startConversationTurn(req.Context(), rec, req, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Fatalf("Allow header = %q, want POST", got)
	}
}

func TestStartConversationTurn_OrgConfigError(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getErr: errors.New("no such org")}
	rec := httptest.NewRecorder()
	req := principalRequest(http.MethodPost, "/api/v1/conversations", "application/json",
		`{"text":"hi","session_id":"t1"}`)
	b.startConversationTurn(req.Context(), rec, req, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "org config not found") {
		t.Fatalf("body = %q, want org-config error", rec.Body.String())
	}
}

func TestStartConversationTurn_EmptyTextNoAttachments(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	rec := httptest.NewRecorder()
	req := principalRequest(http.MethodPost, "/api/v1/conversations", "application/json",
		`{"text":"   ","session_id":"t1"}`)
	b.startConversationTurn(req.Context(), rec, req, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "empty text") {
		t.Fatalf("body = %q, want empty-text error", rec.Body.String())
	}
}

// TestStartConversationTurn_OptionPatchesThenInvalidModel drives the
// task-option patch branches (validate/review/checks/auto_merge) and then
// bails on an unknown model, keeping the assertion deterministic without
// reaching the streaming goroutine.
func TestStartConversationTurn_OptionPatchesThenInvalidModel(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	rec := httptest.NewRecorder()
	req := principalRequest(http.MethodPost, "/api/v1/conversations", "application/json",
		`{"text":"hi","session_id":"t1","model":"bogus","validate":true,`+
			`"review_code_before_push":false,"action_pr_checks_for_done":true,"auto_merge":false}`)
	b.startConversationTurn(req.Context(), rec, req, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid model") {
		t.Fatalf("body = %q, want invalid-model error", rec.Body.String())
	}
}

func TestStartConversationTurn_StreamingUnsupported(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	w := &nonFlushResponseWriter{}
	req := principalRequest(http.MethodPost, "/api/v1/conversations", "application/json",
		`{"text":"hi","session_id":"t1"}`)
	b.startConversationTurn(req.Context(), w, req, "")
	if w.code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.code)
	}
	if !strings.Contains(w.buf.String(), "streaming unsupported") {
		t.Fatalf("body = %q, want streaming-unsupported error", w.buf.String())
	}
}

func TestStartConversationTurn_ActiveDurableRunConflict(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	b.runs = &fakeRunStore{enabled: true} // ActiveForThread returns a zero run, nil error
	rec := httptest.NewRecorder()
	req := principalRequest(http.MethodPost, "/api/v1/conversations", "application/json",
		`{"text":"hi","session_id":"t1"}`)
	b.startConversationTurn(req.Context(), rec, req, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already has a turn in flight") {
		t.Fatalf("body = %q, want in-flight error", rec.Body.String())
	}
}

func TestStartConversationTurn_ActiveRunLookupError(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	b.runs = &fakeRunStore{enabled: true, activeErr: errors.New("db down")}
	rec := httptest.NewRecorder()
	req := principalRequest(http.MethodPost, "/api/v1/conversations", "application/json",
		`{"text":"hi","session_id":"t1"}`)
	b.startConversationTurn(req.Context(), rec, req, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "could not check active chat run") {
		t.Fatalf("body = %q, want active-run lookup error", rec.Body.String())
	}
}

// TestStartConversationTurn_RegisterConflict pre-claims the in-flight slot
// so RegisterIfAbsent loses the race and the handler returns 409 before
// spawning the streaming goroutine. Durable runs are disabled (b.runs nil)
// and ErrNoRows is used to confirm the "no active run" fast path stays open.
func TestStartConversationTurn_RegisterConflict(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	b.runs = &fakeRunStore{enabled: true, activeErr: pgx.ErrNoRows}
	// Occupy the slot for org_test/thread-dup so the handler's own
	// RegisterIfAbsent call fails to register.
	held, registered := b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-dup")
	if !registered {
		t.Fatalf("precondition: expected to claim the live slot")
	}
	defer b.live.Done("org_test", "thread-dup", held)

	rec := httptest.NewRecorder()
	req := principalRequest(http.MethodPost, "/api/v1/conversations", "application/json",
		`{"text":"hi","session_id":"thread-dup"}`)
	b.startConversationTurn(req.Context(), rec, req, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already has a turn in flight") {
		t.Fatalf("body = %q, want in-flight error", rec.Body.String())
	}
}

// --- parseMultipartChatPostBody error paths ---

func TestParseMultipartChatPostBody_InvalidForm(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("not a real multipart body"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xxx")
	if _, ok := parseMultipartChatPostBody(rec, req); ok {
		t.Fatal("expected parse failure for malformed multipart body")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid multipart form") {
		t.Fatalf("body = %q, want invalid-multipart error", rec.Body.String())
	}
}

func TestParseMultipartChatPostBody_PayloadOverridesFields(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	// The "text" field should be ignored once a valid "payload" JSON is present.
	_ = writer.WriteField("text", "ignored")
	_ = writer.WriteField("payload", `{"text":"from payload","session_id":"p1"}`)
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	body, ok := parseMultipartChatPostBody(rec, req)
	if !ok {
		t.Fatalf("parse failed: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if body.Text != "from payload" || body.SessionID != "p1" {
		t.Fatalf("payload did not override fields: %+v", body)
	}
}

func TestParseMultipartChatPostBody_InvalidPayloadJSON(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("payload", `{not valid json`)
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if _, ok := parseMultipartChatPostBody(rec, req); ok {
		t.Fatal("expected failure for invalid payload JSON")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid payload JSON") {
		t.Fatalf("body = %q, want invalid-payload error", rec.Body.String())
	}
}

func TestParseMultipartChatPostBody_TooManyAttachments(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for range maxPromptAttachments + 1 {
		part, err := writer.CreateFormFile("attachments", "f.txt")
		if err != nil {
			t.Fatalf("create file: %v", err)
		}
		_, _ = part.Write([]byte("x"))
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if _, ok := parseMultipartChatPostBody(rec, req); ok {
		t.Fatal("expected failure for too many attachments")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too many attachments") {
		t.Fatalf("body = %q, want too-many-attachments error", rec.Body.String())
	}
}

// --- readMultipartAttachments happy path with an unnamed file ---

func TestReadMultipartAttachments_DefaultsBlankFilename(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	// Craft a file part whose filename is whitespace-only so the
	// "attachment" default-name branch is exercised. An outright empty
	// filename makes the multipart reader treat the part as a form value
	// rather than a file, so we use spaces that TrimSpace collapses.
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="attachments"; filename="   "`}
	h["Content-Type"] = []string{"text/plain"}
	part, err := writer.CreatePart(h)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/chat", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	out, err := readMultipartAttachments(req.MultipartForm.File["attachments"])
	if err != nil {
		t.Fatalf("readMultipartAttachments: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("attachments len = %d, want 1", len(out))
	}
	if out[0].Filename != "attachment" {
		t.Fatalf("filename = %q, want defaulted 'attachment'", out[0].Filename)
	}
	if string(out[0].Data) != "hello" || out[0].Source != "web" {
		t.Fatalf("attachment = %+v", out[0])
	}
}

func TestReadMultipartAttachments_Empty(t *testing.T) {
	out, err := readMultipartAttachments(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("out = %v, want nil", out)
	}
}

// --- decodeJSONAttachments branches ---

func TestDecodeJSONAttachments_Empty(t *testing.T) {
	out, err := decodeJSONAttachments(nil)
	if err != nil || out != nil {
		t.Fatalf("decodeJSONAttachments(nil) = (%v, %v), want (nil, nil)", out, err)
	}
}

func TestDecodeJSONAttachments_TooMany(t *testing.T) {
	raw := make([]chatPostJSONAttachment, maxPromptAttachments+1)
	for i := range raw {
		raw[i] = chatPostJSONAttachment{Filename: "f.txt", Data: base64.StdEncoding.EncodeToString([]byte("x"))}
	}
	if _, err := decodeJSONAttachments(raw); err == nil ||
		!strings.Contains(err.Error(), "too many attachments") {
		t.Fatalf("err = %v, want too-many-attachments", err)
	}
}

func TestDecodeJSONAttachments_MissingData(t *testing.T) {
	_, err := decodeJSONAttachments([]chatPostJSONAttachment{{Filename: "report.txt"}})
	if err == nil || !strings.Contains(err.Error(), "is missing data") {
		t.Fatalf("err = %v, want missing-data", err)
	}
}

func TestDecodeJSONAttachments_InvalidBase64(t *testing.T) {
	_, err := decodeJSONAttachments([]chatPostJSONAttachment{{Filename: "report.txt", Data: "%%%not-base64%%%"}})
	if err == nil || !strings.Contains(err.Error(), "invalid base64") {
		t.Fatalf("err = %v, want invalid-base64", err)
	}
}

func TestDecodeJSONAttachments_DefaultsNameAndSource(t *testing.T) {
	out, err := decodeJSONAttachments([]chatPostJSONAttachment{{
		DataBase64: base64.StdEncoding.EncodeToString([]byte("payload")),
	}})
	if err != nil {
		t.Fatalf("decodeJSONAttachments: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Filename != "attachment" {
		t.Fatalf("filename = %q, want defaulted 'attachment'", out[0].Filename)
	}
	if out[0].Source != "api" {
		t.Fatalf("source = %q, want defaulted 'api'", out[0].Source)
	}
	if string(out[0].Data) != "payload" {
		t.Fatalf("data = %q, want 'payload'", out[0].Data)
	}
	if out[0].ID == "" {
		t.Fatal("expected a generated attachment ID")
	}
}

func TestDecodeJSONAttachments_HonorsProvidedMetadata(t *testing.T) {
	out, err := decodeJSONAttachments([]chatPostJSONAttachment{{
		ID:          "att-123",
		Filename:    "notes.txt",
		ContentType: "text/plain",
		Source:      "slack",
		Data:        base64.StdEncoding.EncodeToString([]byte("hi")),
	}})
	if err != nil {
		t.Fatalf("decodeJSONAttachments: %v", err)
	}
	got := out[0]
	if got.ID != "att-123" || got.Filename != "notes.txt" || got.Source != "slack" {
		t.Fatalf("attachment metadata not preserved: %+v", got)
	}
}

// --- decodeAttachmentBase64 branches ---

func TestDecodeAttachmentBase64(t *testing.T) {
	// URL-safe (raw) encoding should still decode via the fallback list.
	raw := base64.RawURLEncoding.EncodeToString([]byte{0xfb, 0xff, 0xfe})
	data, err := decodeAttachmentBase64(raw)
	if err != nil {
		t.Fatalf("decode url-safe: %v", err)
	}
	if !bytes.Equal(data, []byte{0xfb, 0xff, 0xfe}) {
		t.Fatalf("decoded = %v, want fb ff fe", data)
	}
	if _, err := decodeAttachmentBase64("this is not base64 at all !!!"); err == nil {
		t.Fatal("expected error for undecodable input")
	}
}
