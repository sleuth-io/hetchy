// Package orgcfg loads and stores per-organization runtime config
// (Slack tokens, Anthropic key, default repo selection). It sits on top
// of the sqlc queries, decrypting secrets on read and encrypting on
// write so callers only ever see plaintext.
//
// GitHub auth no longer lives here: it's handled by the GitHub App
// installations cached in the github_app_installations table and
// minted on demand by internal/githubapp. The default-repo fields here
// only record what to fall back to when a chat request doesn't name a
// repo explicitly (Slack messages, plain-text follow-ups).
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
//
// AnthropicAPIKey and ClaudeCodeOAuthToken are alternative ways to
// authenticate Claude Code: an Anthropic Console API key (sk-ant-...)
// or a long-lived OAuth token minted by `claude setup-token` against
// the user's Pro/Max subscription. At least one is required at chat
// time; HandleRequest enforces that. They map to different env vars
// in the sandbox (ANTHROPIC_API_KEY vs CLAUDE_CODE_OAUTH_TOKEN), and
// the form handler clears the other when a new value is pasted into
// one — both stored at once would mean claudeAuthEnv silently picks
// OAuth over the API key the user thought they switched to.
type Config struct {
	OrgID                string
	AnthropicAPIKey      string
	ClaudeCodeOAuthToken string
	// OpenAIAPIKey is an OpenAI Platform API key (sk-...) used by the
	// Codex CLI when the user picks a GPT model. Like the Anthropic
	// pair above, the form handler enforces mutual exclusion with
	// OpenAICodexOAuthToken so the chosen credential is the one Codex
	// actually picks up.
	OpenAIAPIKey string
	// OpenAICodexOAuthToken is a long-lived token minted by
	// `codex login` against a ChatGPT subscription, parallel to the
	// Claude Code OAuth flow. Stored encrypted; never logged.
	OpenAICodexOAuthToken string
	SlackBotToken         string
	SlackSocketToken      string
	SlackTeamID           string
	SXKey                 string
	DefaultGitHubOwner    string
	DefaultGitHubRepo     string
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

// GetBySlackTeamID looks up an org by its Slack workspace team_id. Used
// by the HTTP webhook transport to route an incoming Slack event (which
// carries team_id at the payload root) to the right org's bot token.
func (s *Store) GetBySlackTeamID(ctx context.Context, teamID string) (Config, error) {
	if teamID == "" {
		return Config{}, ErrNotFound
	}
	row, err := s.db.Queries.GetOrgConfigBySlackTeamID(ctx, &teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, fmt.Errorf("get org config by slack team id: %w", err)
	}
	return s.decrypt(row)
}

// ListWithSlack returns every org with both bot and socket tokens set
// — the orgs for which slackManager opens a Socket Mode connection.
//
// HTTP-mode orgs (OAuth-installed) are intentionally excluded: the
// OAuth callback clears the socket token, so they don't appear here
// and the slack manager never tries to socket-connect them. If you
// need to enumerate all Slack-connected orgs (HTTP + socket), add a
// different query — don't generalize this one.
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
	cc, err := s.cipher.Encrypt(c.ClaudeCodeOAuthToken)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt claude code oauth token: %w", err)
	}
	oai, err := s.cipher.Encrypt(c.OpenAIAPIKey)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt openai api key: %w", err)
	}
	oct, err := s.cipher.Encrypt(c.OpenAICodexOAuthToken)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt openai codex oauth token: %w", err)
	}
	var teamID *string
	if c.SlackTeamID != "" {
		t := c.SlackTeamID
		teamID = &t
	}
	row, err := s.db.Queries.UpsertOrgConfig(ctx, sqlc.UpsertOrgConfigParams{
		OrgID:                          c.OrgID,
		SlackBotTokenEncrypted:         sb,
		SlackSocketTokenEncrypted:      ss,
		SxKeyEncrypted:                 sx,
		AnthropicApiKeyEncrypted:       ak,
		ClaudeCodeOauthTokenEncrypted:  cc,
		OpenaiApiKeyEncrypted:          oai,
		OpenaiCodexOauthTokenEncrypted: oct,
		SlackTeamID:                    teamID,
		DefaultGithubOwner:             c.DefaultGitHubOwner,
		DefaultGithubRepo:              c.DefaultGitHubRepo,
	})
	if err != nil {
		return Config{}, fmt.Errorf("upsert org config: %w", err)
	}
	return s.decrypt(row)
}

// Delete wipes every per-org row this app owns: org_configs, the
// org's conversations and agent runs (with cascaded run events), the
// org's agent profiles, every GitHub App installation bound to the
// org (with cascaded repos/teams/team members), and the
// installation-scoped repo bootstrap specs and secret values. Runs
// inside a single transaction so a mid-flight failure leaves the org
// fully present rather than half-deleted.
//
// The WorkOS organization itself is NOT touched here — callers
// (settings handler) call auth.DeleteOrganization separately because
// orgcfg has no business depending on WorkOS. Order matters: the
// bootstrap specs and secret values must be wiped BEFORE the
// installations they join on, because they are not FK-linked and
// would otherwise be left orphaned with no UI path back to them.
func (s *Store) Delete(ctx context.Context, orgID string) error {
	if orgID == "" {
		return errors.New("orgcfg: empty orgID")
	}
	return s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		if err := q.DeleteRepoSecretValuesByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete repo secret values: %w", err)
		}
		if err := q.DeleteRepoSetupSpecsByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete repo setup specs: %w", err)
		}
		if err := q.DeleteGithubInstallationsByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete github installations: %w", err)
		}
		if err := q.DeleteAgentRunsByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete agent runs: %w", err)
		}
		if err := q.DeleteAgentProfilesByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete agent profiles: %w", err)
		}
		if err := q.DeleteConversationsByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete conversations: %w", err)
		}
		if err := q.DeleteOrgConfig(ctx, orgID); err != nil {
			return fmt.Errorf("delete org config: %w", err)
		}
		return nil
	})
}

func (s *Store) decrypt(row sqlc.OrgConfig) (Config, error) {
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
	cc, err := s.cipher.Decrypt(row.ClaudeCodeOauthTokenEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt claude code oauth token: %w", err)
	}
	oai, err := s.cipher.Decrypt(row.OpenaiApiKeyEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt openai api key: %w", err)
	}
	oct, err := s.cipher.Decrypt(row.OpenaiCodexOauthTokenEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt openai codex oauth token: %w", err)
	}
	teamID := ""
	if row.SlackTeamID != nil {
		teamID = *row.SlackTeamID
	}
	return Config{
		OrgID:                 row.OrgID,
		SlackBotToken:         sb,
		SlackSocketToken:      ss,
		SlackTeamID:           teamID,
		SXKey:                 sx,
		AnthropicAPIKey:       ak,
		ClaudeCodeOAuthToken:  cc,
		OpenAIAPIKey:          oai,
		OpenAICodexOAuthToken: oct,
		DefaultGitHubOwner:    row.DefaultGithubOwner,
		DefaultGitHubRepo:     row.DefaultGithubRepo,
	}, nil
}
