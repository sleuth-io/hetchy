package orgcfg

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

// newTestCipher returns a Cipher suitable for use in unit tests.
func newTestCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	// 32 hex-encoded bytes = 64 hex chars.
	c, err := secrets.New("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	return c
}

// fakeQuerier implements querier for unit tests.
type fakeQuerier struct {
	getOrgConfigFn                    func(ctx context.Context, orgID string) (sqlc.OrgConfig, error)
	getOrgConfigBySlackTeamIDFn       func(ctx context.Context, slackTeamID *string) (sqlc.OrgConfig, error)
	getOrgConfigByLinearWorkspaceIDFn func(ctx context.Context, linearWorkspaceID *string) (sqlc.OrgConfig, error)
	listOrgConfigsWithSlackFn         func(ctx context.Context) ([]sqlc.OrgConfig, error)
	upsertOrgConfigFn                 func(ctx context.Context, arg sqlc.UpsertOrgConfigParams) (sqlc.OrgConfig, error)
}

func (f *fakeQuerier) GetOrgConfig(ctx context.Context, orgID string) (sqlc.OrgConfig, error) {
	if f.getOrgConfigFn == nil {
		panic("fakeQuerier.getOrgConfigFn not set")
	}
	return f.getOrgConfigFn(ctx, orgID)
}

func (f *fakeQuerier) GetOrgConfigBySlackTeamID(ctx context.Context, slackTeamID *string) (sqlc.OrgConfig, error) {
	if f.getOrgConfigBySlackTeamIDFn == nil {
		panic("fakeQuerier.getOrgConfigBySlackTeamIDFn not set")
	}
	return f.getOrgConfigBySlackTeamIDFn(ctx, slackTeamID)
}

func (f *fakeQuerier) GetOrgConfigByLinearWorkspaceID(ctx context.Context, linearWorkspaceID *string) (sqlc.OrgConfig, error) {
	if f.getOrgConfigByLinearWorkspaceIDFn == nil {
		panic("fakeQuerier.getOrgConfigByLinearWorkspaceIDFn not set")
	}
	return f.getOrgConfigByLinearWorkspaceIDFn(ctx, linearWorkspaceID)
}

func (f *fakeQuerier) ListOrgConfigsWithSlack(ctx context.Context) ([]sqlc.OrgConfig, error) {
	if f.listOrgConfigsWithSlackFn == nil {
		panic("fakeQuerier.listOrgConfigsWithSlackFn not set")
	}
	return f.listOrgConfigsWithSlackFn(ctx)
}

func (f *fakeQuerier) UpsertOrgConfig(ctx context.Context, arg sqlc.UpsertOrgConfigParams) (sqlc.OrgConfig, error) {
	if f.upsertOrgConfigFn == nil {
		panic("fakeQuerier.upsertOrgConfigFn not set")
	}
	return f.upsertOrgConfigFn(ctx, arg)
}

// fakeTxQuerier implements txQuerier for unit tests.
type fakeTxQuerier struct {
	deleteLinearAgentSessionsByOrgFn func(ctx context.Context, orgID string) error
	deleteRepoSecretValuesByOrgFn    func(ctx context.Context, orgID string) error
	deleteRepoSetupSpecsByOrgFn      func(ctx context.Context, orgID string) error
	deleteGithubInstallationsByOrgFn func(ctx context.Context, orgID string) error
	deleteAgentRunsByOrgFn           func(ctx context.Context, orgID string) error
	deleteAgentProfilesByOrgFn       func(ctx context.Context, orgID string) error
	deleteConversationsByOrgFn       func(ctx context.Context, orgID string) error
	deleteOrgAPIKeysByOrgFn          func(ctx context.Context, orgID string) error
	deleteOrgConfigFn                func(ctx context.Context, orgID string) error
}

func (f *fakeTxQuerier) DeleteLinearAgentSessionsByOrg(ctx context.Context, orgID string) error {
	if f.deleteLinearAgentSessionsByOrgFn != nil {
		return f.deleteLinearAgentSessionsByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteRepoSecretValuesByOrg(ctx context.Context, orgID string) error {
	if f.deleteRepoSecretValuesByOrgFn != nil {
		return f.deleteRepoSecretValuesByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteRepoSetupSpecsByOrg(ctx context.Context, orgID string) error {
	if f.deleteRepoSetupSpecsByOrgFn != nil {
		return f.deleteRepoSetupSpecsByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteGithubInstallationsByOrg(ctx context.Context, orgID string) error {
	if f.deleteGithubInstallationsByOrgFn != nil {
		return f.deleteGithubInstallationsByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteAgentRunsByOrg(ctx context.Context, orgID string) error {
	if f.deleteAgentRunsByOrgFn != nil {
		return f.deleteAgentRunsByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteAgentProfilesByOrg(ctx context.Context, orgID string) error {
	if f.deleteAgentProfilesByOrgFn != nil {
		return f.deleteAgentProfilesByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteConversationsByOrg(ctx context.Context, orgID string) error {
	if f.deleteConversationsByOrgFn != nil {
		return f.deleteConversationsByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteOrgAPIKeysByOrg(ctx context.Context, orgID string) error {
	if f.deleteOrgAPIKeysByOrgFn != nil {
		return f.deleteOrgAPIKeysByOrgFn(ctx, orgID)
	}
	return nil
}

func (f *fakeTxQuerier) DeleteOrgConfig(ctx context.Context, orgID string) error {
	if f.deleteOrgConfigFn != nil {
		return f.deleteOrgConfigFn(ctx, orgID)
	}
	return nil
}

// newFakeStore builds a Store backed by fakeQuerier + fakeTxQuerier + test cipher.
func newFakeStore(t *testing.T, q *fakeQuerier, txq *fakeTxQuerier) *Store {
	t.Helper()
	cipher := newTestCipher(t)
	fakeTx := func(ctx context.Context, fn func(txQuerier) error) error {
		if txq == nil {
			panic("fakeTx: txq not set")
		}
		return fn(txq)
	}
	return &Store{q: q, tx: fakeTx, cipher: cipher}
}

// encryptFor is a test helper that encrypts a value using the same cipher the
// Store uses so fake querier return values are realistic (non-nil ciphertext).
func encryptFor(t *testing.T, val string) []byte {
	t.Helper()
	c := newTestCipher(t)
	b, err := c.Encrypt(val)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return b
}

// buildRow returns an sqlc.OrgConfig with the given fields encrypted using the
// test cipher, so round-trip tests can verify values survive encrypt/decrypt.
func buildRow(t *testing.T, orgID string) sqlc.OrgConfig {
	t.Helper()
	slackTeam := "T123"
	linWS := "LWS1"
	return sqlc.OrgConfig{
		OrgID:                          orgID,
		SlackBotTokenEncrypted:         encryptFor(t, "sbt"),
		SlackSocketTokenEncrypted:      encryptFor(t, "sst"),
		SxKeyEncrypted:                 encryptFor(t, "sxk"),
		AnthropicApiKeyEncrypted:       encryptFor(t, "aak"),
		ClaudeCodeOauthTokenEncrypted:  encryptFor(t, "cct"),
		OpenaiApiKeyEncrypted:          encryptFor(t, "oai"),
		OpenaiCodexOauthTokenEncrypted: encryptFor(t, "oct"),
		LinearAccessTokenEncrypted:     encryptFor(t, "lat"),
		GithubPatEncrypted:             encryptFor(t, "ghp"),
		SlackTeamID:                    &slackTeam,
		LinearWorkspaceID:              &linWS,
		LinearAppUserID:                "lau1",
		DefaultGithubOwner:             "owner",
		DefaultGithubRepo:              "repo",
	}
}

// ---- Get -------------------------------------------------------------------

func TestGet_Found(t *testing.T) {
	row := buildRow(t, "org1")
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, orgID string) (sqlc.OrgConfig, error) {
			if orgID != "org1" {
				t.Errorf("GetOrgConfig called with %q, want %q", orgID, "org1")
			}
			return row, nil
		},
	}
	s := newFakeStore(t, q, nil)
	cfg, err := s.Get(t.Context(), "org1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.OrgID != "org1" {
		t.Errorf("OrgID = %q, want %q", cfg.OrgID, "org1")
	}
	if cfg.SlackBotToken != "sbt" {
		t.Errorf("SlackBotToken = %q, want %q", cfg.SlackBotToken, "sbt")
	}
	if cfg.AnthropicAPIKey != "aak" {
		t.Errorf("AnthropicAPIKey = %q, want %q", cfg.AnthropicAPIKey, "aak")
	}
	if cfg.SlackTeamID != "T123" {
		t.Errorf("SlackTeamID = %q, want %q", cfg.SlackTeamID, "T123")
	}
	if cfg.LinearWorkspaceID != "LWS1" {
		t.Errorf("LinearWorkspaceID = %q, want %q", cfg.LinearWorkspaceID, "LWS1")
	}
	if cfg.DefaultGitHubOwner != "owner" {
		t.Errorf("DefaultGitHubOwner = %q, want %q", cfg.DefaultGitHubOwner, "owner")
	}
}

func TestGet_NilOptionalFields(t *testing.T) {
	row := buildRow(t, "org2")
	row.SlackTeamID = nil
	row.LinearWorkspaceID = nil
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) {
			return row, nil
		},
	}
	s := newFakeStore(t, q, nil)
	cfg, err := s.Get(t.Context(), "org2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.SlackTeamID != "" {
		t.Errorf("SlackTeamID = %q, want empty", cfg.SlackTeamID)
	}
	if cfg.LinearWorkspaceID != "" {
		t.Errorf("LinearWorkspaceID = %q, want empty", cfg.LinearWorkspaceID)
	}
}

func TestGet_NotFound(t *testing.T) {
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) {
			return sqlc.OrgConfig{}, pgx.ErrNoRows
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func TestGet_DBError(t *testing.T) {
	dbErr := errors.New("connection refused")
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) {
			return sqlc.OrgConfig{}, dbErr
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("DB error should not map to ErrNotFound")
	}
}

// ---- GetBySlackTeamID ------------------------------------------------------

func TestGetBySlackTeamID_EmptyTeamID(t *testing.T) {
	s := newFakeStore(t, &fakeQuerier{}, nil)
	_, err := s.GetBySlackTeamID(t.Context(), "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBySlackTeamID(\"\") error = %v, want ErrNotFound", err)
	}
}

func TestGetBySlackTeamID_Found(t *testing.T) {
	row := buildRow(t, "org1")
	q := &fakeQuerier{
		getOrgConfigBySlackTeamIDFn: func(_ context.Context, id *string) (sqlc.OrgConfig, error) {
			if id == nil || *id != "T999" {
				t.Errorf("GetOrgConfigBySlackTeamID id = %v, want T999", id)
			}
			return row, nil
		},
	}
	s := newFakeStore(t, q, nil)
	cfg, err := s.GetBySlackTeamID(t.Context(), "T999")
	if err != nil {
		t.Fatalf("GetBySlackTeamID: %v", err)
	}
	if cfg.OrgID != "org1" {
		t.Errorf("OrgID = %q, want org1", cfg.OrgID)
	}
}

func TestGetBySlackTeamID_NotFound(t *testing.T) {
	q := &fakeQuerier{
		getOrgConfigBySlackTeamIDFn: func(_ context.Context, _ *string) (sqlc.OrgConfig, error) {
			return sqlc.OrgConfig{}, pgx.ErrNoRows
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.GetBySlackTeamID(t.Context(), "Tunknown")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestGetBySlackTeamID_DBError(t *testing.T) {
	q := &fakeQuerier{
		getOrgConfigBySlackTeamIDFn: func(_ context.Context, _ *string) (sqlc.OrgConfig, error) {
			return sqlc.OrgConfig{}, errors.New("db down")
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.GetBySlackTeamID(t.Context(), "T1")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected wrapped DB error, got %v", err)
	}
}

// ---- GetByLinearWorkspaceID ------------------------------------------------

func TestGetByLinearWorkspaceID_EmptyID(t *testing.T) {
	s := newFakeStore(t, &fakeQuerier{}, nil)
	_, err := s.GetByLinearWorkspaceID(t.Context(), "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByLinearWorkspaceID(\"\") error = %v, want ErrNotFound", err)
	}
}

func TestGetByLinearWorkspaceID_Found(t *testing.T) {
	row := buildRow(t, "org2")
	q := &fakeQuerier{
		getOrgConfigByLinearWorkspaceIDFn: func(_ context.Context, id *string) (sqlc.OrgConfig, error) {
			if id == nil || *id != "LWS9" {
				t.Errorf("GetOrgConfigByLinearWorkspaceID id = %v, want LWS9", id)
			}
			return row, nil
		},
	}
	s := newFakeStore(t, q, nil)
	cfg, err := s.GetByLinearWorkspaceID(t.Context(), "LWS9")
	if err != nil {
		t.Fatalf("GetByLinearWorkspaceID: %v", err)
	}
	if cfg.OrgID != "org2" {
		t.Errorf("OrgID = %q, want org2", cfg.OrgID)
	}
}

func TestGetByLinearWorkspaceID_NotFound(t *testing.T) {
	q := &fakeQuerier{
		getOrgConfigByLinearWorkspaceIDFn: func(_ context.Context, _ *string) (sqlc.OrgConfig, error) {
			return sqlc.OrgConfig{}, pgx.ErrNoRows
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.GetByLinearWorkspaceID(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestGetByLinearWorkspaceID_DBError(t *testing.T) {
	q := &fakeQuerier{
		getOrgConfigByLinearWorkspaceIDFn: func(_ context.Context, _ *string) (sqlc.OrgConfig, error) {
			return sqlc.OrgConfig{}, errors.New("db error")
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.GetByLinearWorkspaceID(t.Context(), "LWS1")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected wrapped DB error, got %v", err)
	}
}

// ---- ListWithSlack ---------------------------------------------------------

func TestListWithSlack_Empty(t *testing.T) {
	q := &fakeQuerier{
		listOrgConfigsWithSlackFn: func(_ context.Context) ([]sqlc.OrgConfig, error) {
			return nil, nil
		},
	}
	s := newFakeStore(t, q, nil)
	cfgs, err := s.ListWithSlack(t.Context())
	if err != nil {
		t.Fatalf("ListWithSlack: %v", err)
	}
	if len(cfgs) != 0 {
		t.Errorf("len = %d, want 0", len(cfgs))
	}
}

func TestListWithSlack_Multiple(t *testing.T) {
	rows := []sqlc.OrgConfig{buildRow(t, "org1"), buildRow(t, "org2")}
	q := &fakeQuerier{
		listOrgConfigsWithSlackFn: func(_ context.Context) ([]sqlc.OrgConfig, error) {
			return rows, nil
		},
	}
	s := newFakeStore(t, q, nil)
	cfgs, err := s.ListWithSlack(t.Context())
	if err != nil {
		t.Fatalf("ListWithSlack: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("len = %d, want 2", len(cfgs))
	}
	if cfgs[0].OrgID != "org1" || cfgs[1].OrgID != "org2" {
		t.Errorf("unexpected OrgIDs: %v, %v", cfgs[0].OrgID, cfgs[1].OrgID)
	}
}

func TestListWithSlack_DBError(t *testing.T) {
	q := &fakeQuerier{
		listOrgConfigsWithSlackFn: func(_ context.Context) ([]sqlc.OrgConfig, error) {
			return nil, errors.New("db error")
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.ListWithSlack(t.Context())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- Upsert ----------------------------------------------------------------

func TestUpsert_RoundTrip(t *testing.T) {
	cipher := newTestCipher(t)
	in := Config{
		OrgID:                 "org1",
		SlackBotToken:         "sbt",
		SlackSocketToken:      "sst",
		SlackTeamID:           "T1",
		SXKey:                 "sxk",
		AnthropicAPIKey:       "aak",
		ClaudeCodeOAuthToken:  "cct",
		OpenAIAPIKey:          "oai",
		OpenAICodexOAuthToken: "oct",
		LinearAccessToken:     "lat",
		LinearWorkspaceID:     "LW1",
		LinearAppUserID:       "lau1",
		GitHubPAT:             "ghp",
		DefaultGitHubOwner:    "owner",
		DefaultGitHubRepo:     "repo",
	}

	var captured sqlc.UpsertOrgConfigParams
	q := &fakeQuerier{
		upsertOrgConfigFn: func(_ context.Context, arg sqlc.UpsertOrgConfigParams) (sqlc.OrgConfig, error) {
			captured = arg
			// Return a row encrypted the same way so decrypt works.
			teamID := "T1"
			linWS := "LW1"
			return sqlc.OrgConfig{
				OrgID:                          arg.OrgID,
				SlackBotTokenEncrypted:         arg.SlackBotTokenEncrypted,
				SlackSocketTokenEncrypted:      arg.SlackSocketTokenEncrypted,
				SxKeyEncrypted:                 arg.SxKeyEncrypted,
				AnthropicApiKeyEncrypted:       arg.AnthropicApiKeyEncrypted,
				ClaudeCodeOauthTokenEncrypted:  arg.ClaudeCodeOauthTokenEncrypted,
				OpenaiApiKeyEncrypted:          arg.OpenaiApiKeyEncrypted,
				OpenaiCodexOauthTokenEncrypted: arg.OpenaiCodexOauthTokenEncrypted,
				LinearAccessTokenEncrypted:     arg.LinearAccessTokenEncrypted,
				GithubPatEncrypted:             arg.GithubPatEncrypted,
				SlackTeamID:                    &teamID,
				LinearWorkspaceID:              &linWS,
				LinearAppUserID:                arg.LinearAppUserID,
				DefaultGithubOwner:             arg.DefaultGithubOwner,
				DefaultGithubRepo:              arg.DefaultGithubRepo,
			}, nil
		},
	}
	s := &Store{q: q, cipher: cipher, tx: nil}
	out, err := s.Upsert(t.Context(), in)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Verify the params passed to UpsertOrgConfig.
	if captured.OrgID != "org1" {
		t.Errorf("OrgID = %q, want org1", captured.OrgID)
	}
	if captured.SlackTeamID == nil || *captured.SlackTeamID != "T1" {
		t.Errorf("SlackTeamID = %v, want T1", captured.SlackTeamID)
	}
	if captured.LinearWorkspaceID == nil || *captured.LinearWorkspaceID != "LW1" {
		t.Errorf("LinearWorkspaceID = %v, want LW1", captured.LinearWorkspaceID)
	}
	if captured.DefaultGithubOwner != "owner" {
		t.Errorf("DefaultGithubOwner = %q, want owner", captured.DefaultGithubOwner)
	}

	// Verify the decrypted output matches input.
	if out.OrgID != in.OrgID {
		t.Errorf("out.OrgID = %q, want %q", out.OrgID, in.OrgID)
	}
	if out.SlackBotToken != in.SlackBotToken {
		t.Errorf("SlackBotToken = %q, want %q", out.SlackBotToken, in.SlackBotToken)
	}
	if out.AnthropicAPIKey != in.AnthropicAPIKey {
		t.Errorf("AnthropicAPIKey = %q, want %q", out.AnthropicAPIKey, in.AnthropicAPIKey)
	}
	if out.GitHubPAT != in.GitHubPAT {
		t.Errorf("GitHubPAT = %q, want %q", out.GitHubPAT, in.GitHubPAT)
	}
	if out.LinearWorkspaceID != in.LinearWorkspaceID {
		t.Errorf("LinearWorkspaceID = %q, want %q", out.LinearWorkspaceID, in.LinearWorkspaceID)
	}
}

func TestUpsert_EmptyOptionalIDs(t *testing.T) {
	cipher := newTestCipher(t)
	in := Config{OrgID: "org1"} // SlackTeamID and LinearWorkspaceID empty

	q := &fakeQuerier{
		upsertOrgConfigFn: func(_ context.Context, arg sqlc.UpsertOrgConfigParams) (sqlc.OrgConfig, error) {
			if arg.SlackTeamID != nil {
				t.Errorf("SlackTeamID should be nil for empty string, got %v", arg.SlackTeamID)
			}
			if arg.LinearWorkspaceID != nil {
				t.Errorf("LinearWorkspaceID should be nil for empty string, got %v", arg.LinearWorkspaceID)
			}
			return sqlc.OrgConfig{OrgID: "org1"}, nil
		},
	}
	s := &Store{q: q, cipher: cipher, tx: nil}
	_, err := s.Upsert(t.Context(), in)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func TestUpsert_DBError(t *testing.T) {
	q := &fakeQuerier{
		upsertOrgConfigFn: func(_ context.Context, _ sqlc.UpsertOrgConfigParams) (sqlc.OrgConfig, error) {
			return sqlc.OrgConfig{}, errors.New("db error")
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Upsert(t.Context(), Config{OrgID: "org1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- Delete ----------------------------------------------------------------

func TestDelete_EmptyOrgID(t *testing.T) {
	s := newFakeStore(t, &fakeQuerier{}, &fakeTxQuerier{})
	err := s.Delete(t.Context(), "")
	if err == nil {
		t.Fatal("expected error for empty orgID, got nil")
	}
}

func TestDelete_Success(t *testing.T) {
	deleted := map[string]bool{}
	txq := &fakeTxQuerier{
		deleteLinearAgentSessionsByOrgFn: func(_ context.Context, orgID string) error {
			deleted["linearSessions"] = true
			return nil
		},
		deleteRepoSecretValuesByOrgFn: func(_ context.Context, _ string) error {
			deleted["repoSecrets"] = true
			return nil
		},
		deleteRepoSetupSpecsByOrgFn: func(_ context.Context, _ string) error {
			deleted["repoSpecs"] = true
			return nil
		},
		deleteGithubInstallationsByOrgFn: func(_ context.Context, _ string) error {
			deleted["githubInstalls"] = true
			return nil
		},
		deleteAgentRunsByOrgFn: func(_ context.Context, _ string) error {
			deleted["agentRuns"] = true
			return nil
		},
		deleteAgentProfilesByOrgFn: func(_ context.Context, _ string) error {
			deleted["agentProfiles"] = true
			return nil
		},
		deleteConversationsByOrgFn: func(_ context.Context, _ string) error {
			deleted["conversations"] = true
			return nil
		},
		deleteOrgAPIKeysByOrgFn: func(_ context.Context, _ string) error {
			deleted["apiKeys"] = true
			return nil
		},
		deleteOrgConfigFn: func(_ context.Context, _ string) error {
			deleted["orgConfig"] = true
			return nil
		},
	}
	s := newFakeStore(t, &fakeQuerier{}, txq)
	if err := s.Delete(t.Context(), "org1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	required := []string{"linearSessions", "repoSecrets", "repoSpecs", "githubInstalls",
		"agentRuns", "agentProfiles", "conversations", "apiKeys", "orgConfig"}
	for _, k := range required {
		if !deleted[k] {
			t.Errorf("Delete did not call %s", k)
		}
	}
}

func TestDelete_ErrorStopsEarly(t *testing.T) {
	sentinelErr := errors.New("sentinel")
	called := 0
	txq := &fakeTxQuerier{
		deleteLinearAgentSessionsByOrgFn: func(_ context.Context, _ string) error {
			called++
			return sentinelErr
		},
		deleteRepoSecretValuesByOrgFn: func(_ context.Context, _ string) error {
			called++
			return nil
		},
	}
	s := newFakeStore(t, &fakeQuerier{}, txq)
	err := s.Delete(t.Context(), "org1")
	if !errors.Is(err, sentinelErr) {
		t.Errorf("error = %v, want sentinel", err)
	}
	if called != 1 {
		t.Errorf("called = %d, want 1 (should stop after first error)", called)
	}
}

// TestDelete_ErrorAtEachStep verifies that each delete call's error is
// propagated. We run one sub-test per step so coverage sees every error branch.
func TestDelete_ErrorAtEachStep(t *testing.T) {
	sentinel := errors.New("step error")

	steps := []struct {
		name  string
		build func() *fakeTxQuerier
	}{
		{"linearSessions", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteLinearAgentSessionsByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"repoSecretValues", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteRepoSecretValuesByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"repoSetupSpecs", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteRepoSetupSpecsByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"githubInstallations", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteGithubInstallationsByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"agentRuns", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteAgentRunsByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"agentProfiles", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteAgentProfilesByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"conversations", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteConversationsByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"orgAPIKeys", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteOrgAPIKeysByOrgFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
		{"orgConfig", func() *fakeTxQuerier {
			return &fakeTxQuerier{deleteOrgConfigFn: func(_ context.Context, _ string) error { return sentinel }}
		}},
	}

	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStore(t, &fakeQuerier{}, tc.build())
			err := s.Delete(t.Context(), "org1")
			if !errors.Is(err, sentinel) {
				t.Errorf("Delete/%s error = %v, want sentinel", tc.name, err)
			}
		})
	}
}

// ---- decrypt (error paths via Get) -----------------------------------------

// invalidCiphertext is too short for AES-GCM but non-empty so it's not
// treated as "no value". This causes cipher.Decrypt to return an error.
var invalidCiphertext = []byte("tooshort")

func TestDecrypt_BadSlackBotToken(t *testing.T) {
	row := buildRow(t, "org1")
	row.SlackBotTokenEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadSlackSocketToken(t *testing.T) {
	row := buildRow(t, "org1")
	row.SlackSocketTokenEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadSXKey(t *testing.T) {
	row := buildRow(t, "org1")
	row.SxKeyEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadAnthropicAPIKey(t *testing.T) {
	row := buildRow(t, "org1")
	row.AnthropicApiKeyEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadClaudeCodeOAuthToken(t *testing.T) {
	row := buildRow(t, "org1")
	row.ClaudeCodeOauthTokenEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadOpenAIAPIKey(t *testing.T) {
	row := buildRow(t, "org1")
	row.OpenaiApiKeyEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadOpenAICodexOAuthToken(t *testing.T) {
	row := buildRow(t, "org1")
	row.OpenaiCodexOauthTokenEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadLinearAccessToken(t *testing.T) {
	row := buildRow(t, "org1")
	row.LinearAccessTokenEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

func TestDecrypt_BadGitHubPAT(t *testing.T) {
	row := buildRow(t, "org1")
	row.GithubPatEncrypted = invalidCiphertext
	q := &fakeQuerier{
		getOrgConfigFn: func(_ context.Context, _ string) (sqlc.OrgConfig, error) { return row, nil },
	}
	s := newFakeStore(t, q, nil)
	_, err := s.Get(t.Context(), "org1")
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}

// TestListWithSlack_DecryptError verifies that a decrypt failure on one of the
// rows causes ListWithSlack to propagate the error.
func TestListWithSlack_DecryptError(t *testing.T) {
	good := buildRow(t, "org1")
	bad := buildRow(t, "org2")
	bad.SlackBotTokenEncrypted = invalidCiphertext

	q := &fakeQuerier{
		listOrgConfigsWithSlackFn: func(_ context.Context) ([]sqlc.OrgConfig, error) {
			return []sqlc.OrgConfig{good, bad}, nil
		},
	}
	s := newFakeStore(t, q, nil)
	_, err := s.ListWithSlack(t.Context())
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
}
