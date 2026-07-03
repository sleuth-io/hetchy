package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/jobs"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/sxsync"
	"github.com/sleuth-io/hetchy/internal/webui"
)

func TestSettingsTemplate_RendersJobsTabForAdmin(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_jobs", "OrgName": "Acme", "Email": "u@example.com", "Tab": "jobs",
		"IsAdmin": true,
		"Jobs": []settingsJobView{
			{
				ID:                  "job_123",
				Name:                "Dependency sweep",
				Definition:          "Check dependencies weekly.",
				AgentSlug:           "maintainer",
				AgentLabel:          "Maintainer",
				PrimaryRepository:   "acme/api",
				AdditionalRepos:     []string{"acme/web"},
				AdditionalReposText: "acme/web",
				CronSchedule:        "0 9 * * 1",
				ScheduleLabel:       "Weekly",
				Timezone:            "America/Los_Angeles",
				TimezoneLabel:       "Los Angeles time",
				Enabled:             true,
				NextRunAt:           "2026-06-08T16:00:00Z",
				NextRunLabel:        "Jun 8 at 9:00 AM",
				LastRunLabel:        "Jun 1 at 8:00 AM",
				LastError:           `agent setup exited before runtime: step "run-script" exit 1`,
				LastErrorSummary:    "Agent startup failed while running the agent (exit code 1).",
				LastErrorDetail:     `agent setup exited before runtime: step "run-script" exit 1`,
				LastExecutionStatus: "failed",
			},
		},
		"JobAgentOptions": []settingsJobAgentOption{
			{Slug: "maintainer", DisplayName: "Maintainer"},
		},
		"JobRepoOptions": []settingsJobRepoOption{
			{Slug: "acme/api"},
			{Slug: "acme/web"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/settings/org?tab=jobs" class="active"`,
		`data-job-new="1"`,
		`id="modal-job-edit"`,
		`id="job-job_123"`,
		`data-job-id="job_123"`,
		`data-job-edit="job_123"`,
		`data-job-run="job_123"`,
		`data-job-toggle="job_123"`,
		`data-job-delete="job_123"`,
		`Check dependencies weekly.`,
		`Additional repositories: acme/web`,
		`class="job-submeta"`,
		`<dt>Agent</dt><dd>Maintainer</dd>`,
		`<dt>Repo</dt><dd>acme/api</dd>`,
		`<strong>Weekly</strong>`,
		`<small>Los Angeles time</small>`,
		`Jun 8 at 9:00 AM`,
		`last run: Jun 1 at 8:00 AM`,
		`<span class="job-error-label">Last failure</span>`,
		`Agent startup failed while running the agent (exit code 1).`,
		`<summary>Details</summary>`,
		`agent setup exited before runtime: step &#34;run-script&#34; exit 1`,
		`<option value="maintainer">Maintainer</option>`,
		`id="job-primary-repo"`,
		`<option value="acme/api">acme/api</option>`,
		`data-job-additional-select`,
		`data-job-additional-chips`,
		`type="hidden" name="additional_repositories" data-job-field="additional_repositories"`,
		`id="job-schedule-preset"`,
		`<option value="0 * * * *">Every hour</option>`,
		`<option value="0 */4 * * *">Every 4 hours</option>`,
		`<option value="0 */6 * * *">Every 6 hours</option>`,
		`<option value="0 */8 * * *">Every 8 hours</option>`,
		`<option value="0 */12 * * *">Every 12 hours</option>`,
		`<option value="0 9 * * *">Daily</option>`,
		`<option value="0 9 * * 1">Weekly</option>`,
		`<option value="advanced">Advanced</option>`,
		`data-job-cron-row hidden`,
		`name="cron_schedule" data-job-field="cron_schedule" required value="0 * * * *"`,
		`src="/assets/settings_jobs.js`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("jobs tab missing %q", want)
		}
	}
	if strings.Contains(body, `data-open-modal="modal-job-edit"`) {
		t.Fatal("job create button should use the jobs-specific modal opener only")
	}
	for _, notWant := range []string{
		`id="job-enabled"`,
		`name="enabled"`,
		`list="job-repo-options"`,
		`id="job-repo-options"`,
		`<textarea id="job-additional-repos"`,
		`One repository per line`,
		`<div><span>Agent</span><strong>Maintainer</strong></div>`,
		`<div><span>Repository</span><strong>acme/api</strong></div>`,
	} {
		if strings.Contains(body, notWant) {
			t.Fatalf("jobs tab should not render %q", notWant)
		}
	}
}

func TestSettingsTemplate_RendersJobsTabReadOnlyForMembers(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_jobs", "OrgName": "Acme", "Email": "u@example.com", "Tab": "jobs",
		"IsAdmin": false,
		"Jobs": []settingsJobView{
			{
				ID:                "job_123",
				Name:              "Dependency sweep",
				Definition:        "Check dependencies weekly.",
				AgentLabel:        "Maintainer",
				PrimaryRepository: "acme/api",
				CronSchedule:      "0 9 * * 1",
				ScheduleLabel:     "Weekly",
				Timezone:          "UTC",
				Enabled:           false,
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`Only administrators can change jobs.`,
		`Dependency sweep`,
		`Check dependencies weekly.`,
		`disabled`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("member jobs tab missing %q", want)
		}
	}
	for _, notWant := range []string{
		`data-job-new="1"`,
		`id="modal-job-edit"`,
		`data-job-edit=`,
		`data-job-run=`,
		`data-job-toggle=`,
		`data-job-delete=`,
	} {
		if strings.Contains(body, notWant) {
			t.Fatalf("member jobs tab should not render %q", notWant)
		}
	}
}

func TestSettingsJobFromJobFormatsView(t *testing.T) {
	got := settingsJobFromJob(jobs.Job{
		ID:                  "job_123",
		Name:                "Dependency sweep",
		Definition:          "Check dependencies.",
		AgentSlug:           "maintainer",
		PrimaryOwner:        "acme",
		PrimaryRepo:         "api",
		AdditionalRepos:     []jobs.RepoRef{{Owner: "acme", Name: "web"}, {Owner: "", Name: "ignored"}},
		CronSchedule:        "0 9 * * 1",
		Timezone:            "America/Los_Angeles",
		Enabled:             true,
		NextRunAt:           time.Date(2026, 6, 8, 16, 0, 0, 0, time.UTC),
		LastRunAt:           time.Date(2026, 6, 1, 15, 0, 0, 0, time.UTC),
		LastRunID:           "run_123",
		LastError:           "failed once",
		LastExecutionStatus: jobs.StatusFailed,
	}, map[string]string{"maintainer": "Maintainer"})

	if got.ID != "job_123" || got.Name != "Dependency sweep" || got.Definition != "Check dependencies." {
		t.Fatalf("basic fields = %#v", got)
	}
	if got.AgentSlug != "maintainer" || got.AgentLabel != "Maintainer" {
		t.Fatalf("agent fields = %#v", got)
	}
	if got.PrimaryRepository != "acme/api" {
		t.Fatalf("PrimaryRepository = %q, want acme/api", got.PrimaryRepository)
	}
	if len(got.AdditionalRepos) != 1 || got.AdditionalRepos[0] != "acme/web" {
		t.Fatalf("AdditionalRepos = %#v, want acme/web", got.AdditionalRepos)
	}
	if got.AdditionalReposText != "acme/web" {
		t.Fatalf("AdditionalReposText = %q, want acme/web", got.AdditionalReposText)
	}
	if got.ScheduleLabel != "Weekly" || got.TimezoneLabel != "Los Angeles time" {
		t.Fatalf("labels = schedule %q timezone %q", got.ScheduleLabel, got.TimezoneLabel)
	}
	if !got.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if got.NextRunAt != "2026-06-08T16:00:00Z" || got.NextRunLabel != "Jun 8 at 9:00 AM" {
		t.Fatalf("next run = %q / %q", got.NextRunAt, got.NextRunLabel)
	}
	if got.LastRunAt != "2026-06-01T15:00:00Z" || got.LastRunLabel != "Jun 1 at 8:00 AM" {
		t.Fatalf("last run = %q / %q", got.LastRunAt, got.LastRunLabel)
	}
	if got.LastRunID != "run_123" || got.LastError != "failed once" || got.LastErrorSummary != "failed once" || got.LastErrorDetail != "" || got.LastExecutionStatus != jobs.StatusFailed {
		t.Fatalf("last result = %#v", got)
	}

	defaultAgent := settingsJobFromJob(jobs.Job{}, nil)
	if defaultAgent.AgentLabel != "Default" {
		t.Fatalf("default AgentLabel = %q, want Default", defaultAgent.AgentLabel)
	}
}

func TestJobAPIRequestToInputValidatesStructuredAdditionalRepos(t *testing.T) {
	repo := "acme/api"
	req := jobAPIRequest{
		Name:              ptrString("Dependency sweep"),
		Definition:        ptrString("Check dependencies."),
		PrimaryRepository: &repo,
		CronSchedule:      ptrString("0 9 * * 1"),
		Timezone:          ptrString("UTC"),
		AdditionalRepos: []jobs.RepoRef{
			{Owner: " acme ", Name: " web "},
		},
	}
	got, err := req.toInput(jobs.Job{})
	if err != nil {
		t.Fatalf("toInput: %v", err)
	}
	if len(got.AdditionalRepos) != 1 || got.AdditionalRepos[0].Slug() != "acme/web" {
		t.Fatalf("additional repos = %#v", got.AdditionalRepos)
	}

	req.AdditionalRepos = []jobs.RepoRef{{Owner: "", Name: "web"}}
	if _, err := req.toInput(jobs.Job{}); err == nil {
		t.Fatal("toInput accepted invalid structured additional repo")
	}
}

func TestJobAPIRequestToInputDefaultsAndPreservesEnabled(t *testing.T) {
	createInput, err := (jobAPIRequest{}).toInput(jobs.Job{})
	if err != nil {
		t.Fatalf("create toInput: %v", err)
	}
	if !createInput.Enabled {
		t.Fatal("create input without enabled should default to enabled")
	}

	editInput, err := (jobAPIRequest{}).toInput(jobs.Job{ID: "job_123", Enabled: false})
	if err != nil {
		t.Fatalf("edit toInput: %v", err)
	}
	if editInput.Enabled {
		t.Fatal("edit input without enabled should preserve disabled state")
	}
}

func TestJobAPIRequestToInputPreservesPartialEditFields(t *testing.T) {
	got, err := (jobAPIRequest{Name: ptrString("Renamed")}).toInput(jobs.Job{
		ID:           "job_123",
		Name:         "Old name",
		Definition:   "Keep definition",
		AgentSlug:    "agent-a",
		PrimaryOwner: "acme",
		PrimaryRepo:  "api",
		AdditionalRepos: []jobs.RepoRef{
			{Owner: "acme", Name: "web"},
		},
		CronSchedule: "0 9 * * *",
		Timezone:     "UTC",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("toInput: %v", err)
	}
	if got.Name != "Renamed" || got.Definition != "Keep definition" || got.AgentSlug != "agent-a" {
		t.Fatalf("text fields = %#v", got)
	}
	if got.PrimaryOwner != "acme" || got.PrimaryRepo != "api" {
		t.Fatalf("primary repo = %s/%s, want acme/api", got.PrimaryOwner, got.PrimaryRepo)
	}
	if len(got.AdditionalRepos) != 1 || got.AdditionalRepos[0].Slug() != "acme/web" {
		t.Fatalf("AdditionalRepos = %#v, want acme/web", got.AdditionalRepos)
	}
	if got.CronSchedule != "0 9 * * *" || got.Timezone != "UTC" || !got.Enabled {
		t.Fatalf("schedule/enabled fields = %#v", got)
	}
}

func TestSettingsJobsByAgentReturnsEmptyWhenJobsDisabled(t *testing.T) {
	got, err := (&Bot{}).settingsJobsByAgent(t.Context(), "org_123")
	if err != nil {
		t.Fatalf("settingsJobsByAgent: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("disabled jobs map = %#v, want empty", got)
	}

	b := &Bot{jobs: jobs.NewStore(nil, nil)}
	got, err = b.settingsJobsByAgent(t.Context(), "org_123")
	if err != nil {
		t.Fatalf("settingsJobsByAgent disabled concrete store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("disabled concrete jobs map = %#v, want empty", got)
	}
}

func TestRemoteAgentDisplayNamesMergesVaultRename(t *testing.T) {
	b := &Bot{sx: &fakeSXManager{remoteAgents: []agents.Profile{
		{Slug: "hetchy", DisplayName: "Skills.new Bot"},
		{Slug: "code-reviewer", DisplayName: "Code reviewer"},
		{Slug: "  ", DisplayName: "ignored blank slug"},
		{Slug: "blank-name", DisplayName: "   "},
	}}}

	got := b.remoteAgentDisplayNames(t.Context(), "org_123", sxsync.BackendSkillsNew)

	want := map[string]string{
		"hetchy":        "Skills.new Bot",
		"code-reviewer": "Code reviewer",
	}
	if len(got) != len(want) {
		t.Fatalf("remoteAgentDisplayNames = %#v, want %#v", got, want)
	}
	for slug, name := range want {
		if got[slug] != name {
			t.Fatalf("remoteAgentDisplayNames[%q] = %q, want %q", slug, got[slug], name)
		}
	}
}

func TestRemoteAgentDisplayNamesEmptyWhenUnavailable(t *testing.T) {
	cases := []struct {
		name    string
		bot     *Bot
		backend string
	}{
		{name: "sx disabled", bot: &Bot{sx: &fakeSXManager{remoteAgents: []agents.Profile{{Slug: "hetchy", DisplayName: "Skills.new Bot"}}}}, backend: ""},
		{name: "nil sx manager", bot: &Bot{}, backend: sxsync.BackendSkillsNew},
		{name: "sync error falls back", bot: &Bot{sx: &fakeSXManager{syncAgentsErr: errors.New("vault unavailable")}}, backend: sxsync.BackendSkillsNew},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.bot.remoteAgentDisplayNames(t.Context(), "org_123", tc.backend)
			if len(got) != 0 {
				t.Fatalf("remoteAgentDisplayNames = %#v, want empty", got)
			}
		})
	}
}

func TestBuildJobAgentOptions(t *testing.T) {
	profiles := []agents.Profile{
		// Enabled built-in: kept, remote-name override ignored for built-ins.
		{Slug: "hetchy", DisplayName: "Hetchy", Enabled: true, BuiltIn: true},
		// Enabled custom agent: remote vault name overrides the local name.
		{Slug: "reviewer", DisplayName: "Local reviewer", Enabled: true},
		// Enabled custom agent with no vault override keeps its local display name.
		{Slug: "plain", DisplayName: "Plain Agent", Enabled: true},
		// Enabled custom agent with a blank display name falls back to slug.
		{Slug: "namer", DisplayName: "", Enabled: true},
		// Disabled agent is dropped.
		{Slug: "disabled", DisplayName: "Disabled", Enabled: false},
		// Enabled but bound to a different sx backend: dropped.
		{Slug: "other-backend", DisplayName: "Other", Enabled: true, VaultBackend: "other"},
		// Enabled but blank slug: dropped (would collide with the Default entry).
		{Slug: "", DisplayName: "Blank", Enabled: true},
	}
	remoteNames := map[string]string{
		"reviewer": "Vault reviewer",
		"hetchy":   "Vault hetchy (ignored for built-ins)",
	}

	labels, options := buildJobAgentOptions(profiles, sxsync.BackendSkillsNew, remoteNames)

	wantLabels := map[string]string{
		"":         "Default",
		"hetchy":   "Hetchy",
		"reviewer": "Vault reviewer",
		"plain":    "Plain Agent",
		"namer":    "namer",
	}
	if len(labels) != len(wantLabels) {
		t.Fatalf("labels = %#v, want %#v", labels, wantLabels)
	}
	for slug, name := range wantLabels {
		if labels[slug] != name {
			t.Fatalf("labels[%q] = %q, want %q", slug, labels[slug], name)
		}
	}

	wantOptions := []settingsJobAgentOption{
		{Slug: "", DisplayName: "Default"},
		{Slug: "hetchy", DisplayName: "Hetchy"},
		{Slug: "reviewer", DisplayName: "Vault reviewer"},
		{Slug: "plain", DisplayName: "Plain Agent"},
		{Slug: "namer", DisplayName: "namer"},
	}
	if len(options) != len(wantOptions) {
		t.Fatalf("options = %#v, want %#v", options, wantOptions)
	}
	for i, want := range wantOptions {
		if options[i] != want {
			t.Fatalf("options[%d] = %#v, want %#v", i, options[i], want)
		}
	}
}

func TestBuildJobAgentOptionsEmptyProfiles(t *testing.T) {
	labels, options := buildJobAgentOptions(nil, "", nil)
	if len(labels) != 1 || labels[""] != "Default" {
		t.Fatalf("labels = %#v, want only Default", labels)
	}
	if len(options) != 1 || options[0] != (settingsJobAgentOption{Slug: "", DisplayName: "Default"}) {
		t.Fatalf("options = %#v, want only Default", options)
	}
}

func TestSettingsJobsByAgentGroupsRows(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		listRows: []jobs.Job{
			{ID: "job_a", Name: "Alpha", AgentSlug: "reviewer"},
			{ID: "job_b", Name: "Bravo", AgentSlug: "reviewer"},
			{ID: "job_c", Name: "Charlie", AgentSlug: ""},
		},
	}
	got, err := settingsJobsByAgent(t.Context(), "org_123", store)
	if err != nil {
		t.Fatalf("settingsJobsByAgent: %v", err)
	}
	if len(got["reviewer"]) != 2 {
		t.Fatalf("reviewer jobs = %#v, want 2", got["reviewer"])
	}
	if got["reviewer"][0].ID != "job_a" || got["reviewer"][1].ID != "job_b" {
		t.Fatalf("reviewer job order = %#v", got["reviewer"])
	}
	if len(got[""]) != 1 || got[""][0].ID != "job_c" {
		t.Fatalf("default-agent jobs = %#v, want job_c", got[""])
	}
}

func TestSettingsJobsByAgentPropagatesListError(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true, listErr: errors.New("db down")}
	if _, err := settingsJobsByAgent(t.Context(), "org_123", store); err == nil {
		t.Fatal("settingsJobsByAgent should propagate list error")
	}

	// A nil store and a disabled store both degrade to an empty map.
	for _, s := range []jobListerStore{nil, &fakeJobAPIStore{enabled: false}} {
		got, err := settingsJobsByAgent(t.Context(), "org_123", s)
		if err != nil {
			t.Fatalf("settingsJobsByAgent(disabled): %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("disabled jobs map = %#v, want empty", got)
		}
	}
}

func TestPopulateJobsSettingsTabData(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		listRows: []jobs.Job{
			{ID: "job_a", Name: "Alpha", AgentSlug: "reviewer"},
		},
	}
	populate := func(_ context.Context, _ string, data map[string]any) (map[string]string, error) {
		data["JobAgentOptions"] = []settingsJobAgentOption{{Slug: "reviewer", DisplayName: "Reviewer"}}
		return map[string]string{"reviewer": "Reviewer"}, nil
	}

	data := map[string]any{}
	if err := populateJobsSettingsTabData(t.Context(), "org_123", data, store, populate); err != nil {
		t.Fatalf("populateJobsSettingsTabData: %v", err)
	}
	jobsView, ok := data["Jobs"].([]settingsJobView)
	if !ok || len(jobsView) != 1 {
		t.Fatalf("Jobs = %#v, want one view", data["Jobs"])
	}
	if jobsView[0].ID != "job_a" || jobsView[0].AgentLabel != "Reviewer" {
		t.Fatalf("job view = %#v", jobsView[0])
	}
	if data["JobAgentOptions"] == nil {
		t.Fatal("modal populator output should be merged into data")
	}
}

func TestPopulateJobsSettingsTabDataDisabledStore(t *testing.T) {
	populate := func(_ context.Context, _ string, _ map[string]any) (map[string]string, error) {
		return map[string]string{}, nil
	}
	data := map[string]any{}
	// A disabled store yields an empty (non-nil) Jobs slice without calling List.
	if err := populateJobsSettingsTabData(t.Context(), "org_123", data, &fakeJobAPIStore{enabled: false}, populate); err != nil {
		t.Fatalf("populateJobsSettingsTabData(disabled): %v", err)
	}
	jobsView, ok := data["Jobs"].([]settingsJobView)
	if !ok || len(jobsView) != 0 {
		t.Fatalf("Jobs = %#v, want empty slice", data["Jobs"])
	}
}

func TestPopulateJobsSettingsTabDataPropagatesErrors(t *testing.T) {
	okPopulate := func(_ context.Context, _ string, _ map[string]any) (map[string]string, error) {
		return map[string]string{}, nil
	}
	// List error surfaces.
	listErrStore := &fakeJobAPIStore{enabled: true, listErr: errors.New("db down")}
	if err := populateJobsSettingsTabData(t.Context(), "org_123", map[string]any{}, listErrStore, okPopulate); err == nil {
		t.Fatal("expected list error to propagate")
	}

	// Modal-populator error surfaces even when the job list loads cleanly.
	modalErr := func(_ context.Context, _ string, _ map[string]any) (map[string]string, error) {
		return nil, errors.New("modal boom")
	}
	if err := populateJobsSettingsTabData(t.Context(), "org_123", map[string]any{}, &fakeJobAPIStore{enabled: true}, modalErr); err == nil {
		t.Fatal("expected modal populator error to propagate")
	}
}

// TestBotPopulateJobsSettingsTabDataWiresModalData exercises the (*Bot)
// wrapper end-to-end on a bypass bot with no jobs/agents/repository backends.
// It covers the typed-nil b.jobs delegation path (Enabled() nil-safety) and
// the real populateJobModalData/jobAgentOptions wiring, which the free-function
// tests intentionally stub out.
func TestBotPopulateJobsSettingsTabDataWiresModalData(t *testing.T) {
	b := newBypassBot(t)
	data := map[string]any{}
	if err := b.populateJobsSettingsTabData(t.Context(), "org_test", data); err != nil {
		t.Fatalf("populateJobsSettingsTabData: %v", err)
	}
	jobsView, ok := data["Jobs"].([]settingsJobView)
	if !ok || len(jobsView) != 0 {
		t.Fatalf("Jobs = %#v, want empty slice", data["Jobs"])
	}
	for _, key := range []string{"JobAgentOptions", "JobRepoOptions", "JobModelOptions"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("modal data missing %q", key)
		}
	}
	// With no agents store, the only agent option is the Default entry.
	agentOptions, ok := data["JobAgentOptions"].([]settingsJobAgentOption)
	if !ok || len(agentOptions) != 1 || agentOptions[0].Slug != "" {
		t.Fatalf("JobAgentOptions = %#v, want only Default", data["JobAgentOptions"])
	}
}

func TestOrgHasOpenAICredentials(t *testing.T) {
	cases := []struct {
		name string
		bot  *Bot
		want bool
	}{
		{name: "nil org store", bot: &Bot{}, want: false},
		{name: "lookup error", bot: &Bot{orgs: &fakeOrgStore{getErr: errors.New("boom")}}, want: false},
		{name: "no credentials", bot: &Bot{orgs: &fakeOrgStore{getConfig: orgcfg.Config{}}}, want: false},
		{name: "api key set", bot: &Bot{orgs: &fakeOrgStore{getConfig: orgcfg.Config{OpenAIAPIKey: "sk-test"}}}, want: true},
		{name: "codex oauth set", bot: &Bot{orgs: &fakeOrgStore{getConfig: orgcfg.Config{OpenAICodexOAuthToken: "oauth"}}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.bot.orgHasOpenAICredentials(t.Context(), "org_123"); got != tc.want {
				t.Fatalf("orgHasOpenAICredentials = %v, want %v", got, tc.want)
			}
		})
	}
}

func ptrString(s string) *string { return &s }
