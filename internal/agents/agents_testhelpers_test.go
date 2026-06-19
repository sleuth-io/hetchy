package agents

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// fakeQuerier is a test double for the querier interface.
type fakeQuerier struct {
	countFn      func(ctx context.Context, orgID string) (int64, error)
	seedFn       func(ctx context.Context, orgID string) error
	listFn       func(ctx context.Context, orgID string) ([]sqlc.ListAgentProfilesByOrgRow, error)
	getBySlugFn  func(ctx context.Context, arg sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error)
	upsertFn     func(ctx context.Context, arg sqlc.UpsertAgentProfileParams) (sqlc.UpsertAgentProfileRow, error)
	updateNameFn func(ctx context.Context, arg sqlc.UpdateAgentProfileNameParams) (sqlc.UpdateAgentProfileNameRow, error)
	listTmplFn   func(ctx context.Context) ([]sqlc.AgentProfileTemplate, error)
	getTmplFn    func(ctx context.Context, slug string) (sqlc.AgentProfileTemplate, error)
	vaultSyncFn  func(ctx context.Context, arg sqlc.UpdateAgentProfileVaultSyncParams) (sqlc.UpdateAgentProfileVaultSyncRow, error)
	disableFn    func(ctx context.Context, arg sqlc.DisableAgentProfileParams) (int64, error)
}

func (f *fakeQuerier) CountAgentProfilesByOrg(ctx context.Context, orgID string) (int64, error) {
	if f.countFn == nil {
		return 0, nil
	}
	return f.countFn(ctx, orgID)
}

func (f *fakeQuerier) SeedDefaultAgentProfilesForOrg(ctx context.Context, orgID string) error {
	if f.seedFn == nil {
		return nil
	}
	return f.seedFn(ctx, orgID)
}

func (f *fakeQuerier) ListAgentProfilesByOrg(ctx context.Context, orgID string) ([]sqlc.ListAgentProfilesByOrgRow, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, orgID)
}

func (f *fakeQuerier) GetAgentProfileBySlug(ctx context.Context, arg sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error) {
	if f.getBySlugFn == nil {
		return sqlc.GetAgentProfileBySlugRow{}, pgx.ErrNoRows
	}
	return f.getBySlugFn(ctx, arg)
}

func (f *fakeQuerier) UpsertAgentProfile(ctx context.Context, arg sqlc.UpsertAgentProfileParams) (sqlc.UpsertAgentProfileRow, error) {
	if f.upsertFn == nil {
		return sqlc.UpsertAgentProfileRow{}, nil
	}
	return f.upsertFn(ctx, arg)
}

func (f *fakeQuerier) UpdateAgentProfileName(ctx context.Context, arg sqlc.UpdateAgentProfileNameParams) (sqlc.UpdateAgentProfileNameRow, error) {
	if f.updateNameFn == nil {
		return sqlc.UpdateAgentProfileNameRow{}, pgx.ErrNoRows
	}
	return f.updateNameFn(ctx, arg)
}

func (f *fakeQuerier) ListAgentProfileTemplates(ctx context.Context) ([]sqlc.AgentProfileTemplate, error) {
	if f.listTmplFn == nil {
		return nil, nil
	}
	return f.listTmplFn(ctx)
}

func (f *fakeQuerier) GetAgentProfileTemplate(ctx context.Context, slug string) (sqlc.AgentProfileTemplate, error) {
	if f.getTmplFn == nil {
		return sqlc.AgentProfileTemplate{}, pgx.ErrNoRows
	}
	return f.getTmplFn(ctx, slug)
}

func (f *fakeQuerier) UpdateAgentProfileVaultSync(ctx context.Context, arg sqlc.UpdateAgentProfileVaultSyncParams) (sqlc.UpdateAgentProfileVaultSyncRow, error) {
	if f.vaultSyncFn == nil {
		return sqlc.UpdateAgentProfileVaultSyncRow{}, pgx.ErrNoRows
	}
	return f.vaultSyncFn(ctx, arg)
}

func (f *fakeQuerier) DisableAgentProfile(ctx context.Context, arg sqlc.DisableAgentProfileParams) (int64, error) {
	if f.disableFn == nil {
		return 0, nil
	}
	return f.disableFn(ctx, arg)
}

func newFakeStore(q *fakeQuerier) *Store {
	return &Store{q: q}
}
