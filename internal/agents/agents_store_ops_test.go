package agents

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

// ---- GetCustom --------------------------------------------------------------

func TestGetCustomWithDB(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: func(_ context.Context, arg sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error) {
			return sqlc.GetAgentProfileBySlugRow{Slug: arg.Slug, Enabled: true}, nil
		},
	}
	s := newFakeStore(q)
	p, err := s.GetCustom(t.Context(), "org1", "sally")
	if err != nil {
		t.Fatalf("GetCustom: %v", err)
	}
	if p.Slug != "sally" {
		t.Errorf("GetCustom slug = %q, want sally", p.Slug)
	}
}

func TestGetCustomWithDBDisabledReturnsNotFound(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: func(_ context.Context, _ sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error) {
			return sqlc.GetAgentProfileBySlugRow{Slug: "sally", Enabled: false}, nil
		},
	}
	s := newFakeStore(q)
	_, err := s.GetCustom(t.Context(), "org1", "sally")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCustom(disabled) error = %v, want ErrNotFound", err)
	}
}

func TestGetCustomWithDBNotFound(t *testing.T) {
	q := &fakeQuerier{
		countFn:     func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: nil,
	}
	s := newFakeStore(q)
	_, err := s.GetCustom(t.Context(), "org1", "unknown")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCustom(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestGetCustomNilQuerierReturnsNotFound(t *testing.T) {
	s := &Store{}
	_, err := s.GetCustom(t.Context(), "org1", "sally")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCustom nil querier error = %v, want ErrNotFound", err)
	}
}

func TestGetCustomEmptyOrgIDReturnsNotFound(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	_, err := s.GetCustom(t.Context(), "", "sally")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCustom(empty orgID) error = %v, want ErrNotFound", err)
	}
}

func TestGetCustomWithDBError(t *testing.T) {
	wantErr := errors.New("db error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		getBySlugFn: func(_ context.Context, _ sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error) {
			return sqlc.GetAgentProfileBySlugRow{}, wantErr
		},
	}
	s := newFakeStore(q)
	_, err := s.GetCustom(t.Context(), "org1", "sally")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetCustom error = %v, want %v", err, wantErr)
	}
}

// ---- UpdateName -------------------------------------------------------------

func TestUpdateNameWithDB(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		updateNameFn: func(_ context.Context, arg sqlc.UpdateAgentProfileNameParams) (sqlc.UpdateAgentProfileNameRow, error) {
			return sqlc.UpdateAgentProfileNameRow{
				Slug:        arg.Slug,
				DisplayName: arg.DisplayName,
			}, nil
		},
	}
	s := newFakeStore(q)
	p, err := s.UpdateName(t.Context(), "org1", "bob", "Robert")
	if err != nil {
		t.Fatalf("UpdateName: %v", err)
	}
	if p.DisplayName != "Robert" {
		t.Errorf("DisplayName = %q, want Robert", p.DisplayName)
	}
}

func TestUpdateNameNotFound(t *testing.T) {
	q := &fakeQuerier{
		countFn:      func(_ context.Context, _ string) (int64, error) { return 1, nil },
		updateNameFn: nil,
	}
	s := newFakeStore(q)
	_, err := s.UpdateName(t.Context(), "org1", "unknown", "New Name")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateName(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestUpdateNameEmptyDisplayName(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
	}
	s := newFakeStore(q)
	_, err := s.UpdateName(t.Context(), "org1", "bob", "   ")
	if err == nil {
		t.Fatal("UpdateName(empty display) expected error, got nil")
	}
}

func TestUpdateNameEmptyOrgID(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	_, err := s.UpdateName(t.Context(), "", "bob", "Robert")
	if err == nil {
		t.Fatal("UpdateName(empty orgID) expected error, got nil")
	}
}

func TestUpdateNameNilQuerierReturnsError(t *testing.T) {
	s := &Store{}
	_, err := s.UpdateName(t.Context(), "org1", "bob", "Robert")
	if err == nil {
		t.Fatal("UpdateName with nil querier expected error, got nil")
	}
}

func TestUpdateNameWithDBError(t *testing.T) {
	wantErr := errors.New("update error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		updateNameFn: func(_ context.Context, _ sqlc.UpdateAgentProfileNameParams) (sqlc.UpdateAgentProfileNameRow, error) {
			return sqlc.UpdateAgentProfileNameRow{}, wantErr
		},
	}
	s := newFakeStore(q)
	_, err := s.UpdateName(t.Context(), "org1", "bob", "Robert")
	if !errors.Is(err, wantErr) {
		t.Fatalf("UpdateName error = %v, want %v", err, wantErr)
	}
}

// ---- ListTemplates ----------------------------------------------------------

func TestListTemplatesWithDB(t *testing.T) {
	q := &fakeQuerier{
		listTmplFn: func(_ context.Context) ([]sqlc.AgentProfileTemplate, error) {
			return []sqlc.AgentProfileTemplate{
				{Slug: "bob", DisplayName: "Bob", Enabled: true},
				{Slug: "alice", DisplayName: "Alice", Enabled: true},
			}, nil
		},
	}
	s := newFakeStore(q)
	templates, err := s.ListTemplates(t.Context())
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(templates) != 2 {
		t.Fatalf("ListTemplates returned %d, want 2", len(templates))
	}
}

func TestListTemplatesWithDBError(t *testing.T) {
	wantErr := errors.New("list error")
	q := &fakeQuerier{
		listTmplFn: func(_ context.Context) ([]sqlc.AgentProfileTemplate, error) { return nil, wantErr },
	}
	s := newFakeStore(q)
	_, err := s.ListTemplates(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ListTemplates error = %v, want %v", err, wantErr)
	}
}

// ---- GetTemplate ------------------------------------------------------------

func TestGetTemplateWithDB(t *testing.T) {
	q := &fakeQuerier{
		getTmplFn: func(_ context.Context, slug string) (sqlc.AgentProfileTemplate, error) {
			if slug == "bob" {
				return sqlc.AgentProfileTemplate{Slug: "bob", DisplayName: "Bob", Enabled: true}, nil
			}
			return sqlc.AgentProfileTemplate{}, pgx.ErrNoRows
		},
	}
	s := newFakeStore(q)
	p, err := s.GetTemplate(t.Context(), "bob")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if p.Slug != "bob" {
		t.Errorf("GetTemplate slug = %q, want bob", p.Slug)
	}
	if !p.BuiltIn {
		t.Error("GetTemplate: BuiltIn should be true for template rows")
	}
	if p.TemplateSlug != "bob" {
		t.Errorf("GetTemplate TemplateSlug = %q, want bob", p.TemplateSlug)
	}
}

func TestGetTemplateWithDBNotFound(t *testing.T) {
	q := &fakeQuerier{getTmplFn: nil}
	s := newFakeStore(q)
	_, err := s.GetTemplate(t.Context(), "unknown")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTemplate(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestGetTemplateWithDBError(t *testing.T) {
	wantErr := errors.New("get error")
	q := &fakeQuerier{
		getTmplFn: func(_ context.Context, _ string) (sqlc.AgentProfileTemplate, error) {
			return sqlc.AgentProfileTemplate{}, wantErr
		},
	}
	s := newFakeStore(q)
	_, err := s.GetTemplate(t.Context(), "bob")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetTemplate error = %v, want %v", err, wantErr)
	}
}

// ---- UpdateVaultSync --------------------------------------------------------

func TestUpdateVaultSyncWithDB(t *testing.T) {
	q := &fakeQuerier{
		vaultSyncFn: func(_ context.Context, arg sqlc.UpdateAgentProfileVaultSyncParams) (sqlc.UpdateAgentProfileVaultSyncRow, error) {
			return sqlc.UpdateAgentProfileVaultSyncRow{
				Slug:         arg.Slug,
				VaultBackend: arg.VaultBackend,
				SyncStatus:   arg.SyncStatus,
			}, nil
		},
	}
	s := newFakeStore(q)
	p, err := s.UpdateVaultSync(t.Context(), "org1", "bob", "git@github.com:org/vault.git", "", "bob-tmpl", "synced", "")
	if err != nil {
		t.Fatalf("UpdateVaultSync: %v", err)
	}
	if p.VaultBackend != "git@github.com:org/vault.git" {
		t.Errorf("VaultBackend = %q", p.VaultBackend)
	}
	if p.SyncStatus != "synced" {
		t.Errorf("SyncStatus = %q, want synced", p.SyncStatus)
	}
}

func TestUpdateVaultSyncNotFound(t *testing.T) {
	q := &fakeQuerier{vaultSyncFn: nil}
	s := newFakeStore(q)
	_, err := s.UpdateVaultSync(t.Context(), "org1", "unknown", "", "", "", "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateVaultSync(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestUpdateVaultSyncNilQuerierReturnsError(t *testing.T) {
	s := &Store{}
	_, err := s.UpdateVaultSync(t.Context(), "org1", "bob", "", "", "", "", "")
	if err == nil {
		t.Fatal("UpdateVaultSync with nil querier expected error, got nil")
	}
}

func TestUpdateVaultSyncWithDBError(t *testing.T) {
	wantErr := errors.New("sync error")
	q := &fakeQuerier{
		vaultSyncFn: func(_ context.Context, _ sqlc.UpdateAgentProfileVaultSyncParams) (sqlc.UpdateAgentProfileVaultSyncRow, error) {
			return sqlc.UpdateAgentProfileVaultSyncRow{}, wantErr
		},
	}
	s := newFakeStore(q)
	_, err := s.UpdateVaultSync(t.Context(), "org1", "bob", "", "", "", "", "")
	if !errors.Is(err, wantErr) {
		t.Fatalf("UpdateVaultSync error = %v, want %v", err, wantErr)
	}
}

func TestUpdateVaultSyncWithBotKey(t *testing.T) {
	rawKey := strings.Repeat("e", 32)
	hexKey := hex.EncodeToString([]byte(rawKey))
	cipher, err := secrets.New(hexKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	var gotEncrypted []byte
	q := &fakeQuerier{
		vaultSyncFn: func(_ context.Context, arg sqlc.UpdateAgentProfileVaultSyncParams) (sqlc.UpdateAgentProfileVaultSyncRow, error) {
			gotEncrypted = arg.SxBotKeyEncrypted
			return sqlc.UpdateAgentProfileVaultSyncRow{
				Slug:              arg.Slug,
				SxBotKeyEncrypted: arg.SxBotKeyEncrypted,
			}, nil
		},
	}
	s := &Store{q: q, cipher: cipher}
	const botKey = "sx-vault-bot-key"
	p, err := s.UpdateVaultSync(t.Context(), "org1", "bob", "", botKey, "", "synced", "")
	if err != nil {
		t.Fatalf("UpdateVaultSync with botKey: %v", err)
	}
	if len(gotEncrypted) == 0 {
		t.Error("expected encrypted bot key to be passed to DB, got empty")
	}
	if p.SXBotKey != botKey {
		t.Errorf("SXBotKey = %q, want %q", p.SXBotKey, botKey)
	}
}

func TestUpdateVaultSyncNonEmptyBotKeyRequiresCipher(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	_, err := s.UpdateVaultSync(t.Context(), "org1", "bob", "", "some-key", "", "", "")
	if err == nil {
		t.Fatal("UpdateVaultSync with non-empty botKey and no cipher expected error, got nil")
	}
}

// ---- Delete -----------------------------------------------------------------

func TestDeleteWithDB(t *testing.T) {
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		disableFn: func(_ context.Context, arg sqlc.DisableAgentProfileParams) (int64, error) {
			if arg.OrgID == "org1" && arg.Slug == "bob" {
				return 1, nil
			}
			return 0, nil
		},
	}
	s := newFakeStore(q)
	if err := s.Delete(t.Context(), "org1", "bob"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	q := &fakeQuerier{
		countFn:   func(_ context.Context, _ string) (int64, error) { return 1, nil },
		disableFn: nil,
	}
	s := newFakeStore(q)
	err := s.Delete(t.Context(), "org1", "unknown")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestDeleteEmptyOrgID(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	err := s.Delete(t.Context(), "", "bob")
	if err == nil {
		t.Fatal("Delete(empty orgID) expected error, got nil")
	}
}

func TestDeleteNilQuerierReturnsError(t *testing.T) {
	s := &Store{}
	err := s.Delete(t.Context(), "org1", "bob")
	if err == nil {
		t.Fatal("Delete with nil querier expected error, got nil")
	}
}

func TestDeleteWithDBError(t *testing.T) {
	wantErr := errors.New("disable error")
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		disableFn: func(_ context.Context, _ sqlc.DisableAgentProfileParams) (int64, error) {
			return 0, wantErr
		},
	}
	s := newFakeStore(q)
	err := s.Delete(t.Context(), "org1", "bob")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Delete error = %v, want %v", err, wantErr)
	}
}

func TestDeleteNormalizesSlug(t *testing.T) {
	var gotSlug string
	q := &fakeQuerier{
		countFn: func(_ context.Context, _ string) (int64, error) { return 1, nil },
		disableFn: func(_ context.Context, arg sqlc.DisableAgentProfileParams) (int64, error) {
			gotSlug = arg.Slug
			return 1, nil
		},
	}
	s := newFakeStore(q)
	if err := s.Delete(t.Context(), "org1", "  BOB  "); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotSlug != "bob" {
		t.Errorf("DisableAgentProfile got slug = %q, want bob", gotSlug)
	}
}
