package bot

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestSlackDisconnectHandler_RejectsNonPost(t *testing.T) {
	b := &Bot{log: discardLogger(), orgs: &fakeOrgStore{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/slack/disconnect", nil)

	b.slackDisconnectHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestSlackDisconnectHandler_RejectsCrossOrigin(t *testing.T) {
	b := &Bot{log: discardLogger(), orgs: &fakeOrgStore{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/settings/org/slack/disconnect", nil)
	req.Host = "example.com"
	req.Header.Set("Origin", "http://evil.example.net")

	b.slackDisconnectHandler(rec, req)

	// Both this branch and the admin branch return 403; assert on the body
	// so a regression that reached 403 via the wrong path is still caught.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "does not match") {
		t.Fatalf("body = %q, want origin-mismatch message", rec.Body.String())
	}
}

func TestSlackDisconnectHandler_RequiresAdmin(t *testing.T) {
	b := &Bot{log: discardLogger(), orgs: &fakeOrgStore{}}
	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/slack/disconnect", "")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user_member",
		OrgID:  "org_test",
		Role:   "member",
	}))

	b.slackDisconnectHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "admin role required") {
		t.Fatalf("body = %q, want admin-required message", rec.Body.String())
	}
}

func TestSlackDisconnectHandler_LoadOrgError(t *testing.T) {
	b := &Bot{log: discardLogger(), orgs: &fakeOrgStore{getErr: errors.New("boom")}}
	rec := httptest.NewRecorder()
	req := adminDisconnectRequest()

	b.slackDisconnectHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestSlackDisconnectHandler_NothingToDisconnectIsIdempotent(t *testing.T) {
	// ErrNotFound (or an org with no Slack fields) is treated as an
	// already-disconnected no-op that redirects with a friendly banner.
	store := &fakeOrgStore{getErr: orgcfg.ErrNotFound}
	b := &Bot{log: discardLogger(), orgs: store}
	b.slack = newSlackManager(discardLogger(), store, nil)
	rec := httptest.NewRecorder()
	req := adminDisconnectRequest()

	b.slackDisconnectHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "saved=slack_already_disconnected") {
		t.Fatalf("redirect = %q, want saved=slack_already_disconnected", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0 (nothing to wipe)", len(store.upserts))
	}
}

func TestSlackDisconnectHandler_WipesLocalCredentials(t *testing.T) {
	// An org connected only via socket token (no bot token) skips the
	// network auth.revoke path but still wipes its local credentials.
	//
	// clearInstall spawns a detached restartOrgDetached goroutine, but it is
	// inert here: the manager's Run was never called, so runCtx is nil and
	// RestartOrg returns before touching the store — no race with the upsert
	// assertions below (confirmed under -race).
	store := &fakeOrgStore{getConfig: orgcfg.Config{
		OrgID:            "org_test",
		SlackSocketToken: "xapp-1",
		SlackTeamID:      "T123",
	}}
	b := &Bot{log: discardLogger(), orgs: store}
	b.slack = newSlackManager(discardLogger(), store, nil)
	rec := httptest.NewRecorder()
	req := adminDisconnectRequest()

	b.slackDisconnectHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "saved=slack_disconnected") {
		t.Fatalf("redirect = %q, want saved=slack_disconnected", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.SlackBotToken != "" || got.SlackSocketToken != "" || got.SlackTeamID != "" {
		t.Fatalf("upserted config = %+v, want slack fields cleared", got)
	}
}

func adminDisconnectRequest() *http.Request {
	req := settingsFormRequest(http.MethodPost, "/settings/org/slack/disconnect", "")
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user_admin",
		OrgID:  "org_test",
		Role:   "admin",
	}))
}
