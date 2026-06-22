package bot

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/sxsync"
	"github.com/sleuth-io/hetchy/internal/webui"
)

func TestSettingsTemplate_RendersAgentsTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	skillOptions := []agentSkillOptionView{
		{Name: "code-review", Source: "Skills.new", Description: "Review code changes"},
		{Name: "database-migrations", Source: "Skills.new"},
		{Name: "fix-pr_skill", DisplayName: "fix-pr", Source: "Skills.new", Description: "Fix PRs"},
		{Name: "golang-pro", Source: "Skills.new"},
	}
	teamOptions := []agentTeamOptionView{
		{Name: "Frontend", Description: "Frontend team"},
		{Name: "Backend", Description: "Backend team"},
	}
	reviewerSkillOptions, reviewerCanAddSkill := agentSkillOptionsForAgent(skillOptions, []string{"code-review"}, nil)
	reviewerTeamOptions, reviewerCanAddTeam := agentTeamOptionsForAgent(teamOptions, nil)
	hetchySkillOptions, hetchyCanAddSkill := agentSkillOptionsForAgent(skillOptions, nil, []string{"fix-pr", "webapp-testing"})
	hetchyTeamOptions, hetchyCanAddTeam := agentTeamOptionsForAgent(teamOptions, []string{"Frontend"})
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "agents", "SavedMessage": "", "SXEnabled": true,
		"AgentTemplates": []agentTemplateView{
			{
				Slug:          "backend",
				DisplayName:   "Backend",
				Skills:        []string{"golang-pro", "database-migrations"},
				PersonaPrompt: "Use backend rules.",
			},
		},
		"AgentSkillOptions": skillOptions,
		"AgentTeamOptions":  teamOptions,
		"Agents":            []agentSettingsView{},
		"CustomAgents": []agentSettingsView{
			{
				Slug:          "reviewer",
				DisplayName:   "Reviewer",
				Description:   "Reviews pull requests.",
				PersonaPrompt: "Review code carefully.",
				Skills:        []string{"code-review"},
				SkillChips:    []agentSkillChipView{{Name: "code-review", DisplayName: "code-review"}},
				SkillOptions:  reviewerSkillOptions,
				CanAddSkill:   reviewerCanAddSkill,
				TeamOptions:   reviewerTeamOptions,
				CanAddTeam:    reviewerCanAddTeam,
			},
			{
				Slug:         "hetchy-bot",
				DisplayName:  "Hetchy Bot",
				Description:  "Builds stuff for Hetchy.",
				SXBot:        "hetchy-bot",
				PersonaAsset: "hetchy-bot",
				SXTeams:      []string{"Frontend"},
				SXSkills:     []string{"fix-pr", "webapp-testing"},
				SXSkillChips: []agentSkillChipView{
					{Name: "fix-pr", DisplayName: "fix-pr"},
					{Name: "webapp-testing", DisplayName: "webapp-testing"},
				},
				VaultBackend: "skills_new",
				Imported:     true,
				SkillOptions: hetchySkillOptions,
				CanAddSkill:  hetchyCanAddSkill,
				TeamOptions:  hetchyTeamOptions,
				CanAddTeam:   hetchyCanAddTeam,
			},
		},
		"BuiltInAgents": []agentSettingsView{
			{
				Slug:         "code-reviewer",
				DisplayName:  "Code Reviewer",
				Description:  "Reviews code changes.",
				PersonaAsset: "code-reviewer",
				SlackAliases: []string{"backend", "api"},
				Skills:       []string{"golang-pro", "database-migrations"},
				SkillChips: []agentSkillChipView{
					{Name: "golang-pro", DisplayName: "golang-pro"},
					{Name: "database-migrations", DisplayName: "database-migrations"},
				},
				BuiltIn: true,
			},
		},
		"CatalogAgents": []agentTemplateView{
			{
				Slug:        "python-reviewer",
				DisplayName: "Python Reviewer",
				Description: "Reviews Python code.",
			},
		},
		"CatalogAgentCount": 1,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`href="/settings/org?tab=agents" class="active"`,
		`data-open-modal="modal-agent-create"`,
		`id="modal-agent-create"`,
		`data-skills="golang-pro,database-migrations"`,
		`data-template-slug="backend"`,
		`data-agent-skill-picker`,
		`data-skill-name="golang-pro"`,
		`data-skill-source="Skills.new"`,
		`data-agent-skill-hidden`,
		`Skills.new`,
		`Custom agents`,
		`Used built-in agents`,
		`Browse built-in agents (1)`,
		`data-agent-catalog-search`,
		`data-agent-catalog-item`,
		`Python Reviewer`,
		`Built-in`,
		`<span class="agent-badge">skills_new</span>`,
		`action="/settings/org/agents/reviewer"`,
		`action="/settings/org/agents/reviewer/delete"`,
		`data-confirm-title="Delete Reviewer?"`,
		`data-confirm-action="Delete"`,
		`name="display_name" value="Reviewer"`,
		`Review code carefully.`,
		`Inherited skills from orgs and teams`,
		`fix-pr`,
		`webapp-testing`,
		`Add skill`,
		`data-open-modal="modal-agent-skill-hetchy-bot"`,
		`id="modal-agent-skill-hetchy-bot"`,
		`class="save" type="submit">Install</button>`,
		`class="save" type="submit">Upload</button>`,
		`data-open-modal="modal-agent-team-hetchy-bot"`,
		`id="modal-agent-team-hetchy-bot"`,
		`action="/settings/org/agents/hetchy-bot/delete"`,
		`class="danger-btn agent-delete-link">Delete</button>`,
		`data-confirm-title="Delete Hetchy Bot?"`,
		`action="/settings/org/agents/hetchy-bot/skills"`,
		`action="/settings/org/agents/hetchy-bot/skills/upload"`,
		`data-settings-tab="existing-hetchy-bot"`,
		`data-settings-tab="upload-hetchy-bot"`,
		`data-settings-panel="existing-hetchy-bot"`,
		`data-settings-panel="upload-hetchy-bot"`,
		`data-skill-upload-drop`,
		`data-skill-upload-input`,
		`Drop a .zip file here or click to browse`,
		`The zip should contain skill files (SKILL.md, etc.)`,
		`golang-pro`,
		`database-migrations`,
		`<option value="code-review" title="Review code changes">code-review - Skills.new</option>`,
		`<option value="fix-pr_skill" disabled title="Already installed on this Agent">fix-pr - Skills.new</option>`,
		`action="/settings/org/agents/hetchy-bot/teams"`,
		`action="/settings/org/agents/hetchy-bot/teams/remove"`,
		`<option value="Frontend" disabled title="Already installed on this Agent">Frontend</option>`,
		`<option value="Backend" title="Backend team">Backend</option>`,
		`data-skill-name="fix-pr_skill" data-skill-display-name="fix-pr"`,
		`aliases: @backend, @api`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("agents tab missing %q", w)
		}
	}
	for _, n := range []string{
		`name="slug"`,
		`comma-separated skill names`,
		`action="/settings/org/agents/code-reviewer"`,
		`action="/settings/org/agents/code-reviewer/delete"`,
		`action="/settings/org/agents/hetchy-bot"`,
		`agent-name-hetchy-bot`,
		`agent-prompt-hetchy-bot`,
		`code-review - Review code changes`,
		`>fix-pr_skill<`,
		`name="skill_name"`,
		`name="skill_version"`,
		`onsubmit="return confirm(`,
		`Imported`,
		`sx bot:`,
		`vault:`,
		`teams: Frontend`,
		`agent-meta-delete`,
		`link-btn danger-link agent-delete-link`,
	} {
		if strings.Contains(body, n) {
			t.Errorf("agents tab should not render %q", n)
		}
	}
}

func TestSettingsTemplate_CollapsesLongSkillLists(t *testing.T) {
	b := newBypassBot(t)

	makeChips := func(prefix string, n int) []agentSkillChipView {
		out := make([]agentSkillChipView, n)
		for i := range n {
			name := fmt.Sprintf("%s-%02d", prefix, i+1)
			out[i] = agentSkillChipView{Name: name, DisplayName: name}
		}
		return out
	}
	makeNames := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range n {
			out[i] = fmt.Sprintf("%s-%02d", prefix, i+1)
		}
		return out
	}

	// 12 direct skills + 11 inherited skills → both should collapse.
	// "shorty" only has 8 direct skills, so it must render as-is.
	render := func() string {
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, map[string]any{
			"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
			"IsAdmin": true, "Tab": "agents", "SavedMessage": "", "SXEnabled": true,
			"Agents": []agentSettingsView{},
			"CustomAgents": []agentSettingsView{
				{
					Slug:        "longy",
					DisplayName: "Longy",
					Description: "Has many skills.",
					SkillChips:  makeChips("direct", 12),
					SXSkills:    makeNames("inherit", 11),
				},
				{
					Slug:        "shorty",
					DisplayName: "Shorty",
					Description: "Has few skills.",
					SkillChips:  makeChips("short", 8),
				},
			},
			"BuiltInAgents": []agentSettingsView{},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	body := render()

	collapsibleCount := strings.Count(body, "skill-chip-list-collapsible is-collapsed")
	if collapsibleCount != 2 {
		t.Fatalf("expected 2 collapsible chip lists (direct + inherited on longy), got %d", collapsibleCount)
	}
	for _, want := range []string{
		`data-expand-label="Show all (12)"`,
		`data-collapse-label="Show less"`,
		`>Show all (12)</button>`,
		`data-expand-label="Show all (11)"`,
		`>Show all (11)</button>`,
		`aria-controls="agent-skills-direct-longy"`,
		`aria-controls="agent-skills-inherited-longy"`,
		`id="agent-skills-direct-longy"`,
		`id="agent-skills-inherited-longy"`,
		`aria-expanded="false"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("collapsible chip list missing %q", want)
		}
	}

	// Shorty's 8 chips must not be wrapped in a collapsible list. Locate
	// the chip-list opening tag immediately preceding the first shorty chip.
	before, _, ok := strings.Cut(body, "short-01")
	if !ok {
		t.Fatalf("shorty chips missing from body")
	}
	shortListStart := strings.LastIndex(before, `<div id="agent-skills-direct-shorty"`)
	if shortListStart < 0 {
		t.Fatalf("could not locate shorty chip list opening tag")
	}
	shortListClose := strings.Index(body[shortListStart:], ">")
	if shortListClose < 0 {
		t.Fatalf("malformed shorty chip list tag")
	}
	shortListTag := body[shortListStart : shortListStart+shortListClose+1]
	if strings.Contains(shortListTag, "skill-chip-list-collapsible") {
		t.Errorf("8-chip list should not be collapsible, got %q", shortListTag)
	}
}

func TestSettingsTemplate_DisablesAgentCreateWithoutSX(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "agents", "SavedMessage": "",
		"Agents": []agentSettingsView{}, "CustomAgents": []agentSettingsView{}, "BuiltInAgents": []agentSettingsView{},
	})
	body := rec.Body.String()
	if !strings.Contains(body, `data-open-modal="modal-agent-create" disabled`) {
		t.Fatalf("agents tab should disable create without SX, body=%q", body)
	}
	for _, want := range []string{
		`class="sx-enable-callout"`,
		`href="/settings/org?tab=integrations&amp;expand=sx#integration-sx"`,
		"Enable SX to create custom agents",
		"Connect SX in Integrations to start creating agents",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("agents tab missing %q in callout, body=%q", want, body)
		}
	}
}

func TestSettingsTemplate_HidesSXCalloutWhenEnabled(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "agents", "SavedMessage": "", "SXEnabled": true,
		"Agents": []agentSettingsView{}, "CustomAgents": []agentSettingsView{}, "BuiltInAgents": []agentSettingsView{},
	})
	body := rec.Body.String()
	if strings.Contains(body, `class="sx-enable-callout"`) {
		t.Fatalf("agents tab should not render SX callout when SX is enabled, body=%q", body)
	}
}

func TestSettingsTemplate_SXCalloutForNonAdmin(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": false, "Tab": "agents", "SavedMessage": "",
		"Agents": []agentSettingsView{}, "CustomAgents": []agentSettingsView{}, "BuiltInAgents": []agentSettingsView{},
	})
	body := rec.Body.String()
	if !strings.Contains(body, `class="sx-enable-callout"`) {
		t.Fatalf("agents tab should render SX callout for non-admins too, body=%q", body)
	}
	if !strings.Contains(body, "Ask an administrator to connect SX") {
		t.Fatalf("non-admin callout should ask an administrator, body=%q", body)
	}
}

func TestSettingsTemplate_SXIntegrationExpandsWhenRequested(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "integrations", "SavedMessage": "",
		"GitHubAppEnabled": false, "SXExpand": true,
		"SXGitVault": sxsync.GitVaultView{},
	})
	body := rec.Body.String()
	if !strings.Contains(body, `data-integration="sx" id="integration-sx" open`) {
		t.Fatalf("integrations tab should open SX card when SXExpand is set, body=%q", body)
	}
}
