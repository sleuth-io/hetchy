package sxsync

import (
	"context"
	"testing"

	"github.com/sleuth-io/hetchy/internal/agents"
)

// A self-hosted deployment can run with sx entirely unconfigured — no database
// wiring, no org config store. Every entry point has to degrade to "nothing to
// do" rather than panic on a nil dependency, because these are called from the
// normal run path, not from an sx-specific branch.
func TestUnconfiguredManagerDegradesInsteadOfPanicking(t *testing.T) {
	ctx := context.Background()

	t.Run("nil manager", func(t *testing.T) {
		var m *Manager

		if v, err := m.GitVault(ctx, "org_1"); err != nil || v.Configured {
			t.Fatalf("GitVault = (%+v, %v), want an unconfigured view and no error", v, err)
		}
		if key, err := m.skillsNewKey(ctx, "org_1"); err != nil || key != "" {
			t.Fatalf("skillsNewKey = (%q, %v), want empty and no error", key, err)
		}
		if err := m.ensureOrgConfig(ctx, "org_1"); err != nil {
			t.Fatalf("ensureOrgConfig = %v, want nil", err)
		}
		// With no database to consult, importing a remote agent is allowed:
		// there is no local profile that could be overwritten.
		ok, err := m.shouldImportRemoteAgent(ctx, "org_1", agents.Profile{Slug: "reviewer"})
		if err != nil || !ok {
			t.Fatalf("shouldImportRemoteAgent = (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("manager without a database or org store", func(t *testing.T) {
		m := &Manager{}

		if v, err := m.GitVault(ctx, "org_1"); err != nil || v.Configured {
			t.Fatalf("GitVault = (%+v, %v), want an unconfigured view and no error", v, err)
		}
		if key, err := m.skillsNewKey(ctx, "org_1"); err != nil || key != "" {
			t.Fatalf("skillsNewKey = (%q, %v), want empty and no error", key, err)
		}
		if err := m.ensureOrgConfig(ctx, "org_1"); err != nil {
			t.Fatalf("ensureOrgConfig = %v, want nil", err)
		}
		ok, err := m.shouldImportRemoteAgent(ctx, "org_1", agents.Profile{Slug: "reviewer"})
		if err != nil || !ok {
			t.Fatalf("shouldImportRemoteAgent = (%v, %v), want (true, nil)", ok, err)
		}
	})
}
