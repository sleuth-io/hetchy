package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

func TestAddOrgSXVaultEnvInjectsSkillsNewRuntimeToken(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		sx: &fakeSXManager{skillsNewEnv: map[string]string{
			"HETCHY_AGENT_SX_BOT_KEY": "runtime-token",
		}},
	}
	env := map[string]string{}

	b.addOrgSXVaultEnv(context.Background(), "org_1", agents.Profile{
		Slug:  "reviewer",
		SXBot: "reviewer",
	}, env)

	if got := env["HETCHY_AGENT_SX_BOT_KEY"]; got != "runtime-token" {
		t.Fatalf("runtime token env = %q, want runtime-token", got)
	}
}

func TestAddOrgSXVaultEnvInjectsGitVaultEnv(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		sx: &fakeSXManager{gitEnv: map[string]string{
			"HETCHY_SX_GIT_VAULT_URL":   "https://github.com/acme/vault.git",
			"HETCHY_SX_GIT_VAULT_TOKEN": "github-token",
		}},
	}
	env := map[string]string{}

	b.addOrgSXVaultEnv(context.Background(), "org_1", agents.Profile{
		Slug:         "reviewer",
		VaultBackend: sxsync.BackendGitHubGit,
	}, env)

	if got := env["HETCHY_SX_GIT_VAULT_URL"]; got != "https://github.com/acme/vault.git" {
		t.Fatalf("git vault URL = %q", got)
	}
	if got := env["HETCHY_SX_GIT_VAULT_TOKEN"]; got != "github-token" {
		t.Fatalf("git vault token = %q", got)
	}
}

func TestAddOrgSXVaultEnvIgnoresMissingSkillsNewIntegration(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		sx:  &fakeSXManager{skillsNewErr: sxsync.ErrNotConfigured},
	}
	env := map[string]string{}

	b.addOrgSXVaultEnv(context.Background(), "org_1", agents.Profile{Slug: "reviewer"}, env)

	if len(env) != 0 {
		t.Fatalf("env = %+v, want unchanged", env)
	}
}

func TestAddOrgSXVaultEnvIgnoresRuntimeErrors(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		sx:  &fakeSXManager{skillsNewErr: errors.New("network down")},
	}
	env := map[string]string{}

	b.addOrgSXVaultEnv(context.Background(), "org_1", agents.Profile{Slug: "reviewer"}, env)

	if len(env) != 0 {
		t.Fatalf("env = %+v, want unchanged", env)
	}
}
