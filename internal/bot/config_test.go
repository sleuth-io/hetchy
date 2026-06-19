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
		"STRIPE_RETURN_TO", "HETCHY_ARTIFACT_DIR", "HETCHY_PR_STATE_POLL_INTERVAL_SECONDS", "HETCHY_PR_STATE_POLL_LIMIT",
		"DAYTONA_CACHE_VOLUMES_DISABLED", "DAYTONA_CACHE_VOLUME_PREFIX", "DAYTONA_CACHE_PRUNE_DAYS", "DAYTONA_AUTO_ARCHIVE_MINUTES",
		"HETCHY_SX_CACHE_DIR", "SX_CACHE_DIR", "HETCHY_SX_CACHE_MIN_FREE_MB", "HETCHY_SX_GIT_OPERATION_TIMEOUT_SECONDS", "HETCHY_SX_GIT_MAX_CONCURRENT_OPS")
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
	if cfg.DaytonaCachePruneDays != 10 {
		t.Errorf("DaytonaCachePruneDays default = %d", cfg.DaytonaCachePruneDays)
	}
	if cfg.DaytonaAutoArchiveMinutes != 60 {
		t.Errorf("DaytonaAutoArchiveMinutes default = %d", cfg.DaytonaAutoArchiveMinutes)
	}
	if cfg.SXCacheDir != "" {
		t.Errorf("SXCacheDir default = %q", cfg.SXCacheDir)
	}
	if cfg.SXCacheMinFreeBytes != 0 {
		t.Errorf("SXCacheMinFreeBytes default = %d", cfg.SXCacheMinFreeBytes)
	}
	if cfg.SXGitOperationTimeoutSeconds != defaultSXGitOperationTimeoutSeconds {
		t.Errorf("SXGitOperationTimeoutSeconds default = %d", cfg.SXGitOperationTimeoutSeconds)
	}
	if cfg.SXGitMaxConcurrentOps != defaultSXGitMaxConcurrentOps {
		t.Errorf("SXGitMaxConcurrentOps default = %d", cfg.SXGitMaxConcurrentOps)
	}
	if cfg.ArtifactDir != "" {
		t.Errorf("ArtifactDir default = %q", cfg.ArtifactDir)
	}
	if cfg.PRStatePollIntervalSeconds != defaultPRStatePollIntervalSeconds {
		t.Errorf("PRStatePollIntervalSeconds default = %d", cfg.PRStatePollIntervalSeconds)
	}
	if cfg.PRStatePollLimit != defaultPRStatePollLimit {
		t.Errorf("PRStatePollLimit default = %d", cfg.PRStatePollLimit)
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

func TestLoadConfig_TrustedProxy(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "HETCHY_TRUSTED_PROXY")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_TRUSTED_PROXY", "true")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.TrustedProxy {
		t.Fatal("TrustedProxy should be true")
	}
}

func TestLoadConfig_ArtifactAndPRStatePollOverrides(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_ARTIFACT_DIR", " /data/hetchy/artifacts ")
	t.Setenv("HETCHY_PR_STATE_POLL_INTERVAL_SECONDS", "120")
	t.Setenv("HETCHY_PR_STATE_POLL_LIMIT", "25")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ArtifactDir != "/data/hetchy/artifacts" {
		t.Fatalf("ArtifactDir = %q", cfg.ArtifactDir)
	}
	if cfg.PRStatePollIntervalSeconds != 120 {
		t.Fatalf("PRStatePollIntervalSeconds = %d", cfg.PRStatePollIntervalSeconds)
	}
	if cfg.PRStatePollLimit != 25 {
		t.Fatalf("PRStatePollLimit = %d", cfg.PRStatePollLimit)
	}
}

func TestLoadConfig_PRStatePollValidation(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value string
	}{
		{"HETCHY_PR_STATE_POLL_INTERVAL_SECONDS", "-1"},
		{"HETCHY_PR_STATE_POLL_INTERVAL_SECONDS", "nope"},
		{"HETCHY_PR_STATE_POLL_LIMIT", "0"},
		{"HETCHY_PR_STATE_POLL_LIMIT", "nope"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			clearEnv(t, "AUTH_BYPASS", "HETCHY_PR_STATE_POLL_INTERVAL_SECONDS", "HETCHY_PR_STATE_POLL_LIMIT")
			setEnv(t, requiredEnv())
			t.Setenv(tc.key, tc.value)
			_, err := LoadConfig()
			if err == nil {
				t.Fatal("expected LoadConfig error")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error = %v, want %s", err, tc.key)
			}
		})
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

func TestLoadConfig_SXGitCacheOverrides(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "SX_CACHE_DIR")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_ENV", "dev")
	t.Setenv("HETCHY_SX_CACHE_DIR", " /mnt/volume/sx-cache ")
	t.Setenv("HETCHY_SX_CACHE_MIN_FREE_MB", "128")
	t.Setenv("HETCHY_SX_GIT_OPERATION_TIMEOUT_SECONDS", "45")
	t.Setenv("HETCHY_SX_GIT_MAX_CONCURRENT_OPS", "3")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SXCacheDir != "/mnt/volume/sx-cache" {
		t.Fatalf("SXCacheDir = %q", cfg.SXCacheDir)
	}
	if cfg.SXCacheMinFreeBytes != 128<<20 {
		t.Fatalf("SXCacheMinFreeBytes = %d", cfg.SXCacheMinFreeBytes)
	}
	if cfg.SXGitOperationTimeoutSeconds != 45 {
		t.Fatalf("SXGitOperationTimeoutSeconds = %d", cfg.SXGitOperationTimeoutSeconds)
	}
	if cfg.SXGitMaxConcurrentOps != 3 {
		t.Fatalf("SXGitMaxConcurrentOps = %d", cfg.SXGitMaxConcurrentOps)
	}
}

func TestLoadConfig_SXCacheDirFallsBackToSXCacheDir(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "HETCHY_SX_CACHE_DIR")
	setEnv(t, requiredEnv())
	t.Setenv("HETCHY_ENV", "dev")
	t.Setenv("SX_CACHE_DIR", "/tmp/sx-cache")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SXCacheDir != "/tmp/sx-cache" {
		t.Fatalf("SXCacheDir = %q", cfg.SXCacheDir)
	}
	if cfg.SXCacheMinFreeBytes != defaultSXCacheMinFreeMiB<<20 {
		t.Fatalf("SXCacheMinFreeBytes = %d", cfg.SXCacheMinFreeBytes)
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

func TestLoadConfig_SXGitCacheRejectsInvalid(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"HETCHY_SX_CACHE_MIN_FREE_MB", "-1"},
		{"HETCHY_SX_CACHE_MIN_FREE_MB", "abc"},
		{"HETCHY_SX_GIT_OPERATION_TIMEOUT_SECONDS", "0"},
		{"HETCHY_SX_GIT_OPERATION_TIMEOUT_SECONDS", "abc"},
		{"HETCHY_SX_GIT_MAX_CONCURRENT_OPS", "0"},
		{"HETCHY_SX_GIT_MAX_CONCURRENT_OPS", "abc"},
	}
	for _, tc := range tests {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			clearEnv(t, "AUTH_BYPASS")
			setEnv(t, requiredEnv())
			t.Setenv("HETCHY_ENV", "dev")
			t.Setenv(tc.key, tc.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatalf("expected %s=%q to fail", tc.key, tc.value)
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

func TestLoadConfig_LocalAuthRelaxesWorkOSRequirements(t *testing.T) {
	clearEnv(t, "AUTH_BYPASS", "WORKOS_API_KEY", "WORKOS_CLIENT_ID", "WORKOS_COOKIE_PASSWORD", "WORKOS_REDIRECT_URI")
	setEnv(t, map[string]string{
		"HETCHY_ENV":             "dev",
		"HETCHY_AUTH_MODE":       "local",
		"HETCHY_PUBLIC_BASE_URL": "http://localhost:8080",
		"DATABASE_URL":           "postgres://localhost/x",
		"SECRETS_ENCRYPTION_KEY": strings.Repeat("k", 32),
		"DAYTONA_SNAPSHOT":       "snap",
		"HETCHY_SANDBOX_VERSION": "abc123def456",
	})

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AuthMode != "local" {
		t.Fatalf("AuthMode = %q, want local", cfg.AuthMode)
	}
	if cfg.WorkOSAPIKey != "" || cfg.WorkOSClientID != "" || cfg.WorkOSRedirectURI != "" {
		t.Fatalf("local auth should not require WorkOS config: %+v", cfg)
	}
	if cfg.CookieSecure {
		t.Fatal("CookieSecure should be false for http local auth base URL")
	}
}
