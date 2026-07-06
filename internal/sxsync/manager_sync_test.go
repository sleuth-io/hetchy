package sxsync

import (
	"context"
	"errors"
	"sync"
	"testing"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/sleuth-io/hetchy/internal/agents"
)

// recordingAgentStore is a functional in-memory test double for agentStore.
// Unlike fakeAgentStore (which deliberately fails Upsert to prove vault
// operations ran first), this one actually persists profiles so multi-step
// flows such as SyncAgents can run end-to-end without a Postgres connection.
type recordingAgentStore struct {
	mu            sync.Mutex
	profiles      map[string]agents.Profile
	seededCalls   int
	upsertCalls   int
	vaultSyncs    int
	deletedSlugs  []string
	updatedStatus map[string]string
	seedErr       error
}

func newRecordingAgentStore(initial ...agents.Profile) *recordingAgentStore {
	s := &recordingAgentStore{
		profiles:      map[string]agents.Profile{},
		updatedStatus: map[string]string{},
	}
	for _, p := range initial {
		s.profiles[agents.NormalizeSlug(p.Slug)] = p
	}
	return s
}

func (s *recordingAgentStore) EnsureSeeded(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seededCalls++
	return s.seedErr
}

func (s *recordingAgentStore) GetBySlug(_ context.Context, _, slug string) (agents.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.profiles[agents.NormalizeSlug(slug)]; ok {
		return p, nil
	}
	return agents.Profile{}, agents.ErrNotFound
}

func (s *recordingAgentStore) Upsert(_ context.Context, _ string, p agents.Profile) (agents.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertCalls++
	p.Slug = agents.NormalizeSlug(p.Slug)
	s.profiles[p.Slug] = p
	return p, nil
}

func (s *recordingAgentStore) UpdateVaultSync(_ context.Context, _, slug, backend, _, _, status, _ string) (agents.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vaultSyncs++
	slug = agents.NormalizeSlug(slug)
	p := s.profiles[slug]
	p.VaultBackend = backend
	p.SyncStatus = status
	s.profiles[slug] = p
	s.updatedStatus[slug] = status
	return p, nil
}

func (s *recordingAgentStore) List(_ context.Context, _ string) ([]agents.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agents.Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, p)
	}
	return out, nil
}

func (s *recordingAgentStore) Delete(_ context.Context, _, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug = agents.NormalizeSlug(slug)
	s.deletedSlugs = append(s.deletedSlugs, slug)
	delete(s.profiles, slug)
	return nil
}

// TestManagerSyncAgentsImportsRemoteAndPrunesMissing drives the full
// SyncAgents flow against a real path vault: it seeds two remote bots (one
// with a matching agent asset), a stale local custom agent that is not present
// remotely, and a built-in agent. After sync the remote bots are imported and
// marked synced, the stale custom agent is pruned, and the built-in agent is
// preserved.
func TestManagerSyncAgentsImportsRemoteAndPrunesMissing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vaultClient.EnsureBot(ctx, sxlib.Bot{Name: "reviewer", Description: "Reviews PRs"}); err != nil {
		t.Fatalf("EnsureBot reviewer: %v", err)
	}
	if _, err := vaultClient.EnsureBot(ctx, sxlib.Bot{Name: "planner", Description: "Plans work"}); err != nil {
		t.Fatalf("EnsureBot planner: %v", err)
	}
	if _, err := vaultClient.PutAgent(ctx, sxlib.AgentSpec{
		BotName:     "reviewer",
		AssetName:   "reviewer",
		Version:     "1",
		Description: "Reviewer persona",
		Prompt:      "You are the reviewer.",
	}); err != nil {
		t.Fatalf("PutAgent reviewer: %v", err)
	}

	store := newRecordingAgentStore(
		agents.Profile{Slug: "stale", SXBot: "stale", VaultBackend: BackendGitHubGit, Enabled: true},
		agents.Profile{Slug: "bob", SXBot: "bob", Enabled: true, BuiltIn: true},
	)
	m := &Manager{
		agents: store,
		openVaultFn: func(_ context.Context, _ string, _ Actor) (VaultHandle, error) {
			return VaultHandle{Backend: BackendGitHubGit, Client: vaultClient}, nil
		},
	}

	imported, err := m.SyncAgents(ctx, "org1", Actor{Name: "Admin", Email: "admin@example.com"})
	if err != nil {
		t.Fatalf("SyncAgents: %v", err)
	}

	gotSlugs := map[string]bool{}
	for _, p := range imported {
		gotSlugs[p.Slug] = true
	}
	if !gotSlugs["reviewer"] || !gotSlugs["planner"] {
		t.Fatalf("imported remote profiles = %+v, want reviewer and planner", imported)
	}

	if store.seededCalls == 0 {
		t.Error("SyncAgents did not seed built-in agents")
	}
	if store.updatedStatus["reviewer"] != "imported" || store.updatedStatus["planner"] != "imported" {
		t.Errorf("remote agents not marked imported: %+v", store.updatedStatus)
	}

	store.mu.Lock()
	_, staleStillPresent := store.profiles["stale"]
	_, bobStillPresent := store.profiles["bob"]
	_, reviewerStillPresent := store.profiles["reviewer"]
	_, plannerStillPresent := store.profiles["planner"]
	upserts, syncs := store.upsertCalls, store.vaultSyncs
	deleted := append([]string(nil), store.deletedSlugs...)
	store.mu.Unlock()

	if staleStillPresent {
		t.Errorf("stale custom agent should have been pruned, deletes=%v", deleted)
	}
	if !bobStillPresent {
		t.Error("built-in agent bob should not have been pruned")
	}
	if !reviewerStillPresent || !plannerStillPresent {
		t.Error("just-imported remote agents must not be pruned")
	}
	// One Upsert + one UpdateVaultSync per imported remote agent (reviewer, planner).
	if upserts != 2 || syncs != 2 {
		t.Errorf("import writes = %d upserts / %d vault syncs, want 2 / 2", upserts, syncs)
	}
}

// TestManagerSyncAgentsPropagatesSeedError verifies SyncAgents surfaces an
// EnsureSeeded failure before touching the vault.
func TestManagerSyncAgentsPropagatesSeedError(t *testing.T) {
	seedErr := errors.New("seed boom")
	store := newRecordingAgentStore()
	store.seedErr = seedErr
	m := &Manager{
		agents: store,
		openVaultFn: func(_ context.Context, _ string, _ Actor) (VaultHandle, error) {
			t.Fatal("openVaultFn should not run when seeding fails")
			return VaultHandle{}, nil
		},
	}
	if _, err := m.SyncAgents(context.Background(), "org1", Actor{}); !errors.Is(err, seedErr) {
		t.Fatalf("SyncAgents = %v, want seed error", err)
	}
}

// TestManagerSyncAgentsPropagatesVaultOpenError verifies SyncAgents surfaces a
// vault-open failure after seeding.
func TestManagerSyncAgentsPropagatesVaultOpenError(t *testing.T) {
	openErr := errors.New("open boom")
	store := newRecordingAgentStore()
	m := &Manager{
		agents: store,
		openVaultFn: func(_ context.Context, _ string, _ Actor) (VaultHandle, error) {
			return VaultHandle{}, openErr
		},
	}
	if _, err := m.SyncAgents(context.Background(), "org1", Actor{}); !errors.Is(err, openErr) {
		t.Fatalf("SyncAgents = %v, want open error", err)
	}
	if store.seededCalls == 0 {
		t.Error("SyncAgents should seed before opening the vault")
	}
}
