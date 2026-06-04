package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/jobs"
	"github.com/hetchyhq/hetchy/internal/webui"
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
				LastExecutionStatus: "succeeded",
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

func ptrString(s string) *string { return &s }
