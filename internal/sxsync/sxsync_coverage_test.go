package sxsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sxlib "github.com/sleuth-io/sx/v2/pkg/sxvault"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

// fakeOrgConfigStore is a test double for orgConfigStore. It returns a
// pre-configured Config or error from Get, and echoes the input from Upsert.
type fakeOrgConfigStore struct {
	cfg orgcfg.Config
	err error
}

func (f *fakeOrgConfigStore) Get(_ context.Context, _ string) (orgcfg.Config, error) {
	return f.cfg, f.err
}

func (f *fakeOrgConfigStore) Upsert(_ context.Context, cfg orgcfg.Config) (orgcfg.Config, error) {
	return cfg, nil
}

// fakeAgentStore is a test double for agentStore. It returns a pre-configured
// profile for GetBySlug and simulates a disabled DB by returning "store
// disabled" from Upsert — callers treat that error as evidence the vault
// operations preceding Upsert completed successfully.
type fakeAgentStore struct {
	profile agents.Profile
}

func (f *fakeAgentStore) EnsureSeeded(_ context.Context, _ string) error { return nil }

func (f *fakeAgentStore) GetBySlug(_ context.Context, _, slug string) (agents.Profile, error) {
	if agents.NormalizeSlug(slug) == agents.NormalizeSlug(f.profile.Slug) {
		return f.profile, nil
	}
	return agents.Profile{}, agents.ErrNotFound
}

func (f *fakeAgentStore) Upsert(_ context.Context, _ string, _ agents.Profile) (agents.Profile, error) {
	return agents.Profile{}, errors.New("agents: store disabled")
}

func (f *fakeAgentStore) UpdateVaultSync(_ context.Context, _, _, _, _, _, _, _ string) (agents.Profile, error) {
	return agents.Profile{}, errors.New("agents: store disabled")
}

func (f *fakeAgentStore) List(_ context.Context, _ string) ([]agents.Profile, error) {
	return nil, nil
}

func (f *fakeAgentStore) Delete(_ context.Context, _, _ string) error { return nil }

// pathVaultManager creates a test Manager whose openVaultFn returns the given
// path vault client as a BackendGitHubGit handle. It skips the normal
// orgcfg / db lookup so tests do not need a Postgres connection. The supplied
// profile is returned by the manager's agent store for GetBySlug lookups.
func pathVaultManager(client *sxlib.Client, profile agents.Profile) *Manager {
	return &Manager{
		agents: &fakeAgentStore{profile: profile},
		openVaultFn: func(_ context.Context, _ string, _ Actor) (VaultHandle, error) {
			return VaultHandle{Backend: BackendGitHubGit, Client: client}, nil
		},
	}
}

// newRuntimeTokenServer starts an httptest.Server that handles the ListBots
// and CreateBotRuntimeToken GraphQL operations expected by
// RuntimeSkillsNewEnv. It returns the server and a function to close it.
func newRuntimeTokenServer(t *testing.T, botName, token string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		var req struct {
			OperationName string `json:"operationName"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.OperationName {
		case "ListBots":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"bots": []any{
					map[string]any{"id": "bot-1", "name": botName, "slug": strings.ToLower(botName)},
				},
			}})
		case "CreateBotRuntimeToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"createBotRuntimeToken": map[string]any{
					"botKey":    token,
					"expiresAt": "2030-01-01T00:00:00Z",
				},
			}})
		default:
			t.Errorf("unexpected operation %q", req.OperationName)
			http.Error(w, "not implemented", http.StatusNotImplemented)
		}
	}))
}

// --- RuntimeSkillsNewEnv ---

func TestRuntimeSkillsNewEnvNilManagerReturnsErrNotConfigured(t *testing.T) {
	var m *Manager
	_, err := m.RuntimeSkillsNewEnv(context.Background(), "org1", agents.Profile{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RuntimeSkillsNewEnv(nil) = %v, want ErrNotConfigured", err)
	}
}

func TestRuntimeSkillsNewEnvNilOrgsReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{} // orgs is nil
	_, err := m.RuntimeSkillsNewEnv(context.Background(), "org1", agents.Profile{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RuntimeSkillsNewEnv(nil orgs) = %v, want ErrNotConfigured", err)
	}
}

func TestRuntimeSkillsNewEnvOrgNotFoundReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{orgs: &fakeOrgConfigStore{err: orgcfg.ErrNotFound}}
	_, err := m.RuntimeSkillsNewEnv(context.Background(), "org1", agents.Profile{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RuntimeSkillsNewEnv(org not found) = %v, want ErrNotConfigured", err)
	}
}

func TestRuntimeSkillsNewEnvEmptySXKeyReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{orgs: &fakeOrgConfigStore{cfg: orgcfg.Config{SXKey: ""}}}
	_, err := m.RuntimeSkillsNewEnv(context.Background(), "org1", agents.Profile{SXBot: "bob"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RuntimeSkillsNewEnv(empty SXKey) = %v, want ErrNotConfigured", err)
	}
}

func TestRuntimeSkillsNewEnvEmptySXBotReturnsError(t *testing.T) {
	m := &Manager{orgs: &fakeOrgConfigStore{cfg: orgcfg.Config{SXKey: "test-key"}}}
	_, err := m.RuntimeSkillsNewEnv(context.Background(), "org1", agents.Profile{SXBot: "  "})
	if err == nil || !strings.Contains(err.Error(), "sx bot is required") {
		t.Fatalf("RuntimeSkillsNewEnv(empty SXBot) = %v, want sx bot required error", err)
	}
}

func TestRuntimeSkillsNewEnvReturnsAgentBotKey(t *testing.T) {
	srv := newRuntimeTokenServer(t, "Bob", "agent-runtime-token")
	defer srv.Close()

	m := &Manager{
		orgs:               &fakeOrgConfigStore{cfg: orgcfg.Config{SXKey: "mgmt-token"}},
		skillsNewServerURL: srv.URL,
	}
	env, err := m.RuntimeSkillsNewEnv(context.Background(), "org1", agents.Profile{
		DisplayName: "Bob",
		SXBot:       "Bob",
	})
	if err != nil {
		t.Fatalf("RuntimeSkillsNewEnv: %v", err)
	}
	if env["HETCHY_AGENT_SX_BOT_KEY"] != "agent-runtime-token" {
		t.Fatalf("env = %v, want HETCHY_AGENT_SX_BOT_KEY=agent-runtime-token", env)
	}
}

func TestRuntimeSkillsNewEnvUsesSlugFallbackForLabel(t *testing.T) {
	// Verifies runtimeTokenLabel fallback: no DisplayName → uses Slug.
	srv := newRuntimeTokenServer(t, "bob", "slug-token")
	defer srv.Close()

	m := &Manager{
		orgs:               &fakeOrgConfigStore{cfg: orgcfg.Config{SXKey: "mgmt-token"}},
		skillsNewServerURL: srv.URL,
	}
	env, err := m.RuntimeSkillsNewEnv(context.Background(), "org1", agents.Profile{
		Slug:  "bob",
		SXBot: "bob",
	})
	if err != nil {
		t.Fatalf("RuntimeSkillsNewEnv(slug label): %v", err)
	}
	if env["HETCHY_AGENT_SX_BOT_KEY"] != "slug-token" {
		t.Fatalf("env = %v, want HETCHY_AGENT_SX_BOT_KEY=slug-token", env)
	}
}

// --- refreshExistingRemoteAgent success path ---

// TestManagerRefreshExistingRemoteAgentMergesState confirms that the
// mergeRemoteAgentState call is reached when the agent exists locally. The
// fakeAgentStore's Upsert returns "store disabled"; we treat that error as
// evidence the merge was attempted.
func TestManagerRefreshExistingRemoteAgentMergesState(t *testing.T) {
	m := &Manager{agents: &fakeAgentStore{profile: agents.Profile{Slug: "bob", SXBot: "bob"}}}
	remote := agents.Profile{Slug: "bob", SXBot: "updated-bob-bot"}
	err := m.refreshExistingRemoteAgent(context.Background(), "org1", remote)
	if err == nil || !strings.Contains(err.Error(), "store disabled") {
		t.Fatalf("refreshExistingRemoteAgent(bob) = %v, want store disabled error after merge attempt", err)
	}
}

// --- ListSkills success path ---

func TestManagerListSkillsWithPathVaultReturnsSkills(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := vaultClient.PutSkillZip(ctx, sxlib.SkillZipSpec{
		Name: "golang-pro", Version: "1", Description: "Go patterns.",
		ZipData: testSkillZip(t, "golang-pro"),
	}); err != nil {
		t.Fatalf("PutSkillZip: %v", err)
	}

	m := pathVaultManager(vaultClient, agents.Profile{})
	skills, err := m.ListSkills(ctx, "org1", Actor{Name: "Admin"})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	found := false
	for _, s := range skills {
		if s.Name == "golang-pro" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("golang-pro not found in ListSkills: %+v", skills)
	}
}

// --- ListTeams success path ---

func TestManagerListTeamsWithPathVaultReturnsNonNilSlice(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	m := pathVaultManager(vaultClient, agents.Profile{})
	teams, err := m.ListTeams(ctx, "org1", Actor{Name: "Admin"})
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if teams == nil {
		t.Fatal("expected non-nil (possibly empty) teams slice")
	}
}

// --- AttachSkill success path ---

// TestManagerAttachSkillWithPathVaultInstallsSkillOnBot verifies that
// AttachSkill routes through the vault correctly. The agents.NewStore(nil)
// backing causes Upsert to fail; we treat "store disabled" as evidence that
// all vault operations (EnsureBot, InstallAssetToBot) completed first.
func TestManagerAttachSkillWithPathVaultInstallsSkillOnBot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	// Pre-create the bot — EnsureBot rejects an empty Description on CREATE.
	if _, err := vaultClient.EnsureBot(ctx, sxlib.Bot{Name: "bob", Description: "Bob agent"}); err != nil {
		t.Fatalf("EnsureBot setup: %v", err)
	}
	if err := vaultClient.PutSkillZip(ctx, sxlib.SkillZipSpec{
		Name: "fix-pr", Version: "1", Description: "Fix PR.",
		ZipData: testSkillZip(t, "fix-pr"),
	}); err != nil {
		t.Fatalf("PutSkillZip setup: %v", err)
	}

	m := pathVaultManager(vaultClient, agents.Profile{Slug: "bob", SXBot: "bob"})
	_, err = m.AttachSkill(ctx, "org1", Actor{Name: "Admin"}, "bob", "fix-pr")
	if err == nil || !strings.Contains(err.Error(), "store disabled") {
		t.Fatalf("AttachSkill: %v, want store disabled after vault operations", err)
	}
	bots, lerr := vaultClient.ListBots(ctx)
	if lerr != nil {
		t.Fatalf("ListBots: %v", lerr)
	}
	if !botHasDirectSkill(bots, "bob", "fix-pr") {
		t.Fatalf("fix-pr not installed on bob after AttachSkill: %+v", bots)
	}
}

// --- DetachSkill success path ---

func TestManagerDetachSkillWithPathVaultUninstallsSkillFromBot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vaultClient.EnsureBot(ctx, sxlib.Bot{Name: "bob", Description: "Bob agent"}); err != nil {
		t.Fatalf("EnsureBot setup: %v", err)
	}
	if err := vaultClient.PutSkillZip(ctx, sxlib.SkillZipSpec{
		Name: "fix-pr", Version: "1", Description: "Fix PR.",
		ZipData: testSkillZip(t, "fix-pr"),
	}); err != nil {
		t.Fatalf("PutSkillZip setup: %v", err)
	}
	if err := vaultClient.InstallAssetToBot(ctx, "fix-pr", "bob"); err != nil {
		t.Fatalf("InstallAssetToBot setup: %v", err)
	}

	m := pathVaultManager(vaultClient, agents.Profile{Slug: "bob", SXBot: "bob"})
	_, err = m.DetachSkill(ctx, "org1", Actor{Name: "Admin"}, "bob", "fix-pr")
	if err == nil || !strings.Contains(err.Error(), "store disabled") {
		t.Fatalf("DetachSkill: %v, want store disabled after vault operations", err)
	}
	bots, lerr := vaultClient.ListBots(ctx)
	if lerr != nil {
		t.Fatalf("ListBots: %v", lerr)
	}
	if botHasDirectSkill(bots, "bob", "fix-pr") {
		t.Fatalf("fix-pr still installed on bob after DetachSkill: %+v", bots)
	}
}

// --- UploadSkillZip success path ---

func TestManagerUploadSkillZipWithPathVaultPutsSkillOnBot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vaultClient.EnsureBot(ctx, sxlib.Bot{Name: "bob", Description: "Bob agent"}); err != nil {
		t.Fatalf("EnsureBot setup: %v", err)
	}

	m := pathVaultManager(vaultClient, agents.Profile{Slug: "bob", SXBot: "bob"})
	_, err = m.UploadSkillZip(ctx, "org1", Actor{Name: "Admin"}, "bob", sxlib.SkillZipSpec{
		Name: "custom-skill", Version: "1", Description: "Custom skill.",
		ZipData: testSkillZip(t, "custom-skill"),
	})
	if err == nil || !strings.Contains(err.Error(), "store disabled") {
		t.Fatalf("UploadSkillZip: %v, want store disabled after vault operations", err)
	}
	assets, lerr := vaultClient.ListAssetsWithOptions(ctx, sxlib.ListOptions{Type: "skill"})
	if lerr != nil {
		t.Fatalf("ListAssetsWithOptions: %v", lerr)
	}
	found := false
	for _, a := range assets {
		if a.Name == "custom-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("custom-skill not found in vault after UploadSkillZip: %+v", assets)
	}
}

// --- DeleteAgent success path ---

// TestManagerDeleteAgentWithPathVaultRemovesBotFromVault verifies that
// DeleteAgent routes through the vault delete logic. The fakeAgentStore
// returns a non-built-in profile for "bob"; the path vault has "bob" bot
// pre-created. After deleteAgentFromVault runs, fakeAgentStore.Delete returns
// nil, so the whole operation succeeds.
func TestManagerDeleteAgentWithPathVaultRemovesBotFromVault(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vaultClient.EnsureBot(ctx, sxlib.Bot{Name: "bob", Description: "Bob agent"}); err != nil {
		t.Fatalf("EnsureBot setup: %v", err)
	}

	m := pathVaultManager(vaultClient, agents.Profile{Slug: "bob", SXBot: "bob", Enabled: true})
	if err := m.DeleteAgent(ctx, "org1", Actor{Name: "Admin"}, "bob"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	bots, lerr := vaultClient.ListBots(ctx)
	if lerr != nil {
		t.Fatalf("ListBots: %v", lerr)
	}
	for _, b := range bots {
		if b.Name == "bob" {
			t.Fatalf("bot 'bob' still exists in vault after DeleteAgent: %+v", bots)
		}
	}
}

// --- SaveAgent success path ---

// TestManagerSaveAgentWithPathVaultCreatesAgentInVault verifies that
// SaveAgent writes the bot and agent asset to the vault before attempting
// the DB write. The fakeAgentStore.Upsert returns "store disabled"; we treat
// that error as evidence that PutAgent completed successfully.
func TestManagerSaveAgentWithPathVaultCreatesAgentInVault(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	m := pathVaultManager(vaultClient, agents.Profile{})
	_, err = m.SaveAgent(ctx, "org1", Actor{Name: "Admin"}, agents.Profile{
		Slug:        "bob",
		DisplayName: "Bob",
		SXBot:       "bob",
		Description: "Bob agent",
	}, "")
	if err == nil || !strings.Contains(err.Error(), "store disabled") {
		t.Fatalf("SaveAgent: %v, want store disabled after vault operations", err)
	}
	bots, lerr := vaultClient.ListBots(ctx)
	if lerr != nil {
		t.Fatalf("ListBots: %v", lerr)
	}
	found := false
	for _, b := range bots {
		if b.Name == "bob" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("bot 'bob' not found in vault after SaveAgent: %+v", bots)
	}
}

// AddAgentTeam and RemoveAgentTeam success paths require a skills.new server
// that exposes team operations; the path vault backend used in these tests
// requires teams to pre-exist in a vault manifest and does not expose a team
// creation API via sxlib.Client. Those paths are covered by the existing
// nil/ErrNotConfigured tests in manager_nil_test.go.
