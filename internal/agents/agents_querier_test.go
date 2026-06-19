package agents

import (
	"context"
	"errors"
	"testing"

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

// ---- EnsureSeeded -----------------------------------------------------------

func TestEnsureSeededSkipsWhenProfilesExist(t *testing.T) {
	seeded := false
	q := &fakeQuerier{
		countFn: func(_ context.Context, orgID string) (int64, error) {
			return 3, nil
		},
		seedFn: func(_ context.Context, _ string) error {
			seeded = true
			return nil
		},
	}
	s := newFakeStore(q)
	if err := s.EnsureSeeded(t.Context(), "org1"); err != nil {
		t.Fatalf("EnsureSeeded: %v", err)
	}
	if seeded {
		t.Error("SeedDefaultAgentProfilesForOrg should not have been called when count > 0")
	}
}

func TestEnsureSeededSeedsWhenEmpty(t *testing.T) {
	seeded := false
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) {
			return 0, nil
		},
		seedFn: func(_ context.Context, orgID string) error {
			seeded = true
			if orgID != "org1" {
				return errors.New("unexpected orgID: " + orgID)
			}
			return nil
		},
	}
	s := newFakeStore(q)
	if err := s.EnsureSeeded(t.Context(), "org1"); err != nil {
		t.Fatalf("EnsureSeeded: %v", err)
	}
	if !seeded {
		t.Error("SeedDefaultAgentProfilesForOrg should have been called when count == 0")
	}
}

func TestEnsureSeededCountError(t *testing.T) {
	wantErr := errors.New("db error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) {
			return 0, wantErr
		},
	}
	s := newFakeStore(q)
	err := s.EnsureSeeded(t.Context(), "org1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureSeeded error = %v, want %v", err, wantErr)
	}
}

func TestEnsureSeededSeedError(t *testing.T) {
	wantErr := errors.New("seed error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 0, nil },
		seedFn:  func(_ context.Context, _ string) error { return wantErr },
	}
	s := newFakeStore(q)
	err := s.EnsureSeeded(t.Context(), "org1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureSeeded error = %v, want %v", err, wantErr)
	}
}

func TestEnsureSeededNilQuerier(t *testing.T) {
	s := &Store{}
	if err := s.EnsureSeeded(t.Context(), "org1"); err != nil {
		t.Fatalf("EnsureSeeded with nil querier: %v", err)
	}
}

// ---- List -------------------------------------------------------------------

func TestListWithDB(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		listFn: func(_ context.Context, orgID string) ([]sqlc.ListAgentProfilesByOrgRow, error) {
			return []sqlc.ListAgentProfilesByOrgRow{
				{Slug: "bob", DisplayName: "Bob", Enabled: true},
				{Slug: "disabled-agent", DisplayName: "Disabled", Enabled: false},
			}, nil
		},
	}
	s := newFakeStore(q)
	profiles, err := s.List(t.Context(), "org1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("List returned %d profiles, want 1 (disabled filtered out)", len(profiles))
	}
	if profiles[0].Slug != "bob" {
		t.Errorf("profiles[0].Slug = %q, want bob", profiles[0].Slug)
	}
}

func TestListWithDBError(t *testing.T) {
	wantErr := errors.New("list error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		listFn: func(_ context.Context, _ string) ([]sqlc.ListAgentProfilesByOrgRow, error) {
			return nil, wantErr
		},
	}
	s := newFakeStore(q)
	_, err := s.List(t.Context(), "org1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("List error = %v, want %v", err, wantErr)
	}
}

// ---- GetBySlug --------------------------------------------------------------

func TestGetBySlugWithDB(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: func(_ context.Context, arg sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error) {
			if arg.Slug == "bob" && arg.OrgID == "org1" {
				return sqlc.GetAgentProfileBySlugRow{Slug: "bob", DisplayName: "Bob", Enabled: true}, nil
			}
			return sqlc.GetAgentProfileBySlugRow{}, pgx.ErrNoRows
		},
	}
	s := newFakeStore(q)
	p, err := s.GetBySlug(t.Context(), "org1", "bob")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if p.Slug != "bob" {
		t.Errorf("Slug = %q, want bob", p.Slug)
	}
}

func TestGetBySlugWithDBDisabledReturnsNotFound(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: func(_ context.Context, _ sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error) {
			return sqlc.GetAgentProfileBySlugRow{Slug: "bob", Enabled: false}, nil
		},
	}
	s := newFakeStore(q)
	_, err := s.GetBySlug(t.Context(), "org1", "bob")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBySlug(disabled) error = %v, want ErrNotFound", err)
	}
}

func TestGetBySlugWithDBNotFound(t *testing.T) {
	q := &fakeQuerier{
		countFn:     func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: nil,
	}
	s := newFakeStore(q)
	_, err := s.GetBySlug(t.Context(), "org1", "unknown")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBySlug(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestGetBySlugWithDBError(t *testing.T) {
	wantErr := errors.New("db error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: func(_ context.Context, _ sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error) {
			return sqlc.GetAgentProfileBySlugRow{}, wantErr
		},
	}
	s := newFakeStore(q)
	_, err := s.GetBySlug(t.Context(), "org1", "bob")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetBySlug error = %v, want %v", err, wantErr)
	}
}

// ---- Upsert -----------------------------------------------------------------

func TestUpsertWithDB(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		upsertFn: func(_ context.Context, arg sqlc.UpsertAgentProfileParams) (sqlc.UpsertAgentProfileRow, error) {
			return sqlc.UpsertAgentProfileRow{
				Slug:        arg.Slug,
				DisplayName: arg.DisplayName,
				Enabled:     arg.Enabled,
			}, nil
		},
	}
	s := newFakeStore(q)
	p, err := s.Upsert(t.Context(), "org1", Profile{
		Slug:        "sally",
		DisplayName: "Sally",
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if p.Slug != "sally" {
		t.Errorf("Upsert slug = %q, want sally", p.Slug)
	}
}

func TestUpsertFallsBackToSlugWhenDisplayNameEmpty(t *testing.T) {
	var gotDisplayName string
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		upsertFn: func(_ context.Context, arg sqlc.UpsertAgentProfileParams) (sqlc.UpsertAgentProfileRow, error) {
			gotDisplayName = arg.DisplayName
			return sqlc.UpsertAgentProfileRow{Slug: arg.Slug, DisplayName: arg.DisplayName}, nil
		},
	}
	s := newFakeStore(q)
	_, err := s.Upsert(t.Context(), "org1", Profile{Slug: "sally"})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if gotDisplayName != "sally" {
		t.Errorf("DisplayName = %q, want slug fallback sally", gotDisplayName)
	}
}

func TestUpsertEmptySlugReturnsError(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
	}
	s := newFakeStore(q)
	_, err := s.Upsert(t.Context(), "org1", Profile{Slug: ""})
	if err == nil {
		t.Fatal("Upsert(empty slug) expected error, got nil")
	}
}

func TestUpsertEmptyOrgIDReturnsError(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	_, err := s.Upsert(t.Context(), "", Profile{Slug: "sally"})
	if err == nil {
		t.Fatal("Upsert(empty orgID) expected error, got nil")
	}
}

func TestUpsertNilQuerierReturnsError(t *testing.T) {
	s := &Store{}
	_, err := s.Upsert(t.Context(), "org1", Profile{Slug: "sally"})
	if err == nil {
		t.Fatal("Upsert with nil querier expected error, got nil")
	}
}

func TestUpsertWithDBError(t *testing.T) {
	wantErr := errors.New("upsert error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		upsertFn: func(_ context.Context, _ sqlc.UpsertAgentProfileParams) (sqlc.UpsertAgentProfileRow, error) {
			return sqlc.UpsertAgentProfileRow{}, wantErr
		},
	}
	s := newFakeStore(q)
	_, err := s.Upsert(t.Context(), "org1", Profile{Slug: "sally"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Upsert error = %v, want %v", err, wantErr)
	}
}
