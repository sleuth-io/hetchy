package bot

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestLoadCookieSecureRequiresExplicitInsecureFlag(t *testing.T) {
	t.Setenv("WORKOS_REDIRECT_URI", "")
	t.Setenv("COOKIE_INSECURE", "0")
	if !loadCookieSecure("https://app.example.com", "") {
		t.Fatal("COOKIE_INSECURE=0 should not disable secure cookies")
	}

	t.Setenv("COOKIE_INSECURE", "1")
	if loadCookieSecure("https://app.example.com", "") {
		t.Fatal("COOKIE_INSECURE=1 should disable secure cookies")
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
		"COOKIE_INSECURE",
		"HETCHY_JOB_DISPATCH_INTERVAL_SECONDS",
		"HETCHY_JOB_DISPATCH_LIMIT",
		"HETCHY_JOB_DISPATCH_CONCURRENCY",
	} {
		want := key + ": ${" + key + ":-}"
		if !strings.Contains(compose, want) {
			t.Errorf("docker-compose.yml does not forward %s to the hetchy service", key)
		}
	}
	if want := "HETCHY_PUBLIC_BASE_URL: ${HETCHY_PUBLIC_BASE_URL:-http://localhost:8080}"; !strings.Contains(compose, want) {
		t.Errorf("docker-compose.yml does not provide self-host public URL default")
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

func TestLoadConfig_JobDispatchSettings(t *testing.T) {
	setEnv(t, requiredEnv())
	clearEnv(t, "HETCHY_JOB_DISPATCH_INTERVAL_SECONDS", "HETCHY_JOB_DISPATCH_LIMIT", "HETCHY_JOB_DISPATCH_CONCURRENCY")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if defaultJobDispatchIntervalSeconds != 300 {
		t.Fatalf("defaultJobDispatchIntervalSeconds = %d, want 300", defaultJobDispatchIntervalSeconds)
	}
	if cfg.JobDispatchIntervalSeconds != defaultJobDispatchIntervalSeconds ||
		cfg.JobDispatchLimit != defaultJobDispatchLimit ||
		cfg.JobDispatchConcurrency != defaultJobDispatchConcurrency {
		t.Errorf("defaults not applied: interval=%d limit=%d concurrency=%d",
			cfg.JobDispatchIntervalSeconds, cfg.JobDispatchLimit, cfg.JobDispatchConcurrency)
	}

	t.Setenv("HETCHY_JOB_DISPATCH_INTERVAL_SECONDS", "0")
	t.Setenv("HETCHY_JOB_DISPATCH_LIMIT", "9")
	t.Setenv("HETCHY_JOB_DISPATCH_CONCURRENCY", "2")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with overrides: %v", err)
	}
	if cfg.JobDispatchIntervalSeconds != 0 || cfg.JobDispatchLimit != 9 || cfg.JobDispatchConcurrency != 2 {
		t.Errorf("overrides not applied: interval=%d limit=%d concurrency=%d",
			cfg.JobDispatchIntervalSeconds, cfg.JobDispatchLimit, cfg.JobDispatchConcurrency)
	}

	for env, bad := range map[string]string{
		"HETCHY_JOB_DISPATCH_INTERVAL_SECONDS": "-1",
		"HETCHY_JOB_DISPATCH_LIMIT":            "0",
		"HETCHY_JOB_DISPATCH_CONCURRENCY":      "nope",
	} {
		t.Run(env, func(t *testing.T) {
			setEnv(t, requiredEnv())
			clearEnv(t, "HETCHY_JOB_DISPATCH_INTERVAL_SECONDS", "HETCHY_JOB_DISPATCH_LIMIT", "HETCHY_JOB_DISPATCH_CONCURRENCY")
			t.Setenv(env, bad)
			if _, err := LoadConfig(); err == nil {
				t.Errorf("%s=%q must be rejected", env, bad)
			}
		})
	}
}

func TestLoadAuthModeEnv(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "default", want: "workos"},
		{name: "workos", raw: " WorkOS ", want: "workos"},
		{name: "local", raw: "LOCAL", want: "local"},
		{name: "invalid", raw: "magic", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HETCHY_AUTH_MODE", tt.raw)
			got, err := loadAuthModeEnv()
			if (err != nil) != tt.wantErr {
				t.Fatalf("loadAuthModeEnv() err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("loadAuthModeEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequiredConfigEnvKeys(t *testing.T) {
	tests := []struct {
		name     string
		bypass   bool
		authMode string
		env      string
		want     []string
	}{
		{
			name:     "prod workos",
			authMode: "workos",
			env:      "prod",
			want: []string{
				"DATABASE_URL", "SECRETS_ENCRYPTION_KEY", "DAYTONA_SNAPSHOT",
				"WORKOS_API_KEY", "WORKOS_CLIENT_ID", "WORKOS_COOKIE_PASSWORD",
				"WORKOS_REDIRECT_URI", "HETCHY_PUBLIC_BASE_URL",
			},
		},
		{
			name:     "dev local",
			authMode: "local",
			env:      "dev",
			want:     []string{"DATABASE_URL", "SECRETS_ENCRYPTION_KEY", "DAYTONA_SNAPSHOT"},
		},
		{
			name:     "bypass prod",
			bypass:   true,
			authMode: "workos",
			env:      "prod",
			want:     []string{"DATABASE_URL", "SECRETS_ENCRYPTION_KEY", "DAYTONA_SNAPSHOT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requiredConfigEnvKeys(tt.bypass, tt.authMode, tt.env)
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("requiredConfigEnvKeys() = %v, want %v", got, want)
			}
		})
	}
}

func TestMissingRequiredConfigEnvTrimsValues(t *testing.T) {
	t.Setenv("SET_VALUE", " value ")
	t.Setenv("SPACES_ONLY", "   ")
	t.Setenv("EMPTY_VALUE", "")

	got := missingRequiredConfigEnv([]string{"SET_VALUE", "SPACES_ONLY", "EMPTY_VALUE"})
	want := []string{"SPACES_ONLY", "EMPTY_VALUE"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("missingRequiredConfigEnv() = %v, want %v", got, want)
	}
}

func TestLoadCookieSecureSources(t *testing.T) {
	t.Run("http WorkOS redirect disables secure", func(t *testing.T) {
		t.Setenv("COOKIE_INSECURE", "")
		t.Setenv("WORKOS_REDIRECT_URI", "http://localhost:8080/callback")
		if loadCookieSecure("https://app.example.test", "https://app.example.test/logout") {
			t.Fatal("expected false")
		}
	})

	t.Run("http public base disables secure", func(t *testing.T) {
		t.Setenv("COOKIE_INSECURE", "")
		t.Setenv("WORKOS_REDIRECT_URI", "https://app.example.test/callback")
		if loadCookieSecure("http://localhost:8080", "https://app.example.test/logout") {
			t.Fatal("expected false")
		}
	})

	t.Run("http logout URL disables secure", func(t *testing.T) {
		t.Setenv("COOKIE_INSECURE", "")
		t.Setenv("WORKOS_REDIRECT_URI", "https://app.example.test/callback")
		if loadCookieSecure("https://app.example.test", "http://localhost:8080/logout") {
			t.Fatal("expected false")
		}
	})

	t.Run("https only keeps secure enabled", func(t *testing.T) {
		t.Setenv("COOKIE_INSECURE", "")
		t.Setenv("WORKOS_REDIRECT_URI", "https://app.example.test/callback")
		if !loadCookieSecure("https://app.example.test", "https://app.example.test/logout") {
			t.Fatal("expected true")
		}
	})
}

func TestTruthyEnv(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", " yes ", "on"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HETCHY_TEST_BOOL", value)
			if !truthyEnv("HETCHY_TEST_BOOL") {
				t.Fatalf("truthyEnv(%q) = false", value)
			}
		})
	}
	for _, value := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HETCHY_TEST_BOOL", value)
			if truthyEnv("HETCHY_TEST_BOOL") {
				t.Fatalf("truthyEnv(%q) = true", value)
			}
		})
	}
}
