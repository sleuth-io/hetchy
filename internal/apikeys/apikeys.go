package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

const (
	keyPrefix        = "hetchy_"
	displayPrefixLen = 18
)

var (
	ErrNotConfigured = errors.New("apikeys: store not configured")
	ErrNotFound      = errors.New("apikeys: key not found")
)

// querier is a narrow interface for the DB operations needed by this package.
// Using an interface here allows unit tests to inject a fake without a real
// Postgres connection.
type querier interface {
	CreateOrgAPIKey(ctx context.Context, arg sqlc.CreateOrgAPIKeyParams) (sqlc.OrgApiKey, error)
	ListOrgAPIKeys(ctx context.Context, orgID string) ([]sqlc.OrgApiKey, error)
	GetOrgAPIKeyByHash(ctx context.Context, keyHash []byte) (sqlc.OrgApiKey, error)
	TouchOrgAPIKeyLastUsed(ctx context.Context, id string) error
	RevokeOrgAPIKey(ctx context.Context, arg sqlc.RevokeOrgAPIKeyParams) (int64, error)
}

type Store struct {
	q querier
}

type Key struct {
	ID         string
	OrgID      string
	Name       string
	Prefix     string
	CreatedBy  string
	CreatedAt  time.Time
	LastUsedAt time.Time
}

type CreatedKey struct {
	Key
	Token string
}

func New(d *db.Store) *Store {
	if d == nil || d.Queries == nil {
		return &Store{}
	}
	return &Store{q: d.Queries}
}

func (s *Store) Enabled() bool {
	return s != nil && s.q != nil
}

func (s *Store) Create(ctx context.Context, orgID, name, createdBy string) (CreatedKey, error) {
	if !s.Enabled() {
		return CreatedKey{}, ErrNotConfigured
	}
	orgID = strings.TrimSpace(orgID)
	name = strings.TrimSpace(name)
	if orgID == "" {
		return CreatedKey{}, errors.New("org id is required")
	}
	if name == "" {
		return CreatedKey{}, errors.New("name is required")
	}
	if len([]rune(name)) > 120 {
		return CreatedKey{}, errors.New("name must be 120 characters or fewer")
	}
	token, err := newToken()
	if err != nil {
		return CreatedKey{}, err
	}
	id, err := newID()
	if err != nil {
		return CreatedKey{}, err
	}
	row, err := s.q.CreateOrgAPIKey(ctx, sqlc.CreateOrgAPIKeyParams{
		ID:        id,
		OrgID:     orgID,
		Name:      name,
		KeyPrefix: tokenDisplayPrefix(token),
		KeyHash:   hashToken(token),
		CreatedBy: createdBy,
	})
	if err != nil {
		return CreatedKey{}, fmt.Errorf("create api key: %w", err)
	}
	return CreatedKey{Key: keyFromRow(row), Token: token}, nil
}

func (s *Store) List(ctx context.Context, orgID string) ([]Key, error) {
	if !s.Enabled() {
		return nil, nil
	}
	rows, err := s.q.ListOrgAPIKeys(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	out := make([]Key, 0, len(rows))
	for _, row := range rows {
		out = append(out, keyFromRow(row))
	}
	return out, nil
}

func (s *Store) Authenticate(ctx context.Context, token string) (Key, bool, error) {
	if !s.Enabled() {
		return Key{}, false, ErrNotConfigured
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, keyPrefix) {
		return Key{}, false, nil
	}
	row, err := s.q.GetOrgAPIKeyByHash(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Key{}, false, nil
		}
		return Key{}, false, fmt.Errorf("lookup api key: %w", err)
	}
	return keyFromRow(row), true, nil
}

func (s *Store) Touch(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if !s.Enabled() || id == "" {
		return nil
	}
	return s.q.TouchOrgAPIKeyLastUsed(ctx, id)
}

func (s *Store) Revoke(ctx context.Context, orgID, id string) error {
	if !s.Enabled() {
		return ErrNotConfigured
	}
	rows, err := s.q.RevokeOrgAPIKey(ctx, sqlc.RevokeOrgAPIKeyParams{
		OrgID: orgID,
		ID:    id,
	})
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func newToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate api key id: %w", err)
	}
	return "ak_" + hex.EncodeToString(raw[:]), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func tokenDisplayPrefix(token string) string {
	if len(token) <= displayPrefixLen {
		return token
	}
	return token[:displayPrefixLen]
}

func keyFromRow(row sqlc.OrgApiKey) Key {
	k := Key{
		ID:        row.ID,
		OrgID:     row.OrgID,
		Name:      row.Name,
		Prefix:    row.KeyPrefix,
		CreatedBy: row.CreatedBy,
	}
	if row.CreatedAt.Valid {
		k.CreatedAt = row.CreatedAt.Time
	}
	if row.LastUsedAt.Valid {
		k.LastUsedAt = row.LastUsedAt.Time
	}
	return k
}
