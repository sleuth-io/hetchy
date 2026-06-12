package apikeys

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// fakeQuerier is a test double that implements the querier interface.
type fakeQuerier struct {
	createFn func(ctx context.Context, arg sqlc.CreateOrgAPIKeyParams) (sqlc.OrgApiKey, error)
	listFn   func(ctx context.Context, orgID string) ([]sqlc.OrgApiKey, error)
	getFn    func(ctx context.Context, keyHash []byte) (sqlc.OrgApiKey, error)
	touchFn  func(ctx context.Context, id string) error
	revokeFn func(ctx context.Context, arg sqlc.RevokeOrgAPIKeyParams) (int64, error)
}

func (f *fakeQuerier) CreateOrgAPIKey(ctx context.Context, arg sqlc.CreateOrgAPIKeyParams) (sqlc.OrgApiKey, error) {
	return f.createFn(ctx, arg)
}

func (f *fakeQuerier) ListOrgAPIKeys(ctx context.Context, orgID string) ([]sqlc.OrgApiKey, error) {
	return f.listFn(ctx, orgID)
}

func (f *fakeQuerier) GetOrgAPIKeyByHash(ctx context.Context, keyHash []byte) (sqlc.OrgApiKey, error) {
	return f.getFn(ctx, keyHash)
}

func (f *fakeQuerier) TouchOrgAPIKeyLastUsed(ctx context.Context, id string) error {
	return f.touchFn(ctx, id)
}

func (f *fakeQuerier) RevokeOrgAPIKey(ctx context.Context, arg sqlc.RevokeOrgAPIKeyParams) (int64, error) {
	return f.revokeFn(ctx, arg)
}

// newFakeStore returns a Store backed by a fakeQuerier with the given setup.
func newFakeStore(q *fakeQuerier) *Store {
	return &Store{q: q}
}

// ---- Enabled ----------------------------------------------------------------

func TestEnabled(t *testing.T) {
	if got := (*Store)(nil).Enabled(); got {
		t.Error("nil Store.Enabled() = true, want false")
	}
	if got := (&Store{}).Enabled(); got {
		t.Error("Store{}.Enabled() = true, want false")
	}
	s := newFakeStore(&fakeQuerier{})
	if !s.Enabled() {
		t.Error("Store with querier.Enabled() = false, want true")
	}
}

func TestNewNilDB(t *testing.T) {
	s := New(nil)
	if s == nil {
		t.Fatal("New(nil) returned nil, want non-nil Store")
	}
	if s.Enabled() {
		t.Error("New(nil).Enabled() = true, want false")
	}
}

// ---- Create (disabled) ------------------------------------------------------

func TestCreateNotEnabled(t *testing.T) {
	s := &Store{}
	_, err := s.Create(t.Context(), "org1", "my key", "user1")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Create on disabled store: error = %v, want ErrNotConfigured", err)
	}
}

// ---- Create (validation) ----------------------------------------------------

func TestCreateValidation(t *testing.T) {
	q := &fakeQuerier{}
	s := newFakeStore(q)

	cases := []struct {
		name      string
		orgID     string
		keyName   string
		wantErrIs string
	}{
		{"empty orgID", "", "my key", "org id is required"},
		{"whitespace orgID", "  ", "my key", "org id is required"},
		{"empty name", "org1", "", "name is required"},
		{"whitespace name", "org1", "   ", "name is required"},
		{"name too long", "org1", strings.Repeat("a", 121), "name must be 120 characters or fewer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Create(t.Context(), tc.orgID, tc.keyName, "user1")
			if err == nil || !strings.Contains(err.Error(), tc.wantErrIs) {
				t.Errorf("Create(%q, %q): error = %v, want message containing %q", tc.orgID, tc.keyName, err, tc.wantErrIs)
			}
		})
	}
}

// ---- Create (success) -------------------------------------------------------

func TestCreateSuccess(t *testing.T) {
	var capturedArg sqlc.CreateOrgAPIKeyParams
	q := &fakeQuerier{
		createFn: func(_ context.Context, arg sqlc.CreateOrgAPIKeyParams) (sqlc.OrgApiKey, error) {
			capturedArg = arg
			return sqlc.OrgApiKey{
				ID:    arg.ID,
				OrgID: arg.OrgID,
				Name:  arg.Name,
			}, nil
		},
	}
	s := newFakeStore(q)

	created, err := s.Create(t.Context(), "org1", "CI key", "user1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Token == "" {
		t.Error("created.Token is empty")
	}
	if !strings.HasPrefix(created.Token, keyPrefix) {
		t.Errorf("Token %q does not have prefix %q", created.Token, keyPrefix)
	}
	if capturedArg.OrgID != "org1" {
		t.Errorf("CreateOrgAPIKey called with OrgID=%q, want org1", capturedArg.OrgID)
	}
	if capturedArg.Name != "CI key" {
		t.Errorf("CreateOrgAPIKey called with Name=%q, want 'CI key'", capturedArg.Name)
	}
	if capturedArg.KeyPrefix != tokenDisplayPrefix(created.Token) {
		t.Errorf("KeyPrefix mismatch: got %q, want %q", capturedArg.KeyPrefix, tokenDisplayPrefix(created.Token))
	}
	if created.ID == "" {
		t.Error("returned key ID is empty")
	}
}

func TestCreateTrimsMspace(t *testing.T) {
	q := &fakeQuerier{
		createFn: func(_ context.Context, arg sqlc.CreateOrgAPIKeyParams) (sqlc.OrgApiKey, error) {
			return sqlc.OrgApiKey{ID: arg.ID, OrgID: arg.OrgID, Name: arg.Name}, nil
		},
	}
	s := newFakeStore(q)

	created, err := s.Create(t.Context(), "  org2  ", "  deploy key  ", "")
	if err != nil {
		t.Fatalf("Create with spaces: %v", err)
	}
	if created.OrgID != "org2" {
		t.Errorf("OrgID = %q, want org2 (spaces trimmed)", created.OrgID)
	}
	if created.Name != "deploy key" {
		t.Errorf("Name = %q, want 'deploy key' (spaces trimmed)", created.Name)
	}
}

func TestCreateNameExactly120Runes(t *testing.T) {
	q := &fakeQuerier{
		createFn: func(_ context.Context, arg sqlc.CreateOrgAPIKeyParams) (sqlc.OrgApiKey, error) {
			return sqlc.OrgApiKey{ID: arg.ID, OrgID: arg.OrgID, Name: arg.Name}, nil
		},
	}
	s := newFakeStore(q)

	name120 := strings.Repeat("x", 120)
	_, err := s.Create(t.Context(), "org1", name120, "user1")
	if err != nil {
		t.Errorf("Create(name=120 runes): unexpected error %v", err)
	}
}

func TestCreateDBError(t *testing.T) {
	dbErr := errors.New("connection refused")
	q := &fakeQuerier{
		createFn: func(_ context.Context, _ sqlc.CreateOrgAPIKeyParams) (sqlc.OrgApiKey, error) {
			return sqlc.OrgApiKey{}, dbErr
		},
	}
	s := newFakeStore(q)

	_, err := s.Create(t.Context(), "org1", "key", "user1")
	if err == nil {
		t.Fatal("Create: expected error on DB failure, got nil")
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("Create DB error wrapping: got %v, want to wrap %v", err, dbErr)
	}
}

// ---- List -------------------------------------------------------------------

func TestListNotEnabled(t *testing.T) {
	s := &Store{}
	keys, err := s.List(t.Context(), "org1")
	if err != nil {
		t.Errorf("List on disabled store: unexpected error %v", err)
	}
	if keys != nil {
		t.Errorf("List on disabled store: expected nil slice, got %v", keys)
	}
}

func TestListSuccess(t *testing.T) {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	q := &fakeQuerier{
		listFn: func(_ context.Context, orgID string) ([]sqlc.OrgApiKey, error) {
			if orgID != "org1" {
				return nil, nil
			}
			return []sqlc.OrgApiKey{
				{ID: "ak_1", OrgID: "org1", Name: "CI", KeyPrefix: "hetchy_abcde", CreatedAt: now},
				{ID: "ak_2", OrgID: "org1", Name: "Deploy", CreatedAt: now},
			}, nil
		},
	}
	s := newFakeStore(q)

	keys, err := s.List(t.Context(), "org1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List returned %d keys, want 2", len(keys))
	}
	if keys[0].ID != "ak_1" || keys[1].ID != "ak_2" {
		t.Errorf("List returned unexpected IDs: %v", keys)
	}
}

func TestListEmpty(t *testing.T) {
	q := &fakeQuerier{
		listFn: func(_ context.Context, _ string) ([]sqlc.OrgApiKey, error) {
			return nil, nil
		},
	}
	s := newFakeStore(q)

	keys, err := s.List(t.Context(), "org1")
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List(empty) = %d keys, want 0", len(keys))
	}
}

func TestListDBError(t *testing.T) {
	dbErr := errors.New("timeout")
	q := &fakeQuerier{
		listFn: func(_ context.Context, _ string) ([]sqlc.OrgApiKey, error) {
			return nil, dbErr
		},
	}
	s := newFakeStore(q)

	_, err := s.List(t.Context(), "org1")
	if err == nil {
		t.Fatal("List: expected error on DB failure, got nil")
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("List DB error wrapping: got %v, want to wrap %v", err, dbErr)
	}
}

// ---- Authenticate -----------------------------------------------------------

func TestAuthenticateNotEnabled(t *testing.T) {
	s := &Store{}
	_, _, err := s.Authenticate(t.Context(), "hetchy_sometoken")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Authenticate on disabled store: error = %v, want ErrNotConfigured", err)
	}
}

func TestAuthenticateWrongPrefix(t *testing.T) {
	q := &fakeQuerier{}
	s := newFakeStore(q)

	k, ok, err := s.Authenticate(t.Context(), "sk-not-a-hetchy-key")
	if err != nil {
		t.Errorf("Authenticate(wrong prefix): unexpected error %v", err)
	}
	if ok {
		t.Error("Authenticate(wrong prefix): ok = true, want false")
	}
	if k != (Key{}) {
		t.Errorf("Authenticate(wrong prefix): key = %+v, want zero", k)
	}
}

func TestAuthenticateWhitespaceOnlyToken(t *testing.T) {
	q := &fakeQuerier{}
	s := newFakeStore(q)

	_, ok, err := s.Authenticate(t.Context(), "   ")
	if err != nil {
		t.Errorf("Authenticate(whitespace): unexpected error %v", err)
	}
	if ok {
		t.Error("Authenticate(whitespace): ok = true, want false")
	}
}

func TestAuthenticateNotFound(t *testing.T) {
	q := &fakeQuerier{
		getFn: func(_ context.Context, _ []byte) (sqlc.OrgApiKey, error) {
			return sqlc.OrgApiKey{}, pgx.ErrNoRows
		},
	}
	s := newFakeStore(q)

	_, ok, err := s.Authenticate(t.Context(), keyPrefix+"validtokenxxxxxxxxxxxxxxxx")
	if err != nil {
		t.Errorf("Authenticate(not found): unexpected error %v", err)
	}
	if ok {
		t.Error("Authenticate(not found): ok = true, want false")
	}
}

func TestAuthenticateSuccess(t *testing.T) {
	token := keyPrefix + "realtokenxxxxxxxxxxxxxxxxx"
	q := &fakeQuerier{
		getFn: func(_ context.Context, hash []byte) (sqlc.OrgApiKey, error) {
			return sqlc.OrgApiKey{ID: "ak_abc", OrgID: "org1", Name: "prod"}, nil
		},
	}
	s := newFakeStore(q)

	k, ok, err := s.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatalf("Authenticate(success): %v", err)
	}
	if !ok {
		t.Error("Authenticate(success): ok = false, want true")
	}
	if k.ID != "ak_abc" {
		t.Errorf("Authenticate(success): ID = %q, want ak_abc", k.ID)
	}
}

func TestAuthenticateDBError(t *testing.T) {
	dbErr := errors.New("db error")
	q := &fakeQuerier{
		getFn: func(_ context.Context, _ []byte) (sqlc.OrgApiKey, error) {
			return sqlc.OrgApiKey{}, dbErr
		},
	}
	s := newFakeStore(q)

	_, _, err := s.Authenticate(t.Context(), keyPrefix+"sometoken")
	if err == nil {
		t.Fatal("Authenticate: expected error on DB failure, got nil")
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("Authenticate DB error wrapping: got %v, want to wrap %v", err, dbErr)
	}
}

// ---- Touch ------------------------------------------------------------------

func TestTouchNotEnabled(t *testing.T) {
	s := &Store{}
	if err := s.Touch(t.Context(), "ak_1"); err != nil {
		t.Errorf("Touch on disabled store: unexpected error %v", err)
	}
}

func TestTouchEmptyID(t *testing.T) {
	called := false
	q := &fakeQuerier{
		touchFn: func(_ context.Context, _ string) error {
			called = true
			return nil
		},
	}
	s := newFakeStore(q)

	if err := s.Touch(t.Context(), ""); err != nil {
		t.Errorf("Touch(empty id): unexpected error %v", err)
	}
	if called {
		t.Error("Touch(empty id): querier should not be called")
	}

	if err := s.Touch(t.Context(), "   "); err != nil {
		t.Errorf("Touch(whitespace id): unexpected error %v", err)
	}
	if called {
		t.Error("Touch(whitespace id): querier should not be called")
	}
}

func TestTouchSuccess(t *testing.T) {
	var touched string
	q := &fakeQuerier{
		touchFn: func(_ context.Context, id string) error {
			touched = id
			return nil
		},
	}
	s := newFakeStore(q)

	if err := s.Touch(t.Context(), "ak_1"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if touched != "ak_1" {
		t.Errorf("Touch called querier with id=%q, want ak_1", touched)
	}
}

func TestTouchDBError(t *testing.T) {
	dbErr := errors.New("touch failed")
	q := &fakeQuerier{
		touchFn: func(_ context.Context, _ string) error { return dbErr },
	}
	s := newFakeStore(q)

	if err := s.Touch(t.Context(), "ak_1"); !errors.Is(err, dbErr) {
		t.Errorf("Touch DB error: got %v, want %v", err, dbErr)
	}
}

// ---- Revoke -----------------------------------------------------------------

func TestRevokeNotEnabled(t *testing.T) {
	s := &Store{}
	err := s.Revoke(t.Context(), "org1", "ak_1")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Revoke on disabled store: error = %v, want ErrNotConfigured", err)
	}
}

func TestRevokeNotFound(t *testing.T) {
	q := &fakeQuerier{
		revokeFn: func(_ context.Context, _ sqlc.RevokeOrgAPIKeyParams) (int64, error) {
			return 0, nil
		},
	}
	s := newFakeStore(q)

	err := s.Revoke(t.Context(), "org1", "ak_nonexistent")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Revoke(not found): error = %v, want pgx.ErrNoRows", err)
	}
}

func TestRevokeSuccess(t *testing.T) {
	var capturedArg sqlc.RevokeOrgAPIKeyParams
	q := &fakeQuerier{
		revokeFn: func(_ context.Context, arg sqlc.RevokeOrgAPIKeyParams) (int64, error) {
			capturedArg = arg
			return 1, nil
		},
	}
	s := newFakeStore(q)

	err := s.Revoke(t.Context(), "org1", "ak_1")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if capturedArg.OrgID != "org1" || capturedArg.ID != "ak_1" {
		t.Errorf("Revoke called with %+v, want {OrgID:org1 ID:ak_1}", capturedArg)
	}
}

func TestRevokeDBError(t *testing.T) {
	dbErr := errors.New("revoke failed")
	q := &fakeQuerier{
		revokeFn: func(_ context.Context, _ sqlc.RevokeOrgAPIKeyParams) (int64, error) {
			return 0, dbErr
		},
	}
	s := newFakeStore(q)

	err := s.Revoke(t.Context(), "org1", "ak_1")
	if !errors.Is(err, dbErr) {
		t.Errorf("Revoke DB error wrapping: got %v, want to wrap %v", err, dbErr)
	}
}

// ---- Pure helper functions --------------------------------------------------

func TestNewTokenFormat(t *testing.T) {
	tok, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if !strings.HasPrefix(tok, keyPrefix) {
		t.Errorf("newToken() = %q, want prefix %q", tok, keyPrefix)
	}
	// The base64 part should be 43 chars (32 raw bytes in base64 RawURL = ceil(32*4/3)).
	body := strings.TrimPrefix(tok, keyPrefix)
	if len(body) != 43 {
		t.Errorf("newToken body length = %d, want 43", len(body))
	}
}

func TestNewTokenUnique(t *testing.T) {
	seen := make(map[string]bool)
	for range 20 {
		tok, err := newToken()
		if err != nil {
			t.Fatalf("newToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("newToken returned duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

func TestNewIDFormat(t *testing.T) {
	id, err := newID()
	if err != nil {
		t.Fatalf("newID: %v", err)
	}
	if !strings.HasPrefix(id, "ak_") {
		t.Errorf("newID() = %q, want prefix ak_", id)
	}
	// 16 raw bytes in hex = 32 chars.
	hex32 := strings.TrimPrefix(id, "ak_")
	if len(hex32) != 32 {
		t.Errorf("newID hex part length = %d, want 32", len(hex32))
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := make(map[string]bool)
	for range 20 {
		id, err := newID()
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if seen[id] {
			t.Fatalf("newID returned duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestHashToken(t *testing.T) {
	h1 := hashToken("hetchy_abc")
	h2 := hashToken("hetchy_abc")
	h3 := hashToken("hetchy_xyz")
	if string(h1) != string(h2) {
		t.Error("hashToken is not deterministic")
	}
	if string(h1) == string(h3) {
		t.Error("hashToken: different inputs produced same hash")
	}
	if len(h1) != 32 {
		t.Errorf("hashToken length = %d, want 32 (SHA-256)", len(h1))
	}
}

func TestTokenDisplayPrefix(t *testing.T) {
	cases := []struct {
		token string
		want  string
	}{
		{"hetchy_short", "hetchy_short"},
		{"hetchy_exactly18chars!", "hetchy_exactly18ch"},
		{strings.Repeat("x", 100), strings.Repeat("x", 18)},
		{"", ""},
	}
	for _, tc := range cases {
		got := tokenDisplayPrefix(tc.token)
		if got != tc.want {
			t.Errorf("tokenDisplayPrefix(%q) = %q, want %q", tc.token, got, tc.want)
		}
	}
}

func TestKeyFromRow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	row := sqlc.OrgApiKey{
		ID:        "ak_abc",
		OrgID:     "org1",
		Name:      "test key",
		KeyPrefix: "hetchy_abc",
		CreatedBy: "user1",
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		LastUsedAt: pgtype.Timestamptz{
			Time:  now.Add(time.Minute),
			Valid: true,
		},
	}
	k := keyFromRow(row)
	if k.ID != "ak_abc" {
		t.Errorf("ID = %q, want ak_abc", k.ID)
	}
	if k.OrgID != "org1" {
		t.Errorf("OrgID = %q, want org1", k.OrgID)
	}
	if k.Name != "test key" {
		t.Errorf("Name = %q, want 'test key'", k.Name)
	}
	if k.Prefix != "hetchy_abc" {
		t.Errorf("Prefix = %q, want hetchy_abc", k.Prefix)
	}
	if k.CreatedBy != "user1" {
		t.Errorf("CreatedBy = %q, want user1", k.CreatedBy)
	}
	if !k.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", k.CreatedAt, now)
	}
	if !k.LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Errorf("LastUsedAt = %v, want %v", k.LastUsedAt, now.Add(time.Minute))
	}
}

func TestKeyFromRowInvalidTimestamps(t *testing.T) {
	row := sqlc.OrgApiKey{
		ID:         "ak_zero",
		CreatedAt:  pgtype.Timestamptz{Valid: false},
		LastUsedAt: pgtype.Timestamptz{Valid: false},
	}
	k := keyFromRow(row)
	if !k.CreatedAt.IsZero() {
		t.Errorf("CreatedAt: expected zero time, got %v", k.CreatedAt)
	}
	if !k.LastUsedAt.IsZero() {
		t.Errorf("LastUsedAt: expected zero time, got %v", k.LastUsedAt)
	}
}

// ---- displayPrefixLen constant ----------------------------------------------

func TestDisplayPrefixLen(t *testing.T) {
	// Verify the constant matches expected value so generated token prefixes
	// are appropriately long (not so short they're useless, not so long they
	// leak entropy).
	if displayPrefixLen != 18 {
		t.Errorf("displayPrefixLen = %d, want 18", displayPrefixLen)
	}
	// A freshly generated token must be longer than the prefix.
	tok, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if len(tok) <= displayPrefixLen {
		t.Errorf("newToken() length %d is not greater than displayPrefixLen %d", len(tok), displayPrefixLen)
	}
}
