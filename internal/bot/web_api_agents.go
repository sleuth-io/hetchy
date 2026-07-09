package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/jobs"
	"github.com/sleuth-io/hetchy/internal/sxsync"
)

type agentSummary struct {
	Slug         string            `json:"slug"`
	DisplayName  string            `json:"display_name"`
	Description  string            `json:"description"`
	SXBot        string            `json:"sx_bot,omitempty"`
	PersonaAsset string            `json:"persona_asset,omitempty"`
	SlackAliases []string          `json:"slack_aliases,omitempty"`
	Skills       []string          `json:"skills,omitempty"`
	SXTeams      []string          `json:"sx_teams,omitempty"`
	SXSkills     []string          `json:"sx_skills,omitempty"`
	Jobs         []agentJobSummary `json:"jobs,omitempty"`
	VaultBackend string            `json:"vault_backend,omitempty"`
	SyncStatus   string            `json:"sync_status,omitempty"`
	SyncError    string            `json:"sync_error,omitempty"`
	BuiltIn      bool              `json:"built_in"`
	CatalogOnly  bool              `json:"catalog_only,omitempty"`
	Default      bool              `json:"default"`
}

type agentJobSummary struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Definition          string   `json:"definition,omitempty"`
	PrimaryRepository   string   `json:"primary_repository"`
	AdditionalRepos     []string `json:"additional_repositories,omitempty"`
	CronSchedule        string   `json:"cron_schedule"`
	ScheduleLabel       string   `json:"schedule_label"`
	Timezone            string   `json:"timezone"`
	TimezoneLabel       string   `json:"timezone_label"`
	Enabled             bool     `json:"enabled"`
	NextRunAt           string   `json:"next_run_at,omitempty"`
	NextRunLabel        string   `json:"next_run_label,omitempty"`
	LastRunAt           string   `json:"last_run_at,omitempty"`
	LastRunLabel        string   `json:"last_run_label,omitempty"`
	LastExecutionStatus string   `json:"last_execution_status,omitempty"`
	LastError           string   `json:"last_error,omitempty"`
	LastErrorSummary    string   `json:"last_error_summary,omitempty"`
}

func (b *Bot) agentsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	if r.Method == http.MethodPost {
		if !isAdmin(p) {
			http.Error(w, "admin required", http.StatusForbidden)
			return
		}
		if err := requireSameOrigin(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var body struct {
			Slug          string   `json:"slug"`
			DisplayName   string   `json:"display_name"`
			Description   string   `json:"description"`
			SXBot         string   `json:"sx_bot"`
			PersonaAsset  string   `json:"persona_asset"`
			PersonaPrompt string   `json:"persona_prompt"`
			SlackAliases  []string `json:"slack_aliases"`
			Skills        []string `json:"skills"`
			Enabled       *bool    `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		input := agents.Profile{
			Slug:          body.Slug,
			DisplayName:   body.DisplayName,
			Description:   body.Description,
			SXBot:         body.SXBot,
			PersonaAsset:  body.PersonaAsset,
			PersonaPrompt: body.PersonaPrompt,
			SlackAliases:  body.SlackAliases,
			Skills:        body.Skills,
			Enabled:       enabled,
		}
		var profile agents.Profile
		var err error
		if b.sx != nil {
			profile, err = b.sx.SaveAgent(r.Context(), p.OrgID, sxsync.Actor{Name: p.Email, Email: p.Email}, input, "")
			if errors.Is(err, sxsync.ErrNotConfigured) {
				profile, err = store.Upsert(r.Context(), p.OrgID, input)
			}
		} else {
			profile, err = store.Upsert(r.Context(), p.OrgID, input)
		}
		if err != nil {
			b.log.Warn("upsert agent", "error", err, "org", p.OrgID)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, agentSummary{
			Slug:         profile.Slug,
			DisplayName:  profile.DisplayName,
			Description:  profile.Description,
			SXBot:        profile.SXBot,
			PersonaAsset: profile.PersonaAsset,
			SlackAliases: profile.SlackAliases,
			Skills:       profile.Skills,
			SXTeams:      profile.SXTeams,
			SXSkills:     profile.SXSkills,
			VaultBackend: profile.VaultBackend,
			SyncStatus:   profile.SyncStatus,
			SyncError:    profile.SyncError,
			BuiltIn:      profile.BuiltIn,
			Default:      profile.Slug == agents.DefaultSlug,
		})
		return
	}
	remoteProfiles := []agents.Profile{}
	if b.sx != nil {
		var err error
		remoteProfiles, err = b.sx.SyncAgents(r.Context(), p.OrgID, sxActor(p))
		if errors.Is(err, sxsync.ErrNotConfigured) {
			remoteProfiles = nil
		} else if err != nil && b.log != nil {
			b.log.Warn("sync sx agents", "error", err, "org", p.OrgID)
			remoteProfiles = nil
		}
	}
	activeBackend, err := b.activeSXBackend(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("load sx integration", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	profiles, err := store.List(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list agents", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	profiles = mergeRemoteAgentProfiles(profiles, remoteProfiles)
	jobsByAgent, err := b.agentJobSummariesByAgent(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list agent jobs", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	includeCatalog := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("scope")), "all")
	writeJSON(w, buildAgentSummaries(profiles, remoteProfiles, activeBackend, jobsByAgent, includeCatalog))
}

// buildAgentSummaries assembles the /api/v1/agents response from the merged
// local+remote profiles. Disabled agents and agents bound to a different sx
// backend are dropped; remote vault state is overlaid onto matching locals;
// each visible agent gets its scheduled jobs attached. When includeCatalog is
// set, built-in catalog agents not already visible are appended as
// catalog-only entries. Kept as a pure function so the filtering/overlay/
// catalog logic is testable without a live store or sx vault.
func buildAgentSummaries(profiles, remoteProfiles []agents.Profile, activeBackend string, jobsByAgent map[string][]agentJobSummary, includeCatalog bool) []agentSummary {
	remoteBySlug := make(map[string]agents.Profile, len(remoteProfiles))
	for _, remote := range remoteProfiles {
		slug := agents.NormalizeSlug(remote.Slug)
		if slug != "" {
			remoteBySlug[slug] = remote
		}
	}
	out := make([]agentSummary, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, a := range profiles {
		if !a.Enabled {
			continue
		}
		if remote, ok := remoteBySlug[a.Slug]; ok {
			a = overlayAgentRemoteState(a, remote)
		}
		if !agentAvailableForActiveSXBackend(a, activeBackend) {
			continue
		}
		seen[a.Slug] = struct{}{}
		out = append(out, agentSummaryFromProfile(a, jobsByAgent[a.Slug], false))
	}
	if includeCatalog {
		for _, a := range agents.CatalogProfiles() {
			if _, ok := seen[a.Slug]; ok {
				continue
			}
			out = append(out, agentSummaryFromProfile(a, nil, true))
		}
	}
	return out
}

func mergeRemoteAgentProfiles(local, remote []agents.Profile) []agents.Profile {
	if len(remote) == 0 {
		return local
	}
	out := make([]agents.Profile, 0, len(local)+len(remote))
	seen := make(map[string]struct{}, len(local)+len(remote))
	for _, profile := range local {
		slug := agents.NormalizeSlug(profile.Slug)
		if slug == "" {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, profile)
	}
	for _, profile := range remote {
		slug := agents.NormalizeSlug(profile.Slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		profile.Slug = slug
		if strings.TrimSpace(profile.DisplayName) == "" {
			profile.DisplayName = slug
		}
		profile.Enabled = true
		seen[slug] = struct{}{}
		out = append(out, profile)
	}
	return out
}

func agentSummaryFromProfile(a agents.Profile, jobs []agentJobSummary, catalogOnly bool) agentSummary {
	return agentSummary{
		Slug:         a.Slug,
		DisplayName:  a.DisplayName,
		Description:  a.Description,
		SXBot:        a.SXBot,
		PersonaAsset: a.PersonaAsset,
		SlackAliases: a.SlackAliases,
		Skills:       a.Skills,
		SXTeams:      a.SXTeams,
		SXSkills:     a.SXSkills,
		Jobs:         jobs,
		VaultBackend: a.VaultBackend,
		SyncStatus:   a.SyncStatus,
		SyncError:    a.SyncError,
		BuiltIn:      a.BuiltIn,
		CatalogOnly:  catalogOnly,
		Default:      a.Slug == agents.DefaultSlug,
	}
}

func (b *Bot) agentJobSummariesByAgent(ctx context.Context, orgID string) (map[string][]agentJobSummary, error) {
	return agentJobSummariesByAgent(ctx, orgID, b.jobs)
}

// agentJobSummariesByAgent groups an org's scheduled jobs by agent slug for
// the /api/v1/agents response. Taking the narrow jobListerStore (mirroring the
// Jobs settings tab seam) lets the grouping and the disabled/error paths be
// exercised with an in-memory fake instead of a live Postgres.
func agentJobSummariesByAgent(ctx context.Context, orgID string, store jobListerStore) (map[string][]agentJobSummary, error) {
	out := map[string][]agentJobSummary{}
	if store == nil || !store.Enabled() {
		return out, nil
	}
	rows, err := store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, job := range rows {
		out[job.AgentSlug] = append(out[job.AgentSlug], agentJobSummaryFromJob(job))
	}
	return out, nil
}

func agentJobSummaryFromJob(job jobs.Job) agentJobSummary {
	additional := make([]string, 0, len(job.AdditionalRepos))
	for _, repo := range job.AdditionalRepos {
		if slug := repo.Slug(); slug != "" {
			additional = append(additional, slug)
		}
	}
	lastErrorSummary, _ := jobLastErrorView(job.LastError)
	return agentJobSummary{
		ID:                  job.ID,
		Name:                job.Name,
		Definition:          job.Definition,
		PrimaryRepository:   job.PrimaryOwner + "/" + job.PrimaryRepo,
		AdditionalRepos:     additional,
		CronSchedule:        job.CronSchedule,
		ScheduleLabel:       jobScheduleLabel(job.CronSchedule),
		Timezone:            job.Timezone,
		TimezoneLabel:       jobTimezoneLabel(job.Timezone),
		Enabled:             job.Enabled,
		NextRunAt:           formatSettingsTime(job.NextRunAt),
		NextRunLabel:        jobDisplayTime(job.NextRunAt, job.Timezone),
		LastRunAt:           formatSettingsTime(job.LastRunAt),
		LastRunLabel:        jobDisplayTime(job.LastRunAt, job.Timezone),
		LastExecutionStatus: job.LastExecutionStatus,
		LastError:           job.LastError,
		LastErrorSummary:    lastErrorSummary,
	}
}

func overlayAgentRemoteState(local, remote agents.Profile) agents.Profile {
	local.Skills = remote.Skills
	local.SXTeams = append([]string(nil), remote.SXTeams...)
	local.SXSkills = append([]string(nil), remote.SXSkills...)
	if strings.TrimSpace(remote.DisplayName) != "" {
		local.DisplayName = remote.DisplayName
	}
	if strings.TrimSpace(remote.Description) != "" {
		local.Description = remote.Description
	}
	if strings.TrimSpace(remote.SXBot) != "" {
		local.SXBot = remote.SXBot
	}
	if strings.TrimSpace(remote.PersonaAsset) != "" {
		local.PersonaAsset = remote.PersonaAsset
	}
	if strings.TrimSpace(remote.VaultBackend) != "" {
		local.VaultBackend = remote.VaultBackend
	}
	return local
}
