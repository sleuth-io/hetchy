package githubapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-github/v66/github"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// SyncResult summarises a single SyncInstallation run. The HTTP setup
// callback returns a friendly count to the redirected user; logs
// capture the full breakdown.
type SyncResult struct {
	InstallationID int64
	Repos          int
	Teams          int
	TeamMembers    int
}

// SyncInstallation refreshes the cached repos + (for org installs)
// teams + memberships for a single installation. Idempotent: safe to
// re-run on every webhook + on demand from the settings page.
//
// The DB is the source of truth for what the chat/picker UI sees, so
// items present locally but missing from GitHub on this run are
// pruned. Items new in GitHub are upserted, which also bumps
// last_synced_at.
func (a *App) SyncInstallation(ctx context.Context, store *db.Store, installationID int64) (SyncResult, error) {
	if store == nil {
		return SyncResult{}, errors.New("githubapp: SyncInstallation requires a non-nil db store")
	}
	cli, err := a.ClientForInstallation(ctx, installationID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("client for installation %d: %w", installationID, err)
	}

	repos, err := listInstallationRepos(ctx, cli)
	if err != nil {
		return SyncResult{}, fmt.Errorf("list repos: %w", err)
	}
	keepRepoIDs := make([]int64, 0, len(repos))
	for _, r := range repos {
		if err := store.Queries.UpsertGithubRepo(ctx, sqlc.UpsertGithubRepoParams{
			InstallationID: installationID,
			RepoID:         r.GetID(),
			Owner:          r.GetOwner().GetLogin(),
			Name:           r.GetName(),
			DefaultBranch:  r.GetDefaultBranch(),
			Private:        r.GetPrivate(),
		}); err != nil {
			return SyncResult{}, fmt.Errorf("upsert repo %s: %w", r.GetFullName(), err)
		}
		keepRepoIDs = append(keepRepoIDs, r.GetID())
	}
	if err := store.Queries.DeleteGithubReposByInstallationExcept(ctx, sqlc.DeleteGithubReposByInstallationExceptParams{
		InstallationID: installationID,
		Column2:        keepRepoIDs,
	}); err != nil {
		return SyncResult{}, fmt.Errorf("prune repos: %w", err)
	}

	result := SyncResult{InstallationID: installationID, Repos: len(repos)}

	// Teams + memberships only make sense for Organization installs.
	// Caller passes the installation row, but here we re-discover the
	// account type by inspecting the installation through the App
	// client — avoids requiring callers to thread it in and keeps the
	// sync entrypoint a single id.
	appCli, err := a.AppClient()
	if err != nil {
		return result, fmt.Errorf("app client: %w", err)
	}
	inst, _, err := appCli.Apps.GetInstallation(ctx, installationID)
	if err != nil {
		return result, fmt.Errorf("get installation %d: %w", installationID, err)
	}
	if inst.GetAccount().GetType() != "Organization" {
		return result, nil
	}
	orgLogin := inst.GetAccount().GetLogin()

	teams, err := listOrgTeams(ctx, cli, orgLogin)
	if err != nil {
		return result, fmt.Errorf("list teams: %w", err)
	}
	keepTeamIDs := make([]int64, 0, len(teams))
	for _, t := range teams {
		var parentID *int64
		if p := t.GetParent(); p != nil {
			id := p.GetID()
			parentID = &id
		}
		if err := store.Queries.UpsertGithubTeam(ctx, sqlc.UpsertGithubTeamParams{
			InstallationID: installationID,
			TeamID:         t.GetID(),
			Slug:           t.GetSlug(),
			Name:           t.GetName(),
			ParentTeamID:   parentID,
		}); err != nil {
			return result, fmt.Errorf("upsert team %s: %w", t.GetSlug(), err)
		}
		keepTeamIDs = append(keepTeamIDs, t.GetID())

		members, err := listTeamMembers(ctx, cli, orgLogin, t.GetSlug())
		if err != nil {
			return result, fmt.Errorf("list members of %s: %w", t.GetSlug(), err)
		}
		// Replace strategy: wipe this team's members then re-insert.
		// Cleaner than diffing for the small per-team size we expect
		// (single-digit to low-hundred members).
		if err := store.Queries.DeleteGithubTeamMembersForTeam(ctx, sqlc.DeleteGithubTeamMembersForTeamParams{
			InstallationID: installationID,
			TeamID:         t.GetID(),
		}); err != nil {
			return result, fmt.Errorf("clear members of %s: %w", t.GetSlug(), err)
		}
		for _, u := range members {
			if err := store.Queries.UpsertGithubTeamMember(ctx, sqlc.UpsertGithubTeamMemberParams{
				InstallationID: installationID,
				TeamID:         t.GetID(),
				GithubUserID:   u.GetID(),
				GithubLogin:    u.GetLogin(),
			}); err != nil {
				return result, fmt.Errorf("upsert member %s/%s: %w", t.GetSlug(), u.GetLogin(), err)
			}
		}
		result.TeamMembers += len(members)
	}
	if err := store.Queries.DeleteGithubTeamsByInstallationExcept(ctx, sqlc.DeleteGithubTeamsByInstallationExceptParams{
		InstallationID: installationID,
		Column2:        keepTeamIDs,
	}); err != nil {
		return result, fmt.Errorf("prune teams: %w", err)
	}
	result.Teams = len(teams)
	return result, nil
}

func listInstallationRepos(ctx context.Context, cli *github.Client) ([]*github.Repository, error) {
	var out []*github.Repository
	opts := &github.ListOptions{PerPage: 100}
	for {
		page, resp, err := cli.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Repositories...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func listOrgTeams(ctx context.Context, cli *github.Client, orgLogin string) ([]*github.Team, error) {
	var out []*github.Team
	opts := &github.ListOptions{PerPage: 100}
	for {
		page, resp, err := cli.Teams.ListTeams(ctx, orgLogin, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func listTeamMembers(ctx context.Context, cli *github.Client, orgLogin, teamSlug string) ([]*github.User, error) {
	var out []*github.User
	opts := &github.TeamListTeamMembersOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		page, resp, err := cli.Teams.ListTeamMembersBySlug(ctx, orgLogin, teamSlug, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}
