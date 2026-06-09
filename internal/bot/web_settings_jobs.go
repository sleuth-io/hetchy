package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/jobs"
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
	Enabled             bool
	NextRunAt           string
	NextRunLabel        string
	LastRunAt           string
	LastRunLabel        string
	LastRunID           string
	LastError           string
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
	agentLabels, agentOptions, err := b.jobAgentOptions(ctx, orgID)
	if err != nil {
		return err
	}
	_, repos, err := b.loadIntegrationsView(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load repositories: %w", err)
	}
	repoOptions := make([]settingsJobRepoOption, 0, len(repos))
	for _, repo := range repos {
		repoOptions = append(repoOptions, settingsJobRepoOption{Slug: repo.Owner + "/" + repo.Name})
	}
	out := make([]settingsJobView, 0, len(jobRows))
	for _, job := range jobRows {
		out = append(out, settingsJobFromJob(job, agentLabels))
	}
	data["Jobs"] = out
	data["JobAgentOptions"] = agentOptions
	data["JobRepoOptions"] = repoOptions
	return nil
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
	// Custom agents live in the org's SX vault; the local agent_profiles row can
	// lag behind a rename made through the vault, so the vault copy is the source
	// of truth for the visible name. The agents settings screen already applies
	// this merge (see populateAgentSettingsTabData); mirror it here so the jobs
	// dropdown and job cards do not display a stale pre-rename name.
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
		Enabled:             job.Enabled,
		NextRunAt:           formatSettingsTime(job.NextRunAt),
		NextRunLabel:        jobDisplayTime(job.NextRunAt, job.Timezone),
		LastRunAt:           formatSettingsTime(job.LastRunAt),
		LastRunLabel:        jobDisplayTime(job.LastRunAt, job.Timezone),
		LastRunID:           job.LastRunID,
		LastError:           job.LastError,
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
