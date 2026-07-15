package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/slack-go/slack"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

// slackCallbackRequest builds a callback request whose signed state and
// CSRF cookie are bound to the same nonce, so the handler proceeds past
// state/CSRF validation into the token-exchange completion path.
func slackCallbackRequest(t *testing.T, b *Bot, orgID string) *http.Request {
	t.Helper()
	const nonce = "bound-nonce"
	state, err := b.signSlackInstallState(slackInstallState{
		OrgID:  orgID,
		UserID: "user_installer",
		Exp:    time.Now().Add(time.Minute).Unix(),
		Nonce:  nonce,
	})
	if err != nil {
		t.Fatalf("signSlackInstallState: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/slack/oauth/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: slackInstallCSRFCookie, Value: nonce})
	return req
}

func okSlackOAuthResponse() *slack.OAuthV2Response {
	resp := &slack.OAuthV2Response{
		AccessToken: "xoxb-installed",
		BotUserID:   "U_BOT",
		AppID:       "A123",
	}
	resp.Ok = true
	resp.Team.ID = "T_WS"
	resp.Team.Name = "Acme"
	return resp
}

func TestSlackOAuthCallback_CompletesInstall(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	store := &fakeOrgStore{getConfig: orgcfg.Config{
		OrgID:            "org_test",
		SlackSocketToken: "xapp-stale",
	}}
	b.orgs = store
	b.slack = newSlackManager(discardLogger(), store, nil)
	b.slackOAuthExchangeFn = func(_ context.Context, code string) (*slack.OAuthV2Response, error) {
		if code != "auth-code" {
			t.Fatalf("exchange code = %q, want auth-code", code)
		}
		return okSlackOAuthResponse(), nil
	}

	rec := httptest.NewRecorder()
	b.slackOAuthCallbackHandler(rec, slackCallbackRequest(t, b, "org_test"))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body=%q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "saved=slack_installed") {
		t.Fatalf("redirect = %q, want saved=slack_installed", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.SlackBotToken != "xoxb-installed" || got.SlackTeamID != "T_WS" {
		t.Fatalf("upserted config = %+v, want bot token + team id set", got)
	}
	// HTTP installs must clear any stale socket token so the manager
	// doesn't keep a doomed socket connection alive.
	if got.SlackSocketToken != "" {
		t.Fatalf("SlackSocketToken = %q, want cleared", got.SlackSocketToken)
	}
}

func TestSlackOAuthCallback_ExchangeErrorReturnsBadGateway(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	b.orgs = &fakeOrgStore{}
	b.slackOAuthExchangeFn = func(context.Context, string) (*slack.OAuthV2Response, error) {
		return nil, errors.New("transport blew up")
	}

	rec := httptest.NewRecorder()
	b.slackOAuthCallbackHandler(rec, slackCallbackRequest(t, b, "org_test"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	// The raw SDK error must not leak to the browser.
	if strings.Contains(rec.Body.String(), "transport blew up") {
		t.Fatalf("body leaked internal error: %q", rec.Body.String())
	}
}

func TestSlackOAuthCallback_NotOkReturnsBadGateway(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	b.orgs = &fakeOrgStore{}
	b.slackOAuthExchangeFn = func(context.Context, string) (*slack.OAuthV2Response, error) {
		resp := &slack.OAuthV2Response{}
		resp.Ok = false
		resp.Error = "invalid_code"
		return resp, nil
	}

	rec := httptest.NewRecorder()
	b.slackOAuthCallbackHandler(rec, slackCallbackRequest(t, b, "org_test"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestSlackOAuthCallback_LoadOrgErrorReturns500(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	b.orgs = &fakeOrgStore{getErr: errors.New("db down")}
	b.slackOAuthExchangeFn = func(context.Context, string) (*slack.OAuthV2Response, error) {
		return okSlackOAuthResponse(), nil
	}

	rec := httptest.NewRecorder()
	b.slackOAuthCallbackHandler(rec, slackCallbackRequest(t, b, "org_test"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestSlackOAuthCallback_MissingOrgConfigIsTreatedAsFresh(t *testing.T) {
	// ErrNotFound is not fatal: the org simply has no prior config, so the
	// install proceeds and creates one.
	b := slackOAuthBot(t, "admin")
	store := &fakeOrgStore{getErr: orgcfg.ErrNotFound}
	b.orgs = store
	b.slack = newSlackManager(discardLogger(), store, nil)
	b.slackOAuthExchangeFn = func(context.Context, string) (*slack.OAuthV2Response, error) {
		return okSlackOAuthResponse(), nil
	}

	rec := httptest.NewRecorder()
	b.slackOAuthCallbackHandler(rec, slackCallbackRequest(t, b, "org_test"))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body=%q)", rec.Code, rec.Body.String())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 1 || store.upserts[0].OrgID != "org_test" {
		t.Fatalf("upserts = %+v, want one row for org_test", store.upserts)
	}
}

func TestSlackOAuthCallback_WorkspaceConflictRedirects(t *testing.T) {
	// A partial unique index violation (another org already owns this
	// workspace) surfaces as a friendly conflict redirect, not a 500.
	b := slackOAuthBot(t, "admin")
	b.orgs = &fakeOrgStore{upsertErr: &pgconn.PgError{Code: "23505"}}
	b.slack = newSlackManager(discardLogger(), &fakeOrgStore{}, nil)
	b.slackOAuthExchangeFn = func(context.Context, string) (*slack.OAuthV2Response, error) {
		return okSlackOAuthResponse(), nil
	}

	rec := httptest.NewRecorder()
	b.slackOAuthCallbackHandler(rec, slackCallbackRequest(t, b, "org_test"))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "saved=slack_install_conflict") {
		t.Fatalf("redirect = %q, want saved=slack_install_conflict", got)
	}
}

func TestSlackOAuthCallback_SaveErrorReturns500(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	b.orgs = &fakeOrgStore{upsertErr: errors.New("write failed")}
	b.slackOAuthExchangeFn = func(context.Context, string) (*slack.OAuthV2Response, error) {
		return okSlackOAuthResponse(), nil
	}

	rec := httptest.NewRecorder()
	b.slackOAuthCallbackHandler(rec, slackCallbackRequest(t, b, "org_test"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
