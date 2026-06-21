package agents

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// fakeQuerier is a test double for the querier interface.
type fakeQuerier struct {
	listFn       func(ctx context.Context, orgID string) ([]sqlc.ListAgentProfilesByOrgRow, error)
	getBySlugFn  func(ctx context.Context, arg sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error)
	upsertFn     func(ctx context.Context, arg sqlc.UpsertAgentProfileParams) (sqlc.UpsertAgentProfileRow, error)
	updateNameFn func(ctx context.Context, arg sqlc.UpdateAgentProfileNameParams) (sqlc.UpdateAgentProfileNameRow, error)
	vaultSyncFn  func(ctx context.Context, arg sqlc.UpdateAgentProfileVaultSyncParams) (sqlc.UpdateAgentProfileVaultSyncRow, error)
	disableFn    func(ctx context.Context, arg sqlc.DisableAgentProfileParams) (int64, error)
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
