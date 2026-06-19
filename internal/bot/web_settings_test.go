package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/bootstrap"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/secrets"
	"github.com/sleuth-io/hetchy/internal/sxsync"
)

func TestGithubInstallationManageURL(t *testing.T) {
	cases := []struct {
		accountType string
		account     string
		id          int64
		want        string
	}{
		{"Organization", "sleuth-io", 42, "https://github.com/organizations/sleuth-io/settings/installations/42"},
		{"User", "dylan", 99, "https://github.com/settings/installations/99"},
	}
	for _, tc := range cases {
		got := githubInstallationManageURL(tc.accountType, tc.account, tc.id)
		if got != tc.want {
			t.Fatalf("githubInstallationManageURL(%q, %q, %d) = %q, want %q",
				tc.accountType, tc.account, tc.id, got, tc.want)
		}
	}
}

func TestLoadBootstrapStatusUsesRepoAndBootstrapFakes(t *testing.T) {
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			Kind:                 "node",
			ValidationStatus:     bootstrap.StatusPartial,
			DeferredCapabilities: []string{"oauth"},
		},
		summaries: []bootstrap.SecretSummary{
			{Name: "API_KEY", Filled: true},
			{Name: "OAUTH_CLIENT_SECRET", Filled: false},
		},
	}
	b := newBypassOrgBot(t, "admin")
	b.bootstrap = boot
	b.lookupRepoFn = func(_ context.Context, orgID, owner, name string) (sqlc.GithubRepo, error) {
		if orgID != "org_test" || owner != "sleuth-io" {
			t.Fatalf("lookup repo got org=%q owner=%q", orgID, owner)
		}
		if name == "missing" {
			return sqlc.GithubRepo{}, pgx.ErrNoRows
		}
		return sqlc.GithubRepo{InstallationID: 11, RepoID: 22, Owner: owner, Name: name}, nil
	}

	got, err := b.loadBootstrapStatus(context.Background(), []integrationRepo{
		{OrgID: "org_test", Owner: "sleuth-io", Name: "hetchy"},
		{OrgID: "org_test", Owner: "sleuth-io", Name: "missing"},
	})
	if err != nil {
		t.Fatalf("loadBootstrapStatus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("status count = %d, want 1: %#v", len(got), got)
	}
	status := got["sleuth-io/hetchy"]
	if status.Status != string(bootstrap.StatusPartial) || status.Kind != "node" {
		t.Fatalf("status = %+v", status)
	}
	if status.AllFilled {
		t.Fatal("AllFilled = true, want false when one secret is missing")
	}
	if len(status.Secrets) != 2 || status.Secrets[1].Name != "OAUTH_CLIENT_SECRET" || status.Secrets[1].Filled {
		t.Fatalf("secrets = %+v", status.Secrets)
	}
	if len(status.DeferredCapabilities) != 1 || status.DeferredCapabilities[0] != "oauth" {
		t.Fatalf("deferred capabilities = %#v", status.DeferredCapabilities)
	}
}

func TestPopulateSettingsTabDataAgentsUsesFallbackProfiles(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	data := map[string]any{}

	if err := b.populateSettingsTabData(context.Background(), "org_test", "agents", data); err != nil {
		t.Fatalf("populateSettingsTabData: %v", err)
	}

	agents, ok := data["Agents"].([]agentSettingsView)
	if !ok {
		t.Fatalf("Agents type = %T, want []agentSettingsView", data["Agents"])
	}
	if len(agents) < 3 {
		t.Fatalf("agents count = %d, want fallback profiles: %#v", len(agents), agents)
	}
	builtIns, ok := data["BuiltInAgents"].([]agentSettingsView)
	if !ok {
		t.Fatalf("BuiltInAgents type = %T, want []agentSettingsView", data["BuiltInAgents"])
	}
	if len(builtIns) < 3 {
		t.Fatalf("built-in agents count = %d, want fallback profiles: %#v", len(builtIns), builtIns)
	}
	custom, ok := data["CustomAgents"].([]agentSettingsView)
	if !ok {
		t.Fatalf("CustomAgents type = %T, want []agentSettingsView", data["CustomAgents"])
	}
	if len(custom) != 0 {
		t.Fatalf("custom agents = %#v, want none for fallback built-ins", custom)
	}
	for _, want := range []string{"bob", "alice", "archy"} {
		found := false
		for _, got := range agents {
			if got.Slug == want && got.BuiltIn {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("fallback agent %q not found in %#v", want, agents)
		}
	}
}

func TestSettingsHandlerGetAndPostWithFakes(t *testing.T) {
	store := &fakeOrgStore{
		getConfig: orgcfg.Config{
			OrgID:                "org_test",
			AnthropicAPIKey:      "sk-ant-old",
			SlackBotToken:        "xoxb-old",
			DefaultGitHubOwner:   "sleuth-io",
			DefaultGitHubRepo:    "hetchy",
			ClaudeCodeOAuthToken: "oauth-old",
			SXKey:                "sx-old",
		},
	}
	b := newBypassOrgBot(t, "admin")
	b.orgs = store
	b.slack = newSlackManager(discardLogger(), store, nil)
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org?tab=general&saved=1", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET settings status = %d body=%q", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"org_test"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET settings body missing %q", want)
		}
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org?tab=general", "org_name=Renamed+Org")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("POST general status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=general&saved=1" {
		t.Fatalf("POST general redirect = %q", got)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org?tab=integrations", "default_repo=&slack_bot_token=xoxb-new&slack_socket_token=xapp-new&sx_key=sx-new&anthropic_api_key=sk-ant-new")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("POST integrations status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=integrations&saved=1" {
		t.Fatalf("POST integrations redirect = %q", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	saved := store.upserts[0]
	if saved.OrgID != "org_test" || saved.SlackBotToken != "xoxb-new" || saved.SlackSocketToken != "xapp-new" || saved.SXKey != "sx-new" {
		t.Fatalf("saved org config = %+v", saved)
	}
	if saved.AnthropicAPIKey != "sk-ant-new" || saved.ClaudeCodeOAuthToken != "" {
		t.Fatalf("anthropic credential state = api:%q oauth:%q", saved.AnthropicAPIKey, saved.ClaudeCodeOAuthToken)
	}
	if saved.DefaultGitHubOwner != "" || saved.DefaultGitHubRepo != "" {
		t.Fatalf("default repo should be cleared, got %s/%s", saved.DefaultGitHubOwner, saved.DefaultGitHubRepo)
	}
}

func TestSettingsHandlerSkillsNewSaveDisconnectsGitVault(t *testing.T) {
	store := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	sx := &fakeSXManager{gitVault: sxsync.GitVaultView{Configured: true, RepositorySlug: "acme/vault"}}
	b := newBypassOrgBot(t, "admin")
	b.orgs = store
	b.sx = sx
	b.slack = newSlackManager(discardLogger(), store, nil)
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org?tab=integrations", "sx_key=sx-new")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !sx.deletedGitVault {
		t.Fatal("saving a Skills.new token should disconnect the Git Vault")
	}
	if len(store.upserts) != 1 {
		t.Fatalf("org upserts = %d, want 1", len(store.upserts))
	}
	if store.upserts[0].SXKey != "sx-new" {
		t.Fatalf("saved SXKey = %q, want sx-new", store.upserts[0].SXKey)
	}
}

func TestSettingsHandlerMembersTabFallsBackForNonAdmin(t *testing.T) {
	store := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	b := newBypassOrgBot(t, "member")
	b.orgs = store
	b.slack = newSlackManager(discardLogger(), store, nil)
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org?tab=members", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("member GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `href="/settings/org?tab=general" class="active"`) {
		t.Fatalf("member settings should fall back to general tab")
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org?tab=general", "org_name=Nope")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member POST status = %d want %d", rec.Code, http.StatusForbidden)
	}
}

func TestInviteHandlerBypassAuthSuccessAndValidation(t *testing.T) {
	admin := newBypassOrgBot(t, "admin")
	handler := admin.auth.Middleware(admin.auth.RequireOrg(http.HandlerFunc(admin.inviteHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/invite", "email=new%40example.com&role=admin")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("invite status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=members&saved=invited" {
		t.Fatalf("invite redirect = %q", got)
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/invite", "email=not-an-email&role=member")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad email status = %d want %d", rec.Code, http.StatusBadRequest)
	}

	member := newBypassOrgBot(t, "member")
	handler = member.auth.Middleware(member.auth.RequireOrg(http.HandlerFunc(member.inviteHandler)))
	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/invite", "email=new%40example.com&role=member")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member invite status = %d want %d", rec.Code, http.StatusForbidden)
	}
}

type localInviteQuerier struct {
	sqlc.Querier
	created sqlc.CreateLocalAuthInvitationParams
}

func (q *localInviteQuerier) CreateLocalAuthInvitation(_ context.Context, arg sqlc.CreateLocalAuthInvitationParams) (sqlc.LocalAuthInvitation, error) {
	q.created = arg
	return sqlc.LocalAuthInvitation{
		ID:              arg.ID,
		OrgID:           arg.OrgID,
		Email:           arg.Email,
		EmailNormalized: arg.EmailNormalized,
		RoleSlug:        arg.RoleSlug,
		TokenHash:       arg.TokenHash,
		ExpiresAt:       arg.ExpiresAt,
		CreatedBy:       arg.CreatedBy,
	}, nil
}

func TestInviteHandlerLocalAuthSetsFlash(t *testing.T) {
	q := &localInviteQuerier{}
	a, err := auth.New(auth.Config{Mode: auth.AuthModeLocal, LocalQueries: q})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	b := newBypassOrgBot(t, "admin")
	b.auth = a
	b.cfg.PublicBaseURLOverride = "https://app.example.test"
	cipher, err := secrets.New(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	b.cipher = cipher

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/invite", "email=new%40example.com&role=member")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user_local_admin",
		OrgID:  "org_local_test",
		Role:   "admin",
	}))
	b.inviteHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("invite status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=members&saved=invited" {
		t.Fatalf("invite redirect = %q", got)
	}
	if strings.Contains(rec.Header().Get("Location"), "invite=") {
		t.Fatal("redirect should not expose local invite token")
	}
	if q.created.OrgID != "org_local_test" || q.created.EmailNormalized != "new@example.com" || q.created.RoleSlug != "member" {
		t.Fatalf("created invitation = %+v", q.created)
	}
	if q.created.CreatedBy == nil || *q.created.CreatedBy != "user_local_admin" {
		t.Fatalf("created by = %v, want user_local_admin", q.created.CreatedBy)
	}

	var flash *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == localInviteFlashCookieName {
			flash = cookie
			break
		}
	}
	if flash == nil {
		t.Fatal("missing local invite flash cookie")
	}
	if strings.Contains(flash.Value, "invite=") {
		t.Fatal("flash cookie should not expose the raw invite URL")
	}

	clearRec := httptest.NewRecorder()
	consumeReq := httptest.NewRequest(http.MethodGet, "/settings/org?tab=members", nil)
	consumeReq.AddCookie(flash)
	inviteURL := b.consumeLocalInviteURLFlash(clearRec, consumeReq)
	parsed, err := url.Parse(inviteURL)
	if err != nil {
		t.Fatalf("parse invite URL %q: %v", inviteURL, err)
	}
	if parsed.Scheme != "https" || parsed.Host != "app.example.test" || parsed.Path != "/signup" {
		t.Fatalf("invite URL = %q", inviteURL)
	}
	if parsed.Query().Get("invite") == "" {
		t.Fatalf("invite URL missing token: %q", inviteURL)
	}
}

func TestLocalInviteURLFlashRoundTrip(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	cipher, err := secrets.New(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	b.cipher = cipher
	const inviteURL = "http://localhost:8080/signup?invite=secret-token"

	rec := httptest.NewRecorder()
	if err := b.setLocalInviteURLFlash(rec, inviteURL); err != nil {
		t.Fatalf("setLocalInviteURLFlash: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if strings.Contains(cookies[0].Value, "secret-token") {
		t.Fatal("flash cookie should not expose the raw invite token")
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/org?tab=members", nil)
	req.AddCookie(cookies[0])
	clearRec := httptest.NewRecorder()
	if got := b.consumeLocalInviteURLFlash(clearRec, req); got != inviteURL {
		t.Fatalf("consumeLocalInviteURLFlash = %q, want %q", got, inviteURL)
	}
	if cleared := clearRec.Result().Cookies()[0]; cleared.Name != localInviteFlashCookieName || cleared.MaxAge >= 0 {
		t.Fatalf("flash cookie was not cleared: %+v", cleared)
	}
}

func TestLocalInviteURLFlashPlainMissingAndInvalid(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	const inviteURL = "http://localhost:8080/signup?invite=plain-token"

	encoded, err := b.encodeLocalInviteFlash(inviteURL)
	if err != nil {
		t.Fatalf("encodeLocalInviteFlash: %v", err)
	}
	decoded, err := b.decodeLocalInviteFlash(encoded)
	if err != nil {
		t.Fatalf("decodeLocalInviteFlash: %v", err)
	}
	if decoded != inviteURL {
		t.Fatalf("decodeLocalInviteFlash = %q, want %q", decoded, inviteURL)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org?tab=members", nil)
	if got := b.consumeLocalInviteURLFlash(rec, req); got != "" {
		t.Fatalf("consume missing flash = %q, want empty", got)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("missing flash should not set a clearing cookie")
	}

	req.AddCookie(&http.Cookie{Name: localInviteFlashCookieName, Value: "%%%not-base64"})
	rec = httptest.NewRecorder()
	if got := b.consumeLocalInviteURLFlash(rec, req); got != "" {
		t.Fatalf("consume invalid flash = %q, want empty", got)
	}
	if cleared := rec.Result().Cookies()[0]; cleared.Name != localInviteFlashCookieName || cleared.MaxAge >= 0 {
		t.Fatalf("invalid flash cookie was not cleared: %+v", cleared)
	}
}

type localAuthCleanupQuerier struct {
	sqlc.Querier
	calls int
	err   error
}

func (q *localAuthCleanupQuerier) DeleteExpiredLocalAuthSessions(context.Context) error {
	q.calls++
	return q.err
}

func TestLocalAuthSessionCleanup(t *testing.T) {
	(&Bot{log: discardLogger()}).runLocalAuthSessionCleanupLoop(context.Background())

	q := &localAuthCleanupQuerier{}
	a, err := auth.New(auth.Config{Mode: auth.AuthModeLocal, LocalQueries: q})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	b := &Bot{log: discardLogger(), auth: a}
	b.cleanupExpiredLocalAuthSessions(context.Background())
	if q.calls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", q.calls)
	}

	q.err = context.Canceled
	b.cleanupExpiredLocalAuthSessions(context.Background())
	if q.calls != 2 {
		t.Fatalf("cleanup calls after canceled error = %d, want 2", q.calls)
	}
}

func TestInvitationActionHandlerBypassAuthRevoke(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.invitationActionHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/invitations/inv_123/revoke", "")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("revoke status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=members&saved=revoked" {
		t.Fatalf("revoke redirect = %q", got)
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/invitations/inv_123/delete", "")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action status = %d want %d", rec.Code, http.StatusNotFound)
	}
}

func TestMemberActionHandlerBypassAuthRemoveAndRole(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.memberActionHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/members/mem_123/remove", "")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("remove status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=members&saved=removed" {
		t.Fatalf("remove redirect = %q", got)
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/members/mem_123/role", "role=admin")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("role status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=members&saved=role" {
		t.Fatalf("role redirect = %q", got)
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/members/mem_123/role", "role=owner")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad role status = %d want %d", rec.Code, http.StatusBadRequest)
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/members/mem_123/unknown", "")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action status = %d want %d", rec.Code, http.StatusNotFound)
	}
}

func settingsFormRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "example.com"
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestSettingsHandler_NonAdminPostReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_member",
		BypassEmail: "member@hetchy.local",
		BypassOrg:   "org_x",
		BypassRole:  "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a}

	for _, tab := range []string{"general", "integrations", "agents"} {
		req := httptest.NewRequest(http.MethodPost, "/settings/org?tab="+tab,
			strings.NewReader("org_name=Hacked"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		a.Middleware(http.HandlerFunc(b.settingsHandler)).ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("tab=%s: expected 403 for non-admin POST, got %d (body: %q)", tab, rec.Code, rec.Body.String())
		}
	}
}

func TestSplitAgentAction(t *testing.T) {
	cases := []struct {
		path, wantSlug, wantAction string
		wantOK                     bool
	}{
		{"/settings/org/agents/backend", "backend", "", true},
		{"/settings/org/agents/backend/delete", "backend", "delete", true},
		{"/settings/org/agents/sally-backend", "sally-backend", "", true},
		{"/settings/org/agents/Backend", "", "", false},
		{"/settings/org/agents/backend/delete/extra", "", "", false},
		{"/settings/org/agents/", "", "", false},
		{"/other/backend", "", "", false},
	}
	for _, tc := range cases {
		gotSlug, gotAction, gotOK := splitAgentAction(tc.path)
		if gotSlug != tc.wantSlug || gotAction != tc.wantAction || gotOK != tc.wantOK {
			t.Errorf("splitAgentAction(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, gotSlug, gotAction, gotOK, tc.wantSlug, tc.wantAction, tc.wantOK)
		}
	}
}

func TestAgentSettingsActionHandlerBuiltInsReadOnly(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentSettingsActionHandler)))

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{name: "save", path: "/settings/org/agents/alice", body: "display_name=Alicia"},
		{name: "install skill", path: "/settings/org/agents/alice/skills", body: "skill=golang-pro"},
		{name: "delete", path: "/settings/org/agents/alice/delete", body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := settingsFormRequest(http.MethodPost, tc.path, tc.body)
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d want %d body=%q", rec.Code, http.StatusForbidden, rec.Body.String())
			}
		})
	}
}

func TestSlackInstallHandler_NonAdminReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass: true, BypassUser: "user_member", BypassEmail: "m@hetchy.local",
		BypassOrg: "org_test", BypassRole: "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{
		log:  discardLogger(),
		cfg:  Config{WebPort: "0", SlackClientID: "cid", SlackClientSecret: "csec", SlackOAuthRedirectURI: "https://example.com/slack/callback"},
		auth: a,
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slack/install", nil)
	a.Middleware(a.RequireOrg(http.HandlerFunc(b.slackInstallHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestSplitIDAction(t *testing.T) {
	cases := []struct {
		path, prefix, wantID, wantAction string
		wantOK                           bool
	}{
		{"/settings/org/members/om_42/remove", "/settings/org/members/", "om_42", "remove", true},
		{"/settings/org/members/om_42/role", "/settings/org/members/", "om_42", "role", true},
		{"/settings/org/invitations/inv_1/revoke", "/settings/org/invitations/", "inv_1", "revoke", true},
		{"/settings/org/members/", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om_42", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om_42/", "/settings/org/members/", "", "", false},
		{"/other/path", "/settings/org/members/", "", "", false},
		// id sanitization rejects exotic chars
		{"/settings/org/members/om-42/remove", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om 42/remove", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om;42/remove", "/settings/org/members/", "", "", false},
	}
	for _, tc := range cases {
		id, action, ok := splitIDAction(tc.path, tc.prefix)
		if id != tc.wantID || action != tc.wantAction || ok != tc.wantOK {
			t.Errorf("splitIDAction(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, tc.prefix, id, action, ok, tc.wantID, tc.wantAction, tc.wantOK)
		}
	}
}

func TestValidRoleSlug(t *testing.T) {
	for _, ok := range []string{"admin", "member"} {
		if !validRoleSlug(ok) {
			t.Errorf("validRoleSlug(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "ADMIN", "owner", "admin ", "admin\n", "../admin"} {
		if validRoleSlug(bad) {
			t.Errorf("validRoleSlug(%q) = true, want false", bad)
		}
	}
}

func TestOrgDeleteHandlerBypassAuth(t *testing.T) {
	store := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}

	t.Run("admin POST wipes local data and logs out", func(t *testing.T) {
		b := newBypassOrgBot(t, "admin")
		b.orgs = store
		b.slack = newSlackManager(discardLogger(), store, nil)
		handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler)))

		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/delete", "")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		// LogoutHandler redirects to scheme://publicHost (bypass mode
		// short-circuits to "/?signed_out=1"). Either way, a session
		// cookie clear should be in the response.
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.deletes) != 1 || store.deletes[0] != "org_test" {
			t.Fatalf("deletes = %#v, want [org_test]", store.deletes)
		}
	})

	t.Run("non-admin POST is rejected", func(t *testing.T) {
		b := newBypassOrgBot(t, "member")
		b.orgs = store
		b.slack = newSlackManager(discardLogger(), store, nil)
		handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler)))

		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/delete", "")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin status = %d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("GET is rejected", func(t *testing.T) {
		b := newBypassOrgBot(t, "admin")
		b.orgs = store
		b.slack = newSlackManager(discardLogger(), store, nil)
		handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler)))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/settings/org/delete", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET status = %d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing Origin header is rejected", func(t *testing.T) {
		b := newBypassOrgBot(t, "admin")
		b.orgs = store
		b.slack = newSlackManager(discardLogger(), store, nil)
		handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler)))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/settings/org/delete", strings.NewReader(""))
		req.Host = "example.com"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-origin status = %d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("local wipe failure surfaces 500", func(t *testing.T) {
		// Reset deletes; install an error.
		failing := &fakeOrgStore{
			getConfig: orgcfg.Config{OrgID: "org_test"},
			deleteErr: pgx.ErrTxClosed,
		}
		b := newBypassOrgBot(t, "admin")
		b.orgs = failing
		b.slack = newSlackManager(discardLogger(), failing, nil)
		handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler)))

		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/delete", "")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("workos org delete failure skips local wipe", func(t *testing.T) {
		failing := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
		b := newBypassOrgBot(t, "admin")
		b.orgs = failing
		b.slack = newSlackManager(discardLogger(), failing, nil)
		b.deleteWorkOSOrgFn = func(context.Context, string) error {
			return errors.New("workos delete failed")
		}
		handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler)))

		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/delete", "")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		failing.mu.Lock()
		defer failing.mu.Unlock()
		if len(failing.deletes) != 0 {
			t.Fatalf("local deletes = %#v, want none", failing.deletes)
		}
	})

	t.Run("workos user delete failure skips local wipe", func(t *testing.T) {
		failing := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
		b := newBypassOrgBot(t, "admin")
		b.orgs = failing
		b.slack = newSlackManager(discardLogger(), failing, nil)
		b.usersOnlyInOrgFn = func(context.Context, string) ([]string, error) {
			return []string{"user_a", "user_b"}, nil
		}
		deleteOrgCalled := false
		b.deleteWorkOSOrgFn = func(context.Context, string) error {
			deleteOrgCalled = true
			return nil
		}
		b.deleteWorkOSUsersFn = func(_ context.Context, ids []string) error {
			if !slices.Equal(ids, []string{"user_a", "user_b"}) {
				t.Fatalf("delete user ids = %#v", ids)
			}
			return errors.New("workos user delete failed")
		}
		handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler)))

		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/delete", "")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		if !deleteOrgCalled {
			t.Fatal("DeleteOrganization was not called before DeleteUsers")
		}
		failing.mu.Lock()
		defer failing.mu.Unlock()
		if len(failing.deletes) != 0 {
			t.Fatalf("local deletes = %#v, want none", failing.deletes)
		}
	})
}
