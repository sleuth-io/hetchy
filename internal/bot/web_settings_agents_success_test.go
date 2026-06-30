package bot

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/sxsync"
)

// fakeAgentSettingsStore satisfies agentSettingsStore so the settings mutation
// handlers can be exercised without a database-backed *agents.Store.
type fakeAgentSettingsStore struct {
	profile     agents.Profile
	getErr      error
	template    agents.Profile
	templateErr error
	deleteErr   error

	deleteCalled bool
	deletedOrg   string
	deletedSlug  string
}

func (f *fakeAgentSettingsStore) GetBySlug(_ context.Context, _, _ string) (agents.Profile, error) {
	if f.getErr != nil {
		return agents.Profile{}, f.getErr
	}
	return f.profile, nil
}

func (f *fakeAgentSettingsStore) GetTemplate(context.Context, string) (agents.Profile, error) {
	return f.template, f.templateErr
}

func (f *fakeAgentSettingsStore) Delete(_ context.Context, orgID, slug string) error {
	f.deleteCalled = true
	f.deletedOrg = orgID
	f.deletedSlug = slug
	return f.deleteErr
}

// activeBackendBot returns a bot whose activeSXBackend resolves to skills.new so
// custom agents without a pinned vault backend pass requireActiveAgentBackend.
func activeBackendBot(t *testing.T, sx *fakeSXManager) *Bot {
	t.Helper()
	b := newBypassOrgBot(t, "admin")
	b.sx = sx
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", SXKey: "management-token"}}
	return b
}

func customAgentProfile() agents.Profile {
	return agents.Profile{Slug: "reviewer", DisplayName: "Reviewer", Enabled: true}
}

func TestAttachAgentSkillFromSettingsSuccess(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/skills", "skill=fix-pr")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	b.attachAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_skill_saved" {
		t.Fatalf("Location = %q", got)
	}
	if sx.attachedSlug != "reviewer" || sx.attachedSkill != "fix-pr" {
		t.Fatalf("attach recorded slug=%q skill=%q", sx.attachedSlug, sx.attachedSkill)
	}
}

func TestAttachAgentSkillFromSettingsErrorPaths(t *testing.T) {
	t.Run("attach error", func(t *testing.T) {
		sx := &fakeSXManager{attachErr: errors.New("vault down")}
		b := activeBackendBot(t, sx)
		store := &fakeAgentSettingsStore{profile: customAgentProfile()}

		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/skills", "skill=fix-pr")
		_ = req.ParseForm()
		b.attachAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "vault down") {
			t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("sx not configured", func(t *testing.T) {
		b := activeBackendBot(t, &fakeSXManager{})
		b.sx = nil
		store := &fakeAgentSettingsStore{profile: customAgentProfile()}
		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/skills", "skill=fix-pr")
		_ = req.ParseForm()
		b.attachAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sx vault is not configured") {
			t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("agent not found", func(t *testing.T) {
		b := activeBackendBot(t, &fakeSXManager{})
		store := &fakeAgentSettingsStore{getErr: agents.ErrNotFound}
		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/skills", "skill=fix-pr")
		_ = req.ParseForm()
		b.attachAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d", rec.Code)
		}
	})
}

func TestDetachAgentSkillFromSettingsSuccess(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/skills/delete", "skill=fix-pr")
	_ = req.ParseForm()
	b.detachAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_skill_removed" {
		t.Fatalf("Location = %q", got)
	}
	if sx.detachedSlug != "reviewer" || sx.detachedSkill != "fix-pr" {
		t.Fatalf("detach recorded slug=%q skill=%q", sx.detachedSlug, sx.detachedSkill)
	}
}

func TestAddAgentTeamFromSettingsSuccess(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/teams", "team=Platform")
	_ = req.ParseForm()
	b.addAgentTeamFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_team_added" {
		t.Fatalf("Location = %q", got)
	}
	if sx.addedTeamSlug != "reviewer" || sx.addedTeam != "Platform" {
		t.Fatalf("add team recorded slug=%q team=%q", sx.addedTeamSlug, sx.addedTeam)
	}
}

func TestRemoveAgentTeamFromSettingsSuccess(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/teams/remove", "team=Platform")
	_ = req.ParseForm()
	b.removeAgentTeamFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_team_removed" {
		t.Fatalf("Location = %q", got)
	}
	if sx.removedTeamSlug != "reviewer" || sx.removedTeam != "Platform" {
		t.Fatalf("remove team recorded slug=%q team=%q", sx.removedTeamSlug, sx.removedTeam)
	}
}

func uploadSkillRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("skill_zip", filename)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/org/agents/reviewer/skills/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestAgentMutationErrorAndUnconfiguredBranches(t *testing.T) {
	type call func(b *Bot, rec *httptest.ResponseRecorder, req *http.Request, store agentSettingsStore)
	invoke := map[string]struct {
		path string
		body string
		fn   call
	}{
		"detach": {"/settings/org/agents/reviewer/skills/delete", "skill=fix-pr", func(b *Bot, rec *httptest.ResponseRecorder, req *http.Request, store agentSettingsStore) {
			b.detachAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
		}},
		"add-team": {"/settings/org/agents/reviewer/teams", "team=Platform", func(b *Bot, rec *httptest.ResponseRecorder, req *http.Request, store agentSettingsStore) {
			b.addAgentTeamFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
		}},
		"remove-team": {"/settings/org/agents/reviewer/teams/remove", "team=Platform", func(b *Bot, rec *httptest.ResponseRecorder, req *http.Request, store agentSettingsStore) {
			b.removeAgentTeamFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
		}},
	}

	for name, tc := range invoke {
		t.Run(name+" sx error", func(t *testing.T) {
			sx := &fakeSXManager{detachErr: errors.New("boom"), addTeamErr: errors.New("boom"), removeTeamErr: errors.New("boom")}
			b := activeBackendBot(t, sx)
			store := &fakeAgentSettingsStore{profile: customAgentProfile()}
			rec := httptest.NewRecorder()
			req := settingsFormRequest(http.MethodPost, tc.path, tc.body)
			_ = req.ParseForm()
			tc.fn(b, rec, req, store)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "boom") {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
		})

		t.Run(name+" sx not configured", func(t *testing.T) {
			b := activeBackendBot(t, &fakeSXManager{})
			b.sx = nil
			store := &fakeAgentSettingsStore{profile: customAgentProfile()}
			rec := httptest.NewRecorder()
			req := settingsFormRequest(http.MethodPost, tc.path, tc.body)
			_ = req.ParseForm()
			tc.fn(b, rec, req, store)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sx vault is not configured") {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUploadAgentSkillFromSettingsSuccess(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := uploadSkillRequest(t, "Custom Skill.zip", []byte("PK\x03\x04 not really a zip"))
	b.uploadAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_skill_uploaded" {
		t.Fatalf("Location = %q", got)
	}
	if sx.uploadedSlug != "reviewer" || sx.uploadedSpec.Name != "custom-skill" {
		t.Fatalf("upload recorded slug=%q name=%q", sx.uploadedSlug, sx.uploadedSpec.Name)
	}
	if sx.uploadedSpec.Version != uploadedSkillInitialVersion {
		t.Fatalf("upload version = %q", sx.uploadedSpec.Version)
	}
}

func TestUploadAgentSkillFromSettingsRejectsBadFilename(t *testing.T) {
	b := activeBackendBot(t, &fakeSXManager{})
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := uploadSkillRequest(t, ".zip", []byte("data"))
	b.uploadAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "skill name") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestUploadAgentSkillFromSettingsMissingFile(t *testing.T) {
	b := activeBackendBot(t, &fakeSXManager{})
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/skills/upload", "")
	b.uploadAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "skill zip is required") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestUpdateAgentFromSettingsSuccess(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer", strings.Join([]string{
		"display_name=Senior Reviewer",
		"description=Looks at PRs",
		"persona_prompt=Be thorough",
	}, "&"))
	_ = req.ParseForm()
	b.updateAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_saved" {
		t.Fatalf("Location = %q", got)
	}
	if len(sx.savedAgents) != 1 {
		t.Fatalf("saved agents = %d", len(sx.savedAgents))
	}
	saved := sx.savedAgents[0]
	if saved.DisplayName != "Senior Reviewer" || saved.Description != "Looks at PRs" || saved.PersonaPrompt != "Be thorough" {
		t.Fatalf("saved profile = %+v", saved)
	}
	if !saved.Enabled {
		t.Fatalf("saved profile should be enabled: %+v", saved)
	}
}

func TestUpdateAgentFromSettingsSaveError(t *testing.T) {
	sx := &fakeSXManager{saveErr: errors.New("save failed")}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer", "display_name=Reviewer")
	_ = req.ParseForm()
	b.updateAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "save failed") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestUpdateAgentFromSettingsSXNotConfigured(t *testing.T) {
	b := activeBackendBot(t, &fakeSXManager{})
	b.sx = nil
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer", "display_name=Reviewer")
	_ = req.ParseForm()
	b.updateAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sx vault is not configured") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestUploadAgentSkillFromSettingsUploadError(t *testing.T) {
	sx := &fakeSXManager{uploadErr: errors.New("upload failed")}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := uploadSkillRequest(t, "Custom Skill.zip", []byte("data"))
	b.uploadAgentSkillFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "upload failed") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestUpdateAgentFromSettingsRequiresName(t *testing.T) {
	b := activeBackendBot(t, &fakeSXManager{})
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer", "display_name=%20")
	_ = req.ParseForm()
	b.updateAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent name is required") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestUpdateAgentFromSettingsLocksImportedAgents(t *testing.T) {
	b := activeBackendBot(t, &fakeSXManager{})
	imported := customAgentProfile()
	imported.SyncStatus = "imported"
	store := &fakeAgentSettingsStore{profile: imported}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer", "display_name=Reviewer")
	_ = req.ParseForm()
	b.updateAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "imported agents") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestDeleteAgentFromSettingsSuccess(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/delete", "")
	_ = req.ParseForm()
	b.deleteAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=agents&saved=agent_deleted" {
		t.Fatalf("Location = %q", got)
	}
	if sx.deletedAgent != "reviewer" {
		t.Fatalf("sx delete recorded = %q", sx.deletedAgent)
	}
	if store.deleteCalled {
		t.Fatalf("store delete should not be called when sx delete succeeds")
	}
}

func TestDeleteAgentFromSettingsFallsBackToStore(t *testing.T) {
	sx := &fakeSXManager{deleteAgentErr: sxsync.ErrNotConfigured}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/delete", "")
	_ = req.ParseForm()
	b.deleteAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !store.deleteCalled || store.deletedSlug != "reviewer" || store.deletedOrg != "org_test" {
		t.Fatalf("store delete called=%v org=%q slug=%q", store.deleteCalled, store.deletedOrg, store.deletedSlug)
	}
}

func TestDeleteAgentFromSettingsNotFound(t *testing.T) {
	sx := &fakeSXManager{deleteAgentErr: sxsync.ErrNotConfigured}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile(), deleteErr: agents.ErrNotFound}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/delete", "")
	_ = req.ParseForm()
	b.deleteAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestDeleteAgentFromSettingsStoreError(t *testing.T) {
	sx := &fakeSXManager{deleteAgentErr: sxsync.ErrNotConfigured}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{profile: customAgentProfile(), deleteErr: errors.New("db down")}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents/reviewer/delete", "")
	_ = req.ParseForm()
	b.deleteAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, "reviewer", store)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "db down") {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestCreateAgentFromSettingsRejectsDuplicateSlug(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	// GetBySlug returns an existing agent (no error) -> duplicate.
	store := &fakeAgentSettingsStore{profile: customAgentProfile()}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents", "display_name=Reviewer")
	_ = req.ParseForm()
	b.createAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent already exists") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(sx.savedAgents) != 0 {
		t.Fatalf("should not save duplicate agent: %+v", sx.savedAgents)
	}
}

func TestCreateAgentFromSettingsTemplateNotFound(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{templateErr: agents.ErrNotFound}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents", "template_slug=missing&display_name=Reviewer")
	_ = req.ParseForm()
	b.createAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, store)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent template not found") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestCreateAgentFromSettingsCheckSlugError(t *testing.T) {
	sx := &fakeSXManager{}
	b := activeBackendBot(t, sx)
	store := &fakeAgentSettingsStore{getErr: errors.New("db unreachable")}

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/agents", "display_name=Reviewer")
	_ = req.ParseForm()
	b.createAgentFromSettings(rec, req, "org_test", sxsync.Actor{}, store)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "db unreachable") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
