package bot

import (
	"strings"
	"testing"
)

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func requiredEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":           "postgres://localhost/x",
		"SECRETS_ENCRYPTION_KEY": strings.Repeat("k", 32),
		"DAYTONA_SNAPSHOT":       "snap:1",
		"WORKOS_API_KEY":         "sk_test_x",
		"WORKOS_CLIENT_ID":       "client_x",
		"WORKOS_COOKIE_PASSWORD": strings.Repeat("p", 32),
		"WORKOS_REDIRECT_URI":    "http://localhost:8080/callback",
	}
}

func TestLoadConfig_AllRequiredSet(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "WEB_PORT", "DAYTONA_API_URL", "LOGOUT_RETURN_TO")
	setEnv(t, requiredEnv())

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DatabaseURL == "" {
		t.Errorf("required fields not populated: %+v", cfg)
	}
	if cfg.WorkOSAPIKey != "sk_test_x" || cfg.WorkOSClientID != "client_x" {
		t.Errorf("workos fields not populated: %+v", cfg)
	}
	if cfg.AuthBypass {
		t.Error("AuthBypass should be false")
	}
}

func TestLoadConfig_AppliesDefaults(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "WEB_PORT", "DAYTONA_API_URL", "LOGOUT_RETURN_TO", "HETCHY_SX_PUBLIC_VAULT_URL")
	setEnv(t, requiredEnv())

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.WebPort != "8080" {
		t.Errorf("WebPort default = %q", cfg.WebPort)
	}
	if cfg.LogoutReturnTo != "http://localhost:8080/" {
		t.Errorf("LogoutReturnTo default = %q", cfg.LogoutReturnTo)
	}
	if cfg.SXPublicVaultURL != DefaultSXPublicVaultURL {
		t.Errorf("SXPublicVaultURL default = %q", cfg.SXPublicVaultURL)
	}
}

func TestLoadConfig_SXPublicVaultOverride(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_SX_PUBLIC_VAULT_URL", " https://example.com/custom-vault.git ")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SXPublicVaultURL != "https://example.com/custom-vault.git" {
		t.Errorf("SXPublicVaultURL override = %q", cfg.SXPublicVaultURL)
	}
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	clearEnv(t,
		"AUTH_BYPASS",
		"DATABASE_URL", "SECRETS_ENCRYPTION_KEY", "DAYTONA_SNAPSHOT",
		"WORKOS_API_KEY", "WORKOS_CLIENT_ID", "WORKOS_COOKIE_PASSWORD", "WORKOS_REDIRECT_URI",
	)

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for missing env vars")
	}
	for _, key := range []string{"DATABASE_URL", "SECRETS_ENCRYPTION_KEY", "WORKOS_API_KEY"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error missing %s: %v", key, err)
		}
	}
}

func TestLoadConfig_BypassRelaxesWorkOSRequirements(t *testing.T) {
	clearEnv(t, "WORKOS_API_KEY", "WORKOS_CLIENT_ID", "WORKOS_COOKIE_PASSWORD", "WORKOS_REDIRECT_URI")
	setEnv(t, map[string]string{
		"AUTH_BYPASS":            "1",
		"DATABASE_URL":           "postgres://localhost/x",
		"SECRETS_ENCRYPTION_KEY": strings.Repeat("k", 32),
		"DAYTONA_SNAPSHOT":       "snap:1",
	})

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.AuthBypass {
		t.Error("AuthBypass should be true")
	}
}

func TestGetenvDefault(t *testing.T) {
	t.Run("returns_default_when_unset", func(t *testing.T) {
		t.Setenv("SF_TEST_VAR", "")
		got := getenvDefault("SF_TEST_VAR", "fallback")
		if got != "fallback" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("returns_value_when_set", func(t *testing.T) {
		t.Setenv("SF_TEST_VAR", "actual")
		got := getenvDefault("SF_TEST_VAR", "fallback")
		if got != "actual" {
			t.Errorf("got %q", got)
		}
	})
}
