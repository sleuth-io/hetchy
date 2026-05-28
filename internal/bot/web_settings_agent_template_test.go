package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/webui"
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
				Slug:         "alice",
				DisplayName:  "Alice",
				Description:  "Handles frontend work.",
				SXBot:        "bob",
				PersonaAsset: "bob",
				SlackAliases: []string{"backend", "api"},
				Skills:       []string{"golang-pro", "database-migrations"},
				SkillChips: []agentSkillChipView{
					{Name: "golang-pro", DisplayName: "golang-pro"},
					{Name: "database-migrations", DisplayName: "database-migrations"},
				},
				BuiltIn: true,
			},
		},
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
		`data-prompt="Use backend rules."`,
		`data-agent-skill-picker`,
		`data-skill-name="golang-pro"`,
		`data-skill-source="Skills.new"`,
		`data-agent-skill-hidden`,
		`Skills.new`,
		`Custom agents`,
		`Built-in agents`,
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
		`action="/settings/org/agents/alice"`,
		`action="/settings/org/agents/alice/delete"`,
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
	if !strings.Contains(body, "Enable SX in Integrations before creating agents.") {
		t.Fatalf("agents tab missing SX enable hint")
	}
}
