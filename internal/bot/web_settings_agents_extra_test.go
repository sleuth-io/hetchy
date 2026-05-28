package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

func TestCreateAgentFromSettingsUsesTemplateAndSelectedSkills(t *testing.T) {
	sx := &fakeSXManager{}
	b := newBypassOrgBot(t, "admin")
	b.sx = sx
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", SXKey: "management-token"}}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents", strings.Join([]string{
		"template_slug=alice",
		"display_name=Reviewer",
		"skills_submitted=1",
		"skills=frontend-design",
		"skills=webapp-testing",
		"skills=frontend-design",
	}, "&"))

	b.createAgentFromSettings(rec, req, "org_test", sxsync.Actor{Name: "Admin", Email: "admin@example.com"})

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_created" {
		t.Fatalf("Location = %q", got)
	}
	if len(sx.savedAgents) != 1 {
		t.Fatalf("saved agents = %d, want 1", len(sx.savedAgents))
	}
	saved := sx.savedAgents[0]
	if saved.Slug != "reviewer" || saved.DisplayName != "Reviewer" || saved.BuiltIn {
		t.Fatalf("saved profile identity = %+v", saved)
	}
	if saved.PersonaPrompt == "" || !strings.Contains(saved.PersonaPrompt, "Alice") {
		t.Fatalf("template prompt was not preserved: %q", saved.PersonaPrompt)
	}
	if !reflect.DeepEqual(saved.Skills, []string{"frontend-design", "webapp-testing"}) {
		t.Fatalf("skills = %+v", saved.Skills)
	}
}

func TestCreateAgentFromSettingsRejectsWhenSXDisabled(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.sx = &fakeSXManager{}
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents", "display_name=Reviewer")
	b.createAgentFromSettings(rec, req, "org_test", sxsync.Actor{})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "enable SX") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestAgentSettingsActionHandlerMethodAuthAndCreate(t *testing.T) {
	sx := &fakeSXManager{}
	b := newBypassOrgBot(t, "admin")
	b.sx = sx
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", SXKey: "management-token"}}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentSettingsActionHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/agents", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", rec.Code)
	}

	member := newBypassOrgBot(t, "member")
	member.sx = sx
	member.orgs = b.orgs
	memberHandler := member.auth.Middleware(member.auth.RequireOrg(http.HandlerFunc(member.agentSettingsActionHandler)))
	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/agents", "display_name=Reviewer")
	memberHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member POST status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/agents", "display_name=Reviewer")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("create status = %d body=%q", rec.Code, rec.Body.String())
	}
	if len(sx.savedAgents) != 1 || sx.savedAgents[0].Slug != "reviewer" {
		t.Fatalf("saved agents = %+v", sx.savedAgents)
	}
}

func TestAgentSettingsHelpers(t *testing.T) {
	if got := firstAgentFormValue(" ", "\t", " Reviewer "); got != "Reviewer" {
		t.Fatalf("firstAgentFormValue = %q", got)
	}
	if got := cleanSkillValues([]string{" lint-helper, test-helper ", "lint-helper", "", "docs"}); !reflect.DeepEqual(got, []string{"lint-helper", "test-helper", "docs"}) {
		t.Fatalf("cleanSkillValues = %+v", got)
	}
	if got := mergeCSV([]string{"existing"}, "existing,lint-helper,test-helper"); !reflect.DeepEqual(got, []string{"existing", "lint-helper", "test-helper"}) {
		t.Fatalf("mergeCSV = %+v", got)
	}

	nameTests := []struct {
		filename string
		want     string
	}{
		{filename: "fix-pr.zip", want: "fix-pr"},
		{filename: "My Skill.zip", want: "my-skill"},
		{filename: `C:\Users\me\Review Bot.ZIP`, want: "review-bot"},
		{filename: "../Odd_Name.zip", want: "odd-name"},
		{filename: "", want: ""},
	}
	for _, tc := range nameTests {
		if got := skillNameFromUploadFilename(tc.filename); got != tc.want {
			t.Fatalf("skillNameFromUploadFilename(%q) = %q, want %q", tc.filename, got, tc.want)
		}
	}

	tests := []struct {
		path       string
		wantSlug   string
		wantAction string
		wantOK     bool
	}{
		{"/settings/org/agents/reviewer", "reviewer", "", true},
		{"/settings/org/agents/reviewer/skills", "reviewer", "skills", true},
		{"/settings/org/agents/reviewer/skills/upload", "reviewer", "skills/upload", true},
		{"/settings/org/agents/Review%20Bot", "", "", false},
		{"/settings/org/agents/reviewer/bad/path", "", "", false},
	}
	for _, tc := range tests {
		slug, action, ok := splitAgentAction(tc.path)
		if slug != tc.wantSlug || action != tc.wantAction || ok != tc.wantOK {
			t.Fatalf("splitAgentAction(%q) = %q %q %v", tc.path, slug, action, ok)
		}
	}

	actor := sxActor(auth.Principal{Email: "admin@example.com", UserID: "user_1"})
	if actor.Name != "admin@example.com" || actor.Email != "admin@example.com" {
		t.Fatalf("sxActor with email = %+v", actor)
	}
	actor = sxActor(auth.Principal{UserID: "user_1"})
	if actor.Name != "user_1" || actor.Email != "" {
		t.Fatalf("sxActor without email = %+v", actor)
	}
}

func TestSXIntegrationEnabled(t *testing.T) {
	b := &Bot{}
	if ok, err := b.sxIntegrationEnabled(context.Background(), "org_1"); ok || err != nil {
		t.Fatalf("nil sx enabled=%v err=%v", ok, err)
	}

	b.sx = &fakeSXManager{gitVault: sxsync.GitVaultView{Configured: true}}
	if ok, err := b.sxIntegrationEnabled(context.Background(), "org_1"); !ok || err != nil {
		t.Fatalf("git vault enabled=%v err=%v", ok, err)
	}

	b.sx = &fakeSXManager{}
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", SXKey: "management-token"}}
	if ok, err := b.sxIntegrationEnabled(context.Background(), "org_1"); !ok || err != nil {
		t.Fatalf("skills.new enabled=%v err=%v", ok, err)
	}
	if got, err := b.activeSXBackend(context.Background(), "org_1"); got != sxsync.BackendSkillsNew || err != nil {
		t.Fatalf("active skills.new backend=%q err=%v", got, err)
	}

	b.sx = &fakeSXManager{gitVault: sxsync.GitVaultView{Configured: true}}
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", SXKey: "management-token"}}
	if got, err := b.activeSXBackend(context.Background(), "org_1"); got != sxsync.BackendSkillsNew || err != nil {
		t.Fatalf("active backend with both configured=%q err=%v", got, err)
	}

	b.sx = &fakeSXManager{}
	b.orgs = &fakeOrgStore{getErr: orgcfg.ErrNotFound}
	if ok, err := b.sxIntegrationEnabled(context.Background(), "org_1"); ok || err != nil {
		t.Fatalf("missing org enabled=%v err=%v", ok, err)
	}
}

func TestAgentAvailableForActiveSXBackend(t *testing.T) {
	if !agentAvailableForActiveSXBackend(agents.Profile{BuiltIn: true, VaultBackend: sxsync.BackendSkillsNew}, "") {
		t.Fatal("built-in agents should be available without an active SX backend")
	}
	if !agentAvailableForActiveSXBackend(agents.Profile{Slug: "local"}, "") {
		t.Fatal("local agents without a vault backend should remain available")
	}
	if !agentAvailableForActiveSXBackend(agents.Profile{Slug: "git", VaultBackend: sxsync.BackendGitHubGit}, sxsync.BackendGitHubGit) {
		t.Fatal("matching SX backend should be available")
	}
	if agentAvailableForActiveSXBackend(agents.Profile{Slug: "skills", VaultBackend: sxsync.BackendSkillsNew}, sxsync.BackendGitHubGit) {
		t.Fatal("inactive SX backend should not be available")
	}
	if agentAvailableForActiveSXBackend(agents.Profile{Slug: "skills", VaultBackend: sxsync.BackendSkillsNew}, "") {
		t.Fatal("SX-backed agent should not be available when SX is disabled")
	}
}

func TestHandleAgentEditError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/org/agents/reviewer", nil)
	rec := httptest.NewRecorder()
	handleAgentEditError(rec, req, agents.ErrNotFound)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handleAgentEditError(rec, req, errBuiltInAgentLocked)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("locked status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handleAgentEditError(rec, req, errImportedAgentLocked)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("imported locked status = %d", rec.Code)
	}
}
