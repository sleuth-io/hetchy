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
		"DAYTONA_SNAPSHOT":       "snap",
		"HETCHY_SANDBOX_VERSION": "abc123def456",
		"HETCHY_PUBLIC_BASE_URL": "https://app.example.test",
		"WORKOS_API_KEY":         "sk_test_x",
		"WORKOS_CLIENT_ID":       "client_x",
		"WORKOS_COOKIE_PASSWORD": strings.Repeat("p", 32),
		"WORKOS_REDIRECT_URI":    "http://localhost:8080/callback",
	}
}

func TestLoadConfig_AllRequiredSet(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "WEB_PORT", "DAYTONA_API_URL", "LOGOUT_RETURN_TO", "STRIPE_RETURN_TO")
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
	if cfg.PublicBaseURL() != "https://app.example.test" {
		t.Errorf("PublicBaseURL override = %q", cfg.PublicBaseURL())
	}
	if cfg.SnapshotBase != "snap" || cfg.Snapshot != "snap-abc123def456" || cfg.SandboxSnapshotVersion != "abc123def456" {
		t.Errorf("snapshot config = base %q resolved %q version %q", cfg.SnapshotBase, cfg.Snapshot, cfg.SandboxSnapshotVersion)
	}
}

func TestLoadConfig_AppliesDefaults(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "WEB_PORT", "DAYTONA_API_URL", "LOGOUT_RETURN_TO", "HETCHY_PUBLIC_BASE_URL", "SLACK_OAUTH_REDIRECT_URI", "HETCHY_SX_PUBLIC_VAULT_URL",
		"STRIPE_RETURN_TO",
		"DAYTONA_CACHE_VOLUMES_DISABLED", "DAYTONA_CACHE_VOLUME_PREFIX", "DAYTONA_CACHE_PRUNE_DAYS", "DAYTONA_AUTO_ARCHIVE_MINUTES")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_ENV", "dev")
	t.Setenv("HETCHY_PUBLIC_BASE_URL", "")

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
	if cfg.StripeReturnTo != "http://localhost:8080/" {
		t.Errorf("StripeReturnTo default = %q", cfg.StripeReturnTo)
	}
	if cfg.PublicBaseURL() != "http://localhost:8080" {
		t.Errorf("PublicBaseURL local default = %q", cfg.PublicBaseURL())
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
	if cfg.DaytonaAutoArchiveMinutes != 60 {
		t.Errorf("DaytonaAutoArchiveMinutes default = %d", cfg.DaytonaAutoArchiveMinutes)
	}
}

func TestLoadConfig_StripeReturnToOverridesPublicBase(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS")
	setEnv(t, requiredEnv())
	t.Setenv("LOGOUT_RETURN_TO", "https://app.example.test/")
	t.Setenv("STRIPE_RETURN_TO", "https://billing.example.test/")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PublicBaseURL(); got != "https://app.example.test" {
		t.Fatalf("PublicBaseURL = %q", got)
	}
	if got := cfg.StripeReturnBaseURL(); got != "https://billing.example.test" {
		t.Fatalf("StripeReturnBaseURL = %q", got)
	}
}

func TestPublicBaseURLDevDerivesExternalOrigin(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "LOGOUT_RETURN_TO", "HETCHY_PUBLIC_BASE_URL")
	env := requiredEnv()
	env["HETCHY_ENV"] = "dev"
	env["WORKOS_REDIRECT_URI"] = "https://app.hetchy.ai/callback"
	delete(env, "HETCHY_PUBLIC_BASE_URL")
	setEnv(t, env)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PublicBaseURL(); got != "https://app.hetchy.ai" {
		t.Fatalf("PublicBaseURL = %q, want external WorkOS origin", got)
	}
}

func TestPublicBaseURLProdRequiresExplicitOrigin(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "LOGOUT_RETURN_TO", "HETCHY_PUBLIC_BASE_URL")
	env := requiredEnv()
	delete(env, "HETCHY_PUBLIC_BASE_URL")
	env["HETCHY_ENV"] = "prod"
	env["WORKOS_REDIRECT_URI"] = "https://auth.app.hetchy.ai/callback"
	setEnv(t, env)

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected missing HETCHY_PUBLIC_BASE_URL to fail in prod")
	}
	if !strings.Contains(err.Error(), "HETCHY_PUBLIC_BASE_URL") {
		t.Fatalf("error = %v, want HETCHY_PUBLIC_BASE_URL", err)
	}
}

func TestPublicBaseURLProdDoesNotDeriveOAuthSubdomain(t *testing.T) {
	cfg := Config{
		Env:               "prod",
		WebPort:           "8080",
		LogoutReturnTo:    "http://localhost:8080/",
		WorkOSRedirectURI: "https://auth.app.hetchy.ai/callback",
	}
	if got := cfg.PublicBaseURL(); got != "http://localhost:8080" {
		t.Fatalf("PublicBaseURL = %q, want local fallback without OAuth derivation", got)
	}
}

func TestPublicOriginRejectsInvalidOrigins(t *testing.T) {
	for _, raw := range []string{"app.example.test", "/callback", "mailto:test@example.test", "://broken"} {
		if got := publicOrigin(raw); got != "" {
			t.Fatalf("publicOrigin(%q) = %q, want empty", raw, got)
		}
	}
	if got := publicOrigin("https://app.example.test/root"); got != "https://app.example.test" {
		t.Fatalf("publicOrigin valid URL = %q", got)
	}
}

func TestLoadConfigRejectsInvalidPublicBaseURL(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_PUBLIC_BASE_URL", "app.example.test")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected invalid HETCHY_PUBLIC_BASE_URL to fail")
	}
	if !strings.Contains(err.Error(), "HETCHY_PUBLIC_BASE_URL") {
		t.Fatalf("error = %v, want HETCHY_PUBLIC_BASE_URL", err)
	}
}

func TestPublicBaseURLExplicitOverrideWins(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "LOGOUT_RETURN_TO")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_PUBLIC_BASE_URL", " https://app.example.test/root ")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PublicBaseURL(); got != "https://app.example.test" {
		t.Fatalf("PublicBaseURL = %q, want explicit public origin", got)
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

func TestLoadConfig_StripeSubscriptionPriceIDLegacyDefaultsToStudio(t *testing.T) {
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

func TestLoadConfig_StripeTopupPriceIDs(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "STRIPE_TOPUP_PRICE_ID", "STRIPE_TOPUP_PRICE_IDS")
	setEnv(t, requiredEnv())
	t.Setenv("STRIPE_TOPUP_PRICE_ID", "price_legacy_topup")
	t.Setenv("STRIPE_TOPUP_PRICE_IDS", "starter=price_starter_topup; team=price_team_topup\n growth=price_growth_topup,business=price_business_topup")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := map[string]string{
		"starter":  "price_starter_topup",
		"team":     "price_team_topup",
		"growth":   "price_growth_topup",
		"business": "price_business_topup",
	}
	for plan, priceID := range want {
		if got := cfg.StripeTopupPriceIDs[plan]; got != priceID {
			t.Fatalf("StripeTopupPriceIDs[%q] = %q, want %q", plan, got, priceID)
		}
	}
	if cfg.StripeTopupPriceID != "price_legacy_topup" {
		t.Fatalf("StripeTopupPriceID = %q", cfg.StripeTopupPriceID)
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

func TestLoadConfig_DaytonaAutoArchiveMinutes(t *testing.T) {
	t.Run("parses_positive_int", func(t *testing.T) {
		clearEnv(t, "AUTH_BYPASS")
		setEnv(t, requiredEnv())
		t.Setenv("DAYTONA_AUTO_ARCHIVE_MINUTES", "90")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.DaytonaAutoArchiveMinutes != 90 {
			t.Errorf("DaytonaAutoArchiveMinutes = %d, want 90", cfg.DaytonaAutoArchiveMinutes)
		}
	})
	t.Run("rejects_invalid", func(t *testing.T) {
		for _, value := range []string{"0", "-1", "abc"} {
			t.Run(value, func(t *testing.T) {
				clearEnv(t, "AUTH_BYPASS")
				setEnv(t, requiredEnv())
				t.Setenv("DAYTONA_AUTO_ARCHIVE_MINUTES", value)
				if _, err := LoadConfig(); err == nil {
					t.Fatalf("expected DAYTONA_AUTO_ARCHIVE_MINUTES=%q to fail", value)
				}
			})
		}
	})
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
		"DAYTONA_SNAPSHOT":       "snap",
		"HETCHY_SANDBOX_VERSION": "abc123def456",
	})

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.AuthBypass {
		t.Error("AuthBypass should be true")
	}
}

func TestResolveDaytonaSnapshot(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		base    string
		version string
		want    string
		wantErr bool
	}{
		{name: "prod appends version", env: "prod", base: "universal-coding", version: "abc123def456", want: "universal-coding-abc123def456"},
		{name: "staging appends version", env: "staging", base: "universal-coding", version: "abc123def456", want: "universal-coding-abc123def456"},
		{name: "dev permits exact configured snapshot", env: "dev", base: "universal-coding:local", version: "dev", want: "universal-coding:local"},
		{name: "prod rejects missing version", env: "prod", base: "universal-coding", version: "dev", wantErr: true},
		{name: "prod rejects empty version", env: "prod", base: "universal-coding", version: "", wantErr: true},
		{name: "empty base rejects", env: "prod", base: "", version: "abc123def456", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDaytonaSnapshot(tc.env, tc.base, tc.version)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveDaytonaSnapshot() err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("resolveDaytonaSnapshot() = %q, want %q", got, tc.want)
			}
		})
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
		"DAYTONA_AUTO_ARCHIVE_MINUTES",
		"HETCHY_SANDBOX_VERSION",
		"HETCHY_PUBLIC_BASE_URL",
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
