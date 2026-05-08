package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(Config{
		APIKey:         "sk_test_x",
		ClientID:       "client_test_x",
		CookiePassword: strings.Repeat("p", 32),
		RedirectURI:    "http://dev.hetchy.ai:8080/callback",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// AuthKit's invitation flow lands users on /callback with only an
// `invitation_token` (no `code`). Returning "missing code" there breaks
// account activation -- see hetchyhq/hetchy#sf-dido592qbsq0. Instead the
// handler should kick off a full OAuth round-trip carrying that token, so
// AuthKit can issue a real `code` for us to exchange.
func TestCallbackHandler_InvitationTokenStartsOAuth(t *testing.T) {
	s := newTestService(t)

	req := httptest.NewRequest(http.MethodGet, "/callback?invitation_token=inv_tok_abc123", nil)
	rr := httptest.NewRecorder()
	s.CallbackHandler(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusFound, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("Location header is empty")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location %q: %v", loc, err)
	}
	if got := u.Query().Get("invitation_token"); got != "inv_tok_abc123" {
		t.Errorf("redirect missing invitation_token; got %q in %s", got, loc)
	}
	if got := u.Query().Get("redirect_uri"); got != "http://dev.hetchy.ai:8080/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if got := u.Query().Get("client_id"); got != "client_test_x" {
		t.Errorf("client_id = %q", got)
	}
}

// When both `code` and `invitation_token` are present we want to exchange
// the code for a session, not bounce back to AuthKit with the invitation
// token. The conditional structure today gives `code` priority — pinning
// that with a test so a future refactor can't silently flip it.
func TestCallbackHandler_CodeTakesPriorityOverInvitationToken(t *testing.T) {
	s := newTestService(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"stub"}`))
	}))
	t.Cleanup(stub.Close)
	s.client = workos.NewClient("sk_test_x", workos.WithClientID("client_test_x"), workos.WithBaseURL(stub.URL))

	req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&invitation_token=xyz", nil)
	rr := httptest.NewRecorder()
	s.CallbackHandler(rr, req)

	if rr.Code == http.StatusFound {
		t.Fatalf("expected code-exchange path, got 302 to %q (invitation_token branch ran)", rr.Header().Get("Location"))
	}
}

func TestCallbackHandler_MissingCodeAndInvitationToken(t *testing.T) {
	s := newTestService(t)

	req := httptest.NewRequest(http.MethodGet, "/callback", nil)
	rr := httptest.NewRecorder()
	s.CallbackHandler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "missing code") {
		t.Errorf("body = %q", rr.Body.String())
	}
}
