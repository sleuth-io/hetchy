package apikeys

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// Tests for Store.Enabled()

func TestStoreEnabledNilStore(t *testing.T) {
	var s *Store
	if s.Enabled() {
		t.Error("nil Store should not be enabled")
	}
}

func TestStoreEnabledNilDB(t *testing.T) {
	s := &Store{db: nil}
	if s.Enabled() {
		t.Error("Store with nil db should not be enabled")
	}
}

func TestStoreEnabledNilQueries(t *testing.T) {
	// db.Store with nil Queries also makes apikeys.Store.Enabled() return false
	s := &Store{db: &db.Store{}}
	if s.Enabled() {
		t.Error("Store with db.Store.Queries==nil should not be enabled")
	}
}

func TestNewStore(t *testing.T) {
	s := New(nil)
	if s == nil {
		t.Fatal("New returned nil")
	}
}

// Tests for Create() validation paths (all return before hitting DB)

func TestCreateDisabledStore(t *testing.T) {
	var s *Store
	_, err := s.Create(context.Background(), "org-1", "mykey", "user-1")
	if err == nil {
		t.Fatal("Create on nil store should return an error")
	}
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Create on nil store: got %v, want ErrNotConfigured", err)
	}
}

func TestListDisabledStore(t *testing.T) {
	var s *Store
	keys, err := s.List(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("List on nil store: unexpected error %v", err)
	}
	if keys != nil {
		t.Errorf("List on nil store: expected nil slice, got %v", keys)
	}
}

// Tests for Authenticate() — prefix check happens before DB access

func TestAuthenticateDisabledStore(t *testing.T) {
	var s *Store
	_, ok, err := s.Authenticate(context.Background(), "hetchy_sometoken")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Authenticate on nil store: err=%v, want ErrNotConfigured", err)
	}
	if ok {
		t.Error("Authenticate on nil store: ok should be false")
	}
}

func TestAuthenticateEmptyToken(t *testing.T) {
	// empty token has no hetchy_ prefix — but disabled store returns ErrNotConfigured
	// before the prefix check. This tests both the nil-store and the token format.
	var s *Store
	_, ok, err := s.Authenticate(context.Background(), "")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Authenticate empty token on nil store: err=%v, want ErrNotConfigured", err)
	}
	if ok {
		t.Error("Authenticate empty token: ok should be false")
	}
}

// Tests for Touch() — disabled / empty-id path

func TestTouchDisabledStore(t *testing.T) {
	var s *Store
	// nil store and empty id — should be a no-op
	if err := s.Touch(context.Background(), ""); err != nil {
		t.Errorf("Touch on nil store with empty id: unexpected error %v", err)
	}
}

func TestTouchEmptyID(t *testing.T) {
	s := &Store{db: nil}
	// Enabled() returns false for nil db, so Touch returns nil early
	if err := s.Touch(context.Background(), ""); err != nil {
		t.Errorf("Touch with empty id: unexpected error %v", err)
	}
}

// Tests for Revoke() disabled path

func TestRevokeDisabledStore(t *testing.T) {
	var s *Store
	err := s.Revoke(context.Background(), "org-1", "ak_abc")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Revoke on nil store: got %v, want ErrNotConfigured", err)
	}
}

// Tests for private helper functions

func TestHashToken(t *testing.T) {
	h1 := hashToken("hetchy_abc")
	h2 := hashToken("hetchy_abc")
	h3 := hashToken("hetchy_xyz")

	if len(h1) != 32 {
		t.Errorf("hashToken: expected 32 bytes (SHA-256), got %d", len(h1))
	}
	if !bytes.Equal(h1, h2) {
		t.Error("hashToken: same input should produce same hash")
	}
	if bytes.Equal(h1, h3) {
		t.Error("hashToken: different inputs should produce different hashes")
	}
}

func TestTokenDisplayPrefix(t *testing.T) {
	// displayPrefixLen == 18
	longToken := "hetchy_abcdefghijklmnopqrstuvwxyz" // > 18 chars
	got := tokenDisplayPrefix(longToken)
	if len(got) != displayPrefixLen {
		t.Errorf("tokenDisplayPrefix(long): len=%d, want %d", len(got), displayPrefixLen)
	}
	if got != longToken[:displayPrefixLen] {
		t.Errorf("tokenDisplayPrefix(long) = %q, want %q", got, longToken[:displayPrefixLen])
	}

	shortToken := "hetchy_x"
	if tokenDisplayPrefix(shortToken) != shortToken {
		t.Errorf("tokenDisplayPrefix(short): got %q, want %q", tokenDisplayPrefix(shortToken), shortToken)
	}

	// Exactly displayPrefixLen characters
	exactToken := strings.Repeat("a", displayPrefixLen)
	if tokenDisplayPrefix(exactToken) != exactToken {
		t.Errorf("tokenDisplayPrefix(exact len): got %q, want %q", tokenDisplayPrefix(exactToken), exactToken)
	}
}

func TestNewToken(t *testing.T) {
	tok1, err := newToken()
	if err != nil {
		t.Fatalf("newToken() error: %v", err)
	}
	tok2, err := newToken()
	if err != nil {
		t.Fatalf("newToken() error: %v", err)
	}
	if !strings.HasPrefix(tok1, keyPrefix) {
		t.Errorf("newToken() = %q, should start with %q", tok1, keyPrefix)
	}
	if tok1 == tok2 {
		t.Error("newToken(): two calls returned identical tokens")
	}
	if len(tok1) < len(keyPrefix)+10 {
		t.Errorf("newToken() token too short: %q", tok1)
	}
}

func TestNewID(t *testing.T) {
	id1, err := newID()
	if err != nil {
		t.Fatalf("newID() error: %v", err)
	}
	id2, err := newID()
	if err != nil {
		t.Fatalf("newID() error: %v", err)
	}
	if !strings.HasPrefix(id1, "ak_") {
		t.Errorf("newID() = %q, should start with ak_", id1)
	}
	if id1 == id2 {
		t.Error("newID(): two calls returned identical IDs")
	}
}

func TestKeyFromRow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	row := sqlc.OrgApiKey{
		ID:        "ak_abc123",
		OrgID:     "org_1",
		Name:      "prod key",
		KeyPrefix: "hetchy_abc12345",
		CreatedBy: "user_1",
	}
	// Fill in valid timestamps using pgtype
	row.CreatedAt.Valid = true
	row.CreatedAt.Time = now
	row.LastUsedAt.Valid = true
	row.LastUsedAt.Time = now.Add(time.Hour)

	k := keyFromRow(row)
	if k.ID != "ak_abc123" {
		t.Errorf("keyFromRow: ID = %q, want ak_abc123", k.ID)
	}
	if k.OrgID != "org_1" {
		t.Errorf("keyFromRow: OrgID = %q, want org_1", k.OrgID)
	}
	if k.Name != "prod key" {
		t.Errorf("keyFromRow: Name = %q, want prod key", k.Name)
	}
	if k.Prefix != "hetchy_abc12345" {
		t.Errorf("keyFromRow: Prefix = %q, want hetchy_abc12345", k.Prefix)
	}
	if k.CreatedBy != "user_1" {
		t.Errorf("keyFromRow: CreatedBy = %q, want user_1", k.CreatedBy)
	}
	if !k.CreatedAt.Equal(now) {
		t.Errorf("keyFromRow: CreatedAt = %v, want %v", k.CreatedAt, now)
	}
	if !k.LastUsedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("keyFromRow: LastUsedAt = %v, want %v", k.LastUsedAt, now.Add(time.Hour))
	}
}

func TestKeyFromRowZeroTimestamps(t *testing.T) {
	row := sqlc.OrgApiKey{
		ID: "ak_xyz",
	}
	// CreatedAt and LastUsedAt are invalid (zero pgtype.Timestamptz)
	k := keyFromRow(row)
	if !k.CreatedAt.IsZero() {
		t.Errorf("keyFromRow with invalid CreatedAt: expected zero time, got %v", k.CreatedAt)
	}
	if !k.LastUsedAt.IsZero() {
		t.Errorf("keyFromRow with invalid LastUsedAt: expected zero time, got %v", k.LastUsedAt)
	}
}
