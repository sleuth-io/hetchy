package bot

import (
	"os"
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
	clearEnv(t, "AUTH_BYPASS", "WEB_PORT", "DAYTONA_API_URL", "LOGOUT_RETURN_TO", "HETCHY_SX_PUBLIC_VAULT_URL",
		"DAYTONA_CACHE_VOLUMES_DISABLED", "DAYTONA_CACHE_VOLUME_PREFIX", "DAYTONA_CACHE_PRUNE_DAYS")
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
	if cfg.DaytonaCacheVolumesDisabled {
		t.Error("DaytonaCacheVolumesDisabled should default false")
	}
	if cfg.DaytonaCacheVolumePrefix != "hetchy-cache" {
		t.Errorf("DaytonaCacheVolumePrefix default = %q", cfg.DaytonaCacheVolumePrefix)
	}
	if cfg.DaytonaCachePruneDays != 30 {
		t.Errorf("DaytonaCachePruneDays default = %d", cfg.DaytonaCachePruneDays)
	}
}

func TestLoadConfig_DaytonaCacheOverrides(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS")
	setEnv(t, requiredEnv())
	t.Setenv("DAYTONA_CACHE_VOLUMES_DISABLED", "1")
	t.Setenv("DAYTONA_CACHE_VOLUME_PREFIX", " cache-prefix ")
	t.Setenv("DAYTONA_CACHE_PRUNE_DAYS", "14")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.DaytonaCacheVolumesDisabled {
		t.Error("DaytonaCacheVolumesDisabled should be true")
	}
	if cfg.DaytonaCacheVolumePrefix != "cache-prefix" {
		t.Errorf("DaytonaCacheVolumePrefix = %q", cfg.DaytonaCacheVolumePrefix)
	}
	if cfg.DaytonaCachePruneDays != 14 {
		t.Errorf("DaytonaCachePruneDays = %d", cfg.DaytonaCachePruneDays)
	}
}

func TestLoadConfig_StripeSubscriptionPriceIDs(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "STRIPE_SUBSCRIPTION_PRICE_ID", "STRIPE_SUBSCRIPTION_PRICE_IDS")
	setEnv(t, requiredEnv())
	t.Setenv("STRIPE_SUBSCRIPTION_PRICE_ID", "price_legacy_team")
	t.Setenv("STRIPE_SUBSCRIPTION_PRICE_IDS", "starter=price_starter; team=price_team\n growth=price_growth,business=price_business")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := map[string]string{
		"starter":  "price_starter",
		"team":     "price_team",
		"growth":   "price_growth",
		"business": "price_business",
	}
	for plan, priceID := range want {
		if got := cfg.StripeSubscriptionPriceIDs[plan]; got != priceID {
			t.Fatalf("StripeSubscriptionPriceIDs[%q] = %q, want %q", plan, got, priceID)
		}
	}
}

func TestLoadConfig_StripeSubscriptionPriceIDLegacyDefaultsToTeam(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "STRIPE_SUBSCRIPTION_PRICE_ID", "STRIPE_SUBSCRIPTION_PRICE_IDS")
	setEnv(t, requiredEnv())
	t.Setenv("STRIPE_SUBSCRIPTION_PRICE_ID", "price_team")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.StripeSubscriptionPriceIDs["team"]; got != "price_team" {
		t.Fatalf("legacy StripeSubscriptionPriceID mapped to team = %q", got)
	}
}

func TestLoadConfig_DaytonaCachePruneDaysRejectsInvalid(t *testing.T) {
	for _, value := range []string{"0", "-1", "abc"} {
		t.Run(value, func(t *testing.T) {
			clearEnv(t, "AUTH_BYPASS")
			setEnv(t, requiredEnv())
			t.Setenv("DAYTONA_CACHE_PRUNE_DAYS", value)
			if _, err := LoadConfig(); err == nil {
				t.Fatalf("expected DAYTONA_CACHE_PRUNE_DAYS=%q to fail", value)
			}
		})
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

func TestLoadConfig_SXPublicVaultDisabled(t *testing.T) {
	for _, value := range []string{"disabled", "off", "none", "-"} {
		t.Run(value, func(t *testing.T) {
			clearEnv(t, "AUTH_BYPASS")
			setEnv(t, requiredEnv())
			t.Setenv("HETCHY_SX_PUBLIC_VAULT_URL", " "+value+" ")

			cfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.SXPublicVaultURL != "" {
				t.Errorf("SXPublicVaultURL disabled value = %q", cfg.SXPublicVaultURL)
			}
		})
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

func TestComposeForwardsArtifactUploadEnv(t *testing.T) {
	raw, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	compose := string(raw)
	for _, key := range []string{
		"HETCHY_S3_BUCKET",
		"HETCHY_S3_REGION",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"DATABASE_MAX_CONNS",
		"DAYTONA_CACHE_VOLUMES_DISABLED",
		"DAYTONA_CACHE_VOLUME_PREFIX",
		"DAYTONA_CACHE_PRUNE_DAYS",
	} {
		want := key + ": ${" + key + ":-}"
		if !strings.Contains(compose, want) {
			t.Errorf("docker-compose.yml does not forward %s to the hetchy service", key)
		}
	}
}

func TestLoadConfig_DatabaseMaxConns(t *testing.T) {
	t.Run("unset_leaves_zero", func(t *testing.T) {
		clearEnv(t, "AUTH_BYPASS", "DATABASE_MAX_CONNS")
		setEnv(t, requiredEnv())
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.DatabaseMaxConns != 0 {
			t.Errorf("DatabaseMaxConns = %d, want 0", cfg.DatabaseMaxConns)
		}
	})
	t.Run("parses_positive_int", func(t *testing.T) {
		clearEnv(t, "AUTH_BYPASS")
		setEnv(t, requiredEnv())
		t.Setenv("DATABASE_MAX_CONNS", "25")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.DatabaseMaxConns != 25 {
			t.Errorf("DatabaseMaxConns = %d, want 25", cfg.DatabaseMaxConns)
		}
	})
	t.Run("rejects_zero_and_negative", func(t *testing.T) {
		for _, v := range []string{"0", "-1", "abc"} {
			t.Run(v, func(t *testing.T) {
				clearEnv(t, "AUTH_BYPASS")
				setEnv(t, requiredEnv())
				t.Setenv("DATABASE_MAX_CONNS", v)
				if _, err := LoadConfig(); err == nil {
					t.Fatalf("expected error for DATABASE_MAX_CONNS=%q", v)
				}
			})
		}
	})
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
