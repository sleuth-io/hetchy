// Package orgcfg loads and stores per-organization runtime config
// (GitHub/Slack tokens, repo, base branch). It sits on top of the sqlc
// queries, decrypting secrets on read and encrypting on write so callers
// only ever see plaintext.
package orgcfg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

// ErrNotFound is returned when an org has no config row yet (e.g. just
// after onboarding, before settings have been saved).
var ErrNotFound = errors.New("orgcfg: not found")

// Config is the decrypted, app-friendly view of a row in org_configs.
// All token fields are plaintext; do not log them.
type Config struct {
	OrgID            string
	AnthropicAPIKey  string
	GitHubToken      string
	SlackBotToken    string
	SlackSocketToken string
	SXKey            string
	GitHubRepo       string
	GitHubBaseBranch string
}

// Store wires a *db.Store to a *secrets.Cipher and exposes plaintext
// reads/writes for org_configs.
type Store struct {
	db     *db.Store
	cipher *secrets.Cipher
}

// New constructs a Store. Both arguments are required.
func New(d *db.Store, c *secrets.Cipher) *Store {
	return &Store{db: d, cipher: c}
}

// Get returns the decrypted config for orgID, or ErrNotFound.
func (s *Store) Get(ctx context.Context, orgID string) (Config, error) {
	row, err := s.db.Queries.GetOrgConfig(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, fmt.Errorf("get org config: %w", err)
	}
	return s.decrypt(row)
}

// ListWithSlack returns every org that has both Slack tokens set —
// the list of orgs for which we should maintain a socket-mode connection.
func (s *Store) ListWithSlack(ctx context.Context) ([]Config, error) {
	rows, err := s.db.Queries.ListOrgConfigsWithSlack(ctx)
	if err != nil {
		return nil, fmt.Errorf("list slack orgs: %w", err)
	}
	out := make([]Config, 0, len(rows))
	for _, r := range rows {
		c, err := s.decrypt(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Upsert encrypts and writes the supplied Config.
func (s *Store) Upsert(ctx context.Context, c Config) (Config, error) {
	gh, err := s.cipher.Encrypt(c.GitHubToken)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt github token: %w", err)
	}
	sb, err := s.cipher.Encrypt(c.SlackBotToken)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt slack bot token: %w", err)
	}
	ss, err := s.cipher.Encrypt(c.SlackSocketToken)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt slack socket token: %w", err)
	}
	sx, err := s.cipher.Encrypt(c.SXKey)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt sx key: %w", err)
	}
	ak, err := s.cipher.Encrypt(c.AnthropicAPIKey)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt anthropic api key: %w", err)
	}
	branch := c.GitHubBaseBranch
	if branch == "" {
		branch = "main"
	}
	row, err := s.db.Queries.UpsertOrgConfig(ctx, sqlc.UpsertOrgConfigParams{
		OrgID:                     c.OrgID,
		GithubTokenEncrypted:      gh,
		SlackBotTokenEncrypted:    sb,
		SlackSocketTokenEncrypted: ss,
		SxKeyEncrypted:            sx,
		AnthropicApiKeyEncrypted:  ak,
		GithubRepo:                c.GitHubRepo,
		GithubBaseBranch:          branch,
	})
	if err != nil {
		return Config{}, fmt.Errorf("upsert org config: %w", err)
	}
	return s.decrypt(row)
}

func (s *Store) decrypt(row sqlc.OrgConfig) (Config, error) {
	gh, err := s.cipher.Decrypt(row.GithubTokenEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt github token: %w", err)
	}
	sb, err := s.cipher.Decrypt(row.SlackBotTokenEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt slack bot token: %w", err)
	}
	ss, err := s.cipher.Decrypt(row.SlackSocketTokenEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt slack socket token: %w", err)
	}
	sx, err := s.cipher.Decrypt(row.SxKeyEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt sx key: %w", err)
	}
	ak, err := s.cipher.Decrypt(row.AnthropicApiKeyEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt anthropic api key: %w", err)
	}
	return Config{
		OrgID:            row.OrgID,
		GitHubToken:      gh,
		SlackBotToken:    sb,
		SlackSocketToken: ss,
		SXKey:            sx,
		AnthropicAPIKey:  ak,
		GitHubRepo:       row.GithubRepo,
		GitHubBaseBranch: row.GithubBaseBranch,
	}, nil
}
