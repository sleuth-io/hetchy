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

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/secrets"
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
	// OpenAICodexOAuthToken stores the Codex auth JSON from
	// `~/.codex/auth.json`, or an agent-identity JWT accepted by
	// `codex login --with-access-token`. Stored encrypted; never logged.
	OpenAICodexOAuthToken string
	SlackBotToken         string
	SlackSocketToken      string
	SlackTeamID           string
	// LinearAccessToken is the OAuth (actor=app) access token for the
	// org's Linear workspace. LinearWorkspaceID is Linear's organization
	// ID — the webhook router's lookup key, mirroring SlackTeamID.
	// LinearAppUserID is the app's own viewer id in that workspace.
	LinearAccessToken string
	LinearWorkspaceID string
	LinearAppUserID   string
	// GitHubPAT is a personal access token an org pastes as an
	// alternative to installing the GitHub App. Repos it grants access
	// to are cached under a synthetic (negative) installation id — see
	// internal/githubapp's PAT support for how tokens are resolved.
	GitHubPAT          string
	SXKey              string
	DefaultGitHubOwner string
	DefaultGitHubRepo  string
}

// querier is the narrow database interface needed by Store for reads and upserts.
// Using an interface here lets unit tests inject a fake without a real Postgres
// connection.
type querier interface {
	GetOrgConfig(ctx context.Context, orgID string) (sqlc.OrgConfig, error)
	GetOrgConfigBySlackTeamID(ctx context.Context, slackTeamID *string) (sqlc.OrgConfig, error)
	GetOrgConfigByLinearWorkspaceID(ctx context.Context, linearWorkspaceID *string) (sqlc.OrgConfig, error)
	ListOrgConfigsWithSlack(ctx context.Context) ([]sqlc.OrgConfig, error)
	UpsertOrgConfig(ctx context.Context, arg sqlc.UpsertOrgConfigParams) (sqlc.OrgConfig, error)
}

// txQuerier is the view of sqlc.Queries required inside the Delete transaction.
type txQuerier interface {
	DeleteLinearAgentSessionsByOrg(ctx context.Context, orgID string) error
	DeleteRepoSecretValuesByOrg(ctx context.Context, orgID string) error
	DeleteRepoSetupSpecsByOrg(ctx context.Context, orgID string) error
	DeleteGithubInstallationsByOrg(ctx context.Context, orgID string) error
	DeleteAgentRunsByOrg(ctx context.Context, orgID string) error
	DeleteAgentProfilesByOrg(ctx context.Context, orgID string) error
	DeleteConversationsByOrg(ctx context.Context, orgID string) error
	DeleteOrgAPIKeysByOrg(ctx context.Context, orgID string) error
	DeleteOrgConfig(ctx context.Context, orgID string) error
}

// txRunner wraps the DB transaction plumbing. It runs fn inside a transaction
// and commits on success or rolls back on error.
type txRunner func(context.Context, func(txQuerier) error) error

// Store wires a querier and txRunner to a *secrets.Cipher and exposes plaintext
// reads/writes for org_configs.
type Store struct {
	q      querier
	tx     txRunner
	cipher *secrets.Cipher
}

// New constructs a Store. Both arguments are required.
func New(d *db.Store, c *secrets.Cipher) *Store {
	return &Store{
		q:      d.Queries,
		cipher: c,
		tx: func(ctx context.Context, fn func(txQuerier) error) error {
			return d.WithTx(ctx, func(q *sqlc.Queries) error {
				return fn(q)
			})
		},
	}
}

// Get returns the decrypted config for orgID, or ErrNotFound.
func (s *Store) Get(ctx context.Context, orgID string) (Config, error) {
	row, err := s.q.GetOrgConfig(ctx, orgID)
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
	row, err := s.q.GetOrgConfigBySlackTeamID(ctx, &teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, fmt.Errorf("get org config by slack team id: %w", err)
	}
	return s.decrypt(row)
}

// GetByLinearWorkspaceID looks up an org by its Linear workspace
// (organization) ID. Used by the Linear webhook transport to route an
// inbound agent session event (which carries organizationId at the
// payload root) to the right org's access token.
func (s *Store) GetByLinearWorkspaceID(ctx context.Context, workspaceID string) (Config, error) {
	if workspaceID == "" {
		return Config{}, ErrNotFound
	}
	row, err := s.q.GetOrgConfigByLinearWorkspaceID(ctx, &workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, fmt.Errorf("get org config by linear workspace id: %w", err)
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
	rows, err := s.q.ListOrgConfigsWithSlack(ctx)
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
	lt, err := s.cipher.Encrypt(c.LinearAccessToken)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt linear access token: %w", err)
	}
	gp, err := s.cipher.Encrypt(c.GitHubPAT)
	if err != nil {
		return Config{}, fmt.Errorf("encrypt github pat: %w", err)
	}
	var teamID *string
	if c.SlackTeamID != "" {
		t := c.SlackTeamID
		teamID = &t
	}
	var linearWorkspaceID *string
	if c.LinearWorkspaceID != "" {
		w := c.LinearWorkspaceID
		linearWorkspaceID = &w
	}
	row, err := s.q.UpsertOrgConfig(ctx, sqlc.UpsertOrgConfigParams{
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
		LinearAccessTokenEncrypted:     lt,
		LinearWorkspaceID:              linearWorkspaceID,
		LinearAppUserID:                c.LinearAppUserID,
		GithubPatEncrypted:             gp,
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
	return s.tx(ctx, func(q txQuerier) error {
		if err := q.DeleteLinearAgentSessionsByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete linear agent sessions: %w", err)
		}
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
		if err := q.DeleteOrgAPIKeysByOrg(ctx, orgID); err != nil {
			return fmt.Errorf("delete api keys: %w", err)
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
	lt, err := s.cipher.Decrypt(row.LinearAccessTokenEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt linear access token: %w", err)
	}
	gp, err := s.cipher.Decrypt(row.GithubPatEncrypted)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt github pat: %w", err)
	}
	teamID := ""
	if row.SlackTeamID != nil {
		teamID = *row.SlackTeamID
	}
	linearWorkspaceID := ""
	if row.LinearWorkspaceID != nil {
		linearWorkspaceID = *row.LinearWorkspaceID
	}
	return Config{
		OrgID:                 row.OrgID,
		SlackBotToken:         sb,
		SlackSocketToken:      ss,
		SlackTeamID:           teamID,
		LinearAccessToken:     lt,
		LinearWorkspaceID:     linearWorkspaceID,
		LinearAppUserID:       row.LinearAppUserID,
		GitHubPAT:             gp,
		SXKey:                 sx,
		AnthropicAPIKey:       ak,
		ClaudeCodeOAuthToken:  cc,
		OpenAIAPIKey:          oai,
		OpenAICodexOAuthToken: oct,
		DefaultGitHubOwner:    row.DefaultGithubOwner,
		DefaultGitHubRepo:     row.DefaultGithubRepo,
	}, nil
}
