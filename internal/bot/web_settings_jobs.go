package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/jobs"
)

type settingsJobView struct {
	ID                  string
	Name                string
	Definition          string
	AgentSlug           string
	AgentLabel          string
	PrimaryRepository   string
	AdditionalRepos     []string
	AdditionalReposText string
	CronSchedule        string
	ScheduleLabel       string
	Timezone            string
	TimezoneLabel       string
	Model               string
	ModelLabel          string
	Enabled             bool
	NextRunAt           string
	NextRunLabel        string
	LastRunAt           string
	LastRunLabel        string
	LastRunID           string
	LastError           string
	LastErrorSummary    string
	LastErrorDetail     string
	LastExecutionStatus string
}

type settingsJobAgentOption struct {
	Slug        string
	DisplayName string
}

type settingsJobRepoOption struct {
	Slug string
}

func (b *Bot) populateJobsSettingsTabData(ctx context.Context, orgID string, data map[string]any) error {
	jobRows := []jobs.Job{}
	if b.jobs != nil && b.jobs.Enabled() {
		rows, err := b.jobs.List(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load jobs: %w", err)
		}
		jobRows = rows
	}
	agentLabels, err := b.populateJobModalData(ctx, orgID, data)
	if err != nil {
		return err
	}
	out := make([]settingsJobView, 0, len(jobRows))
	for _, job := range jobRows {
		out = append(out, settingsJobFromJob(job, agentLabels))
	}
	data["Jobs"] = out
	return nil
}

// populateJobModalData fills the shared job-edit modal's select options
// (agents, repositories, models) into data. Both the settings Jobs tab
// and the chat page mount the same modal, so both call this. It returns
// the agent-label map so the settings tab can also render each job card.
func (b *Bot) populateJobModalData(ctx context.Context, orgID string, data map[string]any) (map[string]string, error) {
	agentLabels, agentOptions, err := b.jobAgentOptions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	repoOptions := []settingsJobRepoOption{}
	// The repository list comes from the GitHub installations store. It is
	// absent in lightweight render paths/tests, so degrade to no repo
	// options rather than panicking.
	if b.store != nil && b.store.Queries != nil {
		_, repos, err := b.loadIntegrationsView(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("load repositories: %w", err)
		}
		for _, repo := range repos {
			repoOptions = append(repoOptions, settingsJobRepoOption{Slug: repo.Owner + "/" + repo.Name})
		}
	}
	data["JobAgentOptions"] = agentOptions
	data["JobRepoOptions"] = repoOptions
	data["JobModelOptions"] = jobModelOptions(b.orgHasOpenAICredentials(ctx, orgID))
	return agentLabels, nil
}

// orgHasOpenAICredentials reports whether the org has wired up OpenAI
// Codex, gating the GPT block in the job modal's model picker the same
// way the chat composer gates it. Errors are treated as "not enabled"
// so a transient orgcfg hiccup never drops the Anthropic options.
func (b *Bot) orgHasOpenAICredentials(ctx context.Context, orgID string) bool {
	if b.orgs == nil {
		return false
	}
	cfg, err := b.orgs.Get(ctx, orgID)
	if err != nil {
		return false
	}
	return cfg.OpenAIAPIKey != "" || cfg.OpenAICodexOAuthToken != ""
}

func (b *Bot) jobAgentOptions(ctx context.Context, orgID string) (map[string]string, []settingsJobAgentOption, error) {
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	activeBackend, err := b.activeSXBackend(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("load sx integration: %w", err)
	}
	profiles, err := store.List(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("load agents: %w", err)
	}
	// Vault names override stale local names, matching the agents settings screen.
	remoteNames := b.remoteAgentDisplayNames(ctx, orgID, activeBackend)
	labels := map[string]string{"": "Default"}
	options := []settingsJobAgentOption{{Slug: "", DisplayName: "Default"}}
	for _, profile := range profiles {
		if !profile.Enabled || !agentAvailableForActiveSXBackend(profile, activeBackend) {
			continue
		}
		if profile.Slug == "" {
			continue
		}
		label := profile.DisplayName
		if !profile.BuiltIn {
			if remoteName, ok := remoteNames[profile.Slug]; ok {
				label = remoteName
			}
		}
		if label == "" {
			label = profile.Slug
		}
		labels[profile.Slug] = label
		options = append(options, settingsJobAgentOption{Slug: profile.Slug, DisplayName: label})
	}
	return labels, options, nil
}

func settingsJobFromJob(job jobs.Job, agentLabels map[string]string) settingsJobView {
	additional := make([]string, 0, len(job.AdditionalRepos))
	for _, repo := range job.AdditionalRepos {
		if slug := repo.Slug(); slug != "" {
			additional = append(additional, slug)
		}
	}
	label := agentLabels[job.AgentSlug]
	if label == "" {
		label = job.AgentSlug
	}
	if label == "" {
		label = "Default"
	}
	lastErrorSummary, lastErrorDetail := jobLastErrorView(job.LastError)
	return settingsJobView{
		ID:                  job.ID,
		Name:                job.Name,
		Definition:          job.Definition,
		AgentSlug:           job.AgentSlug,
		AgentLabel:          label,
		PrimaryRepository:   job.PrimaryOwner + "/" + job.PrimaryRepo,
		AdditionalRepos:     additional,
		AdditionalReposText: strings.Join(additional, "\n"),
		CronSchedule:        job.CronSchedule,
		ScheduleLabel:       jobScheduleLabel(job.CronSchedule),
		Timezone:            job.Timezone,
		TimezoneLabel:       jobTimezoneLabel(job.Timezone),
		Model:               job.Model,
		ModelLabel:          jobModelLabel(job.Model),
		Enabled:             job.Enabled,
		NextRunAt:           formatSettingsTime(job.NextRunAt),
		NextRunLabel:        jobDisplayTime(job.NextRunAt, job.Timezone),
		LastRunAt:           formatSettingsTime(job.LastRunAt),
		LastRunLabel:        jobDisplayTime(job.LastRunAt, job.Timezone),
		LastRunID:           job.LastRunID,
		LastError:           job.LastError,
		LastErrorSummary:    lastErrorSummary,
		LastErrorDetail:     lastErrorDetail,
		LastExecutionStatus: job.LastExecutionStatus,
	}
}

func (b *Bot) settingsJobsByAgent(ctx context.Context, orgID string) (map[string][]settingsJobView, error) {
	out := map[string][]settingsJobView{}
	if b.jobs == nil || !b.jobs.Enabled() {
		return out, nil
	}
	rows, err := b.jobs.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("load agent jobs: %w", err)
	}
	for _, job := range rows {
		out[job.AgentSlug] = append(out[job.AgentSlug], settingsJobFromJob(job, nil))
	}
	return out, nil
}
