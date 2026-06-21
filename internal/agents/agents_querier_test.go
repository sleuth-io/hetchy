package agents

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// ---- EnsureSeeded -----------------------------------------------------------

func TestEnsureSeededIsNoop(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, orgID string) (int64, error) {
			t.Fatalf("CountAgentProfilesByOrg should not be called")
			return 0, nil
		},
		seedFn: func(_ context.Context, _ string) error {
			t.Fatalf("SeedDefaultAgentProfilesForOrg should not be called")
			return nil
		},
	}
	s := newFakeStore(q)
	if err := s.EnsureSeeded(t.Context(), "org1"); err != nil {
		t.Fatalf("EnsureSeeded: %v", err)
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
