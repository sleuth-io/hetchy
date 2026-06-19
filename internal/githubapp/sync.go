package githubapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// SyncResult summarises a single SyncInstallation run. The HTTP setup
// callback returns a friendly count to the redirected user; logs
// capture the full breakdown.
type SyncResult struct {
	InstallationID int64
	Repos          int
	Teams          int
	TeamMembers    int
	// Truncated is set by PAT syncs that hit the repo paging cap; App
	// syncs always page to the end.
	Truncated bool
}

// SyncInstallation refreshes the cached repos + (for org installs)
// teams + memberships for a single installation. Idempotent: safe to
// re-run on every webhook + on demand from the settings page.
//
// The DB is the source of truth for what the chat/picker UI sees, so
// items present locally but missing from GitHub on this run are
// pruned. Items new in GitHub are upserted, which also bumps
// last_synced_at.
//
// All DB writes happen inside one transaction so a crash, context
// cancellation, or a concurrent webhook for the same installation can
// never leave the cache in a half-applied state.
func (a *App) SyncInstallation(ctx context.Context, store *db.Store, installationID int64) (SyncResult, error) {
	if store == nil {
		return SyncResult{}, errors.New("githubapp: SyncInstallation requires a non-nil db store")
	}
	cli, err := a.ClientForInstallation(ctx, installationID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("client for installation %d: %w", installationID, err)
	}
	appCli, err := a.AppClient()
	if err != nil {
		return SyncResult{}, fmt.Errorf("app client: %w", err)
	}

	// GitHub round-trips happen outside the transaction so we don't hold a
	// DB connection open for tens of seconds on a slow Apps API; the tx
	// only owns the writes.
	repos, err := listInstallationRepos(ctx, cli)
	if err != nil {
		return SyncResult{}, fmt.Errorf("list repos: %w", err)
	}
	inst, _, err := appCli.Apps.GetInstallation(ctx, installationID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("get installation %d: %w", installationID, err)
	}
	var teams []*github.Team
	teamMembers := map[int64][]*github.User{}
	if inst.GetAccount().GetType() == "Organization" {
		orgLogin := inst.GetAccount().GetLogin()
		teams, err = listOrgTeams(ctx, cli, orgLogin)
		if err != nil {
			return SyncResult{}, fmt.Errorf("list teams: %w", err)
		}
		for _, t := range teams {
			members, err := listTeamMembers(ctx, cli, orgLogin, t.GetSlug())
			if err != nil {
				return SyncResult{}, fmt.Errorf("list members of %s: %w", t.GetSlug(), err)
			}
			teamMembers[t.GetID()] = members
		}
	}

	result := SyncResult{InstallationID: installationID, Repos: len(repos), Teams: len(teams)}
	err = store.WithTx(ctx, func(q *sqlc.Queries) error {
		keepRepoIDs := make([]int64, 0, len(repos))
		for _, r := range repos {
			if err := q.UpsertGithubRepo(ctx, sqlc.UpsertGithubRepoParams{
				InstallationID: installationID,
				RepoID:         r.GetID(),
				Owner:          r.GetOwner().GetLogin(),
				Name:           r.GetName(),
				DefaultBranch:  r.GetDefaultBranch(),
				Private:        r.GetPrivate(),
			}); err != nil {
				return fmt.Errorf("upsert repo %s: %w", r.GetFullName(), err)
			}
			keepRepoIDs = append(keepRepoIDs, r.GetID())
		}
		if err := q.DeleteGithubReposByInstallationExcept(ctx, sqlc.DeleteGithubReposByInstallationExceptParams{
			InstallationID: installationID,
			Column2:        keepRepoIDs,
		}); err != nil {
			return fmt.Errorf("prune repos: %w", err)
		}

		if len(teams) == 0 {
			return nil
		}

		keepTeamIDs := make([]int64, 0, len(teams))
		for _, t := range teams {
			var parentID *int64
			if p := t.GetParent(); p != nil {
				id := p.GetID()
				parentID = &id
			}
			if err := q.UpsertGithubTeam(ctx, sqlc.UpsertGithubTeamParams{
				InstallationID: installationID,
				TeamID:         t.GetID(),
				Slug:           t.GetSlug(),
				Name:           t.GetName(),
				ParentTeamID:   parentID,
			}); err != nil {
				return fmt.Errorf("upsert team %s: %w", t.GetSlug(), err)
			}
			keepTeamIDs = append(keepTeamIDs, t.GetID())

			// Replace strategy: wipe this team's members then re-insert.
			// Cleaner than diffing for the small per-team size we expect
			// (single-digit to low-hundred members).
			if err := q.DeleteGithubTeamMembersForTeam(ctx, sqlc.DeleteGithubTeamMembersForTeamParams{
				InstallationID: installationID,
				TeamID:         t.GetID(),
			}); err != nil {
				return fmt.Errorf("clear members of %s: %w", t.GetSlug(), err)
			}
			members := teamMembers[t.GetID()]
			for _, u := range members {
				if err := q.UpsertGithubTeamMember(ctx, sqlc.UpsertGithubTeamMemberParams{
					InstallationID: installationID,
					TeamID:         t.GetID(),
					GithubUserID:   u.GetID(),
					GithubLogin:    u.GetLogin(),
				}); err != nil {
					return fmt.Errorf("upsert member %s/%s: %w", t.GetSlug(), u.GetLogin(), err)
				}
			}
			result.TeamMembers += len(members)
		}
		// FK on github_team_members(installation_id, team_id) cascades,
		// so pruning the team here also drops its member rows.
		if err := q.DeleteGithubTeamsByInstallationExcept(ctx, sqlc.DeleteGithubTeamsByInstallationExceptParams{
			InstallationID: installationID,
			Column2:        keepTeamIDs,
		}); err != nil {
			return fmt.Errorf("prune teams: %w", err)
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
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
