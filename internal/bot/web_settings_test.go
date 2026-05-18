package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/billing"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestGithubInstallationManageURL(t *testing.T) {
	cases := []struct {
		accountType string
		account     string
		id          int64
		want        string
	}{
		{"Organization", "hetchyhq", 42, "https://github.com/organizations/hetchyhq/settings/installations/42"},
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
		if orgID != "org_test" || owner != "hetchyhq" {
			t.Fatalf("lookup repo got org=%q owner=%q", orgID, owner)
		}
		if name == "missing" {
			return sqlc.GithubRepo{}, pgx.ErrNoRows
		}
		return sqlc.GithubRepo{InstallationID: 11, RepoID: 22, Owner: owner, Name: name}, nil
	}

	got, err := b.loadBootstrapStatus(context.Background(), []integrationRepo{
		{OrgID: "org_test", Owner: "hetchyhq", Name: "hetchy"},
		{OrgID: "org_test", Owner: "hetchyhq", Name: "missing"},
	})
	if err != nil {
		t.Fatalf("loadBootstrapStatus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("status count = %d, want 1: %#v", len(got), got)
	}
	status := got["hetchyhq/hetchy"]
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
			DefaultGitHubOwner:   "hetchyhq",
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

func TestApplyDefaultRepoChange(t *testing.T) {
	b := &Bot{}
	cases := []struct {
		name      string
		form      map[string][]string
		current   orgcfg.Config
		wantOK    bool
		wantOwner string
		wantRepo  string
		wantCode  int
	}{
		{
			name:      "absent field leaves existing selection",
			form:      map[string][]string{},
			current:   orgcfg.Config{DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
			wantOK:    true,
			wantOwner: "hetchyhq",
			wantRepo:  "hetchy",
			wantCode:  http.StatusOK,
		},
		{
			name:     "blank field clears selection",
			form:     map[string][]string{"default_repo": {""}},
			current:  orgcfg.Config{DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
			wantOK:   true,
			wantCode: http.StatusOK,
		},
		{
			name:      "malformed field errors before store lookup",
			form:      map[string][]string{"default_repo": {"not-a-slug"}},
			current:   orgcfg.Config{DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
			wantOK:    false,
			wantOwner: "hetchyhq",
			wantRepo:  "hetchy",
			wantCode:  http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/settings/org", nil)
			req.PostForm = tc.form
			current := tc.current
			gotOK := b.applyDefaultRepoChange(rec, req, "org_test", &current)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if current.DefaultGitHubOwner != tc.wantOwner || current.DefaultGitHubRepo != tc.wantRepo {
				t.Fatalf("default repo = %s/%s, want %s/%s",
					current.DefaultGitHubOwner, current.DefaultGitHubRepo, tc.wantOwner, tc.wantRepo)
			}
			gotCode := rec.Code
			if gotCode == 0 {
				gotCode = http.StatusOK
			}
			if gotCode != tc.wantCode {
				t.Fatalf("status = %d, want %d body=%q", gotCode, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestBillingTopupSettingsFromSpend(t *testing.T) {
	account := billing.Account{PlanCode: billing.PlanGrowth, PerRunMaxCredits: 6}
	got := billingTopupSettingsFromSpend(account, true, 2000)

	if !got.AutoTopupEnabled {
		t.Fatal("AutoTopupEnabled = false, want true")
	}
	if got.TriggerThreshold != 6 {
		t.Fatalf("TriggerThreshold = %d, want 6", got.TriggerThreshold)
	}
	if got.TargetBalance != 16 {
		t.Fatalf("TargetBalance = %d, want 16", got.TargetBalance)
	}
	// Growth top-ups are $6.50, so a $20 spend cap permits 3 whole units.
	if got.MonthlyMaxUnits != 3 {
		t.Fatalf("MonthlyMaxUnits = %d, want 3", got.MonthlyMaxUnits)
	}
	if got.MonthlyMaxCents != 2000 {
		t.Fatalf("MonthlyMaxCents = %d, want 2000", got.MonthlyMaxCents)
	}

	got = billingTopupSettingsFromSpend(
		billing.Account{PlanCode: billing.PlanStarter, PerRunMaxCredits: 1},
		true,
		2000,
	)
	if got.MonthlyMaxUnits != 1 {
		t.Fatalf("starter MonthlyMaxUnits = %d, want 1", got.MonthlyMaxUnits)
	}
}

func TestBillingPlanSwitchConfirmation(t *testing.T) {
	team, _ := billing.PaidPlanByCode(billing.PlanTeam)
	growth, _ := billing.PaidPlanByCode(billing.PlanGrowth)

	title, msg := billingPlanSwitchConfirmation(team, true, growth, false, true, "Jun 18, 2026")
	if title != "Switch to Growth?" {
		t.Fatalf("upgrade title = %q, want Switch to Growth?", title)
	}
	if !strings.Contains(msg, "takes effect immediately") || !strings.Contains(msg, "prorated") {
		t.Fatalf("upgrade message = %q, want immediate prorated billing copy", msg)
	}

	title, msg = billingPlanSwitchConfirmation(growth, true, team, false, true, "Jun 18, 2026")
	if title != "Switch to Team?" {
		t.Fatalf("downgrade title = %q, want Switch to Team?", title)
	}
	if !strings.Contains(msg, "Jun 18, 2026") || !strings.Contains(msg, "no immediate charge") {
		t.Fatalf("downgrade message = %q, want next-cycle no-charge copy", msg)
	}

	_, msg = billingPlanSwitchConfirmation(growth, true, team, true, true, "Jun 18, 2026")
	if msg != "" {
		t.Fatalf("current-plan confirmation = %q, want empty", msg)
	}
}

func TestBillingPendingPlanChange(t *testing.T) {
	effective := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	code, label, when, ok := billingPendingPlanChange(billing.Account{
		PlanCode:               billing.PlanBusiness,
		PendingPlanCode:        billing.PlanTeam,
		PendingPlanEffectiveAt: effective,
	})
	if !ok || code != billing.PlanTeam || label != "Team" || when != "Jun 18, 2026" {
		t.Fatalf("pending plan = (%q, %q, %q, %v), want Team on Jun 18, 2026", code, label, when, ok)
	}

	_, _, _, ok = billingPendingPlanChange(billing.Account{
		PlanCode:        billing.PlanTeam,
		PendingPlanCode: billing.PlanTeam,
	})
	if ok {
		t.Fatal("pending plan matching current plan should not render")
	}
}

func TestParseBillingCents(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 42},
		{"$27", 2700},
		{"12.50", 1250},
		{".99", 99},
		{"1,234.05", 123405},
		{"12.345", 42},
		{"-1", 42},
		{"abc", 42},
	}
	for _, tc := range cases {
		if got := parseBillingCents(tc.raw, 42); got != tc.want {
			t.Errorf("parseBillingCents(%q) = %d, want %d", tc.raw, got, tc.want)
		}
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

func TestPreviewSecret(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty stays empty", in: "", want: ""},
		{name: "short fully masked", in: "abc", want: "••••••••"},
		{name: "13 chars still fully masked", in: "abcdefghijklm", want: "••••••••"},
		{name: "14 chars exposes prefix and suffix", in: "ghp_AbCdEfwxyz", want: "ghp_Ab••••••wxyz"},
		{name: "long anthropic key", in: "sk-ant-api03_AbCdEf123456XyZ4", want: "sk-ant••••••XyZ4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewSecret(tc.in)
			if got != tc.want {
				t.Errorf("previewSecret(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestApplyTokenChange(t *testing.T) {
	cases := []struct {
		name     string
		form     map[string]string
		existing string
		want     string
	}{
		{name: "blank input keeps existing", form: map[string]string{"k": ""}, existing: "old", want: "old"},
		{name: "non-blank input rotates", form: map[string]string{"k": "new"}, existing: "old", want: "new"},
		{name: "remove action clears", form: map[string]string{"k": "", "k_action": "remove"}, existing: "old", want: ""},
		{name: "remove action wins over input", form: map[string]string{"k": "ignored", "k_action": "remove"}, existing: "old", want: ""},
		{name: "no field at all keeps existing", form: map[string]string{}, existing: "old", want: "old"},
		{name: "whitespace input keeps existing", form: map[string]string{"k": "   "}, existing: "old", want: "old"},
		{name: "embedded newline stripped (terminal-wrap paste)", form: map[string]string{"k": "sk-ant-oat01-abc\nxyz"}, existing: "old", want: "sk-ant-oat01-abcxyz"},
		{name: "embedded CRLF stripped", form: map[string]string{"k": "sk-ant-api03-abc\r\nxyz"}, existing: "old", want: "sk-ant-api03-abcxyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
			req.PostForm = make(map[string][]string)
			for k, v := range tc.form {
				req.PostForm[k] = []string{v}
			}
			got := applyTokenChange(req, "k", tc.existing)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
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

func TestSavedMessage(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"unknown":                    "",
		"1":                          "Settings saved.",
		"invited":                    "Invitation sent.",
		"revoked":                    "Invitation revoked.",
		"removed":                    "Member removed.",
		"role":                       "Role updated.",
		"slack_installed":            "Slack installed.",
		"slack_install_cancelled":    "Slack install cancelled.",
		"slack_install_conflict":     "That Slack workspace is already connected to another Hetchy organization. Have the existing org uninstall first.",
		"github_installed":           "GitHub App installed. Repos and teams have been synced.",
		"github_synced":              "Sync complete.",
		"github_install_conflict":    "That GitHub installation is already connected to another Hetchy organization. Have the existing org uninstall first (or pick a different account).",
		"github_disconnected":        "GitHub installation removed. The Hetchy GitHub App has been uninstalled from that account.",
		"slack_disconnected":         "Slack disconnected. The Hetchy app has been removed from that workspace.",
		"slack_already_disconnected": "Slack was already disconnected.",
		"agent_saved":                "Agent saved.",
		"agent_deleted":              "Agent deleted.",
	}
	for in, want := range cases {
		if got := savedMessage(in); got != want {
			t.Errorf("savedMessage(%q) = %q, want %q", in, got, want)
		}
	}
}
