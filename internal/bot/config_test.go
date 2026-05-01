package bot

import (
	"strings"
	"testing"
)

// requiredFullEnv returns env vars sufficient to satisfy LoadConfig with Slack on.
func requiredFullEnv() map[string]string {
	return map[string]string{
		"ANTHROPIC_API_KEY":     "ant",
		"GITHUB_TOKEN":          "ghp",
		"GITHUB_REPO":           "owner/repo",
		"SLACK_BOT_OAUTH_TOKEN": "xoxb",
		"SLACK_SOCKET_TOKEN":    "xapp",
	}
}

// setEnv applies env, restores prior values on cleanup. Empty value means unset.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// clearEnv unsets a list of env vars for the test.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestLoadConfig_AllRequiredSet(t *testing.T) {
	clearEnv(t, "DISABLE_SLACK", "GITHUB_BASE_BRANCH", "DAYTONA_SNAPSHOT", "DAYTONA_API_URL", "WEB_PORT", "STATE_FILE", "SX_KEY")
	setEnv(t, requiredFullEnv())

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AnthropicAPIKey != "ant" || cfg.GitHubToken != "ghp" || cfg.GitHubRepo != "owner/repo" {
		t.Errorf("required fields not populated: %+v", cfg)
	}
	if cfg.SlackBotToken != "xoxb" || cfg.SlackSocketToken != "xapp" {
		t.Errorf("slack tokens not populated: %+v", cfg)
	}
	if cfg.DisableSlack {
		t.Error("DisableSlack should be false")
	}
}

func TestLoadConfig_AppliesDefaults(t *testing.T) {
	clearEnv(t, "DISABLE_SLACK", "GITHUB_BASE_BRANCH", "DAYTONA_SNAPSHOT", "DAYTONA_API_URL", "WEB_PORT", "STATE_FILE", "SX_KEY")
	setEnv(t, requiredFullEnv())

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.BaseBranch != "main" {
		t.Errorf("BaseBranch default = %q, want main", cfg.BaseBranch)
	}
	if cfg.WebPort != "8080" {
		t.Errorf("WebPort default = %q, want 8080", cfg.WebPort)
	}
	if cfg.StateFile != "state.json" {
		t.Errorf("StateFile default = %q, want state.json", cfg.StateFile)
	}
	if cfg.Snapshot != "ghcr.io/owner/repo/sandbox:latest" {
		t.Errorf("Snapshot default = %q, want ghcr.io/owner/repo/sandbox:latest", cfg.Snapshot)
	}
}

func TestLoadConfig_OverridesDefaults(t *testing.T) {
	setEnv(t, requiredFullEnv())
	setEnv(t, map[string]string{
		"GITHUB_BASE_BRANCH": "trunk",
		"DAYTONA_SNAPSHOT":   "claude-playwright",
		"DAYTONA_API_URL":    "http://localhost:3000/api",
		"WEB_PORT":           "9090",
		"STATE_FILE":         "/tmp/sf.json",
		"SX_KEY":             "secret-token",
	})

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.BaseBranch != "trunk" {
		t.Errorf("BaseBranch = %q", cfg.BaseBranch)
	}
	if cfg.Snapshot != "claude-playwright" {
		t.Errorf("Snapshot = %q", cfg.Snapshot)
	}
	if cfg.DaytonaAPIURL != "http://localhost:3000/api" {
		t.Errorf("DaytonaAPIURL = %q", cfg.DaytonaAPIURL)
	}
	if cfg.WebPort != "9090" {
		t.Errorf("WebPort = %q", cfg.WebPort)
	}
	if cfg.StateFile != "/tmp/sf.json" {
		t.Errorf("StateFile = %q", cfg.StateFile)
	}
	if cfg.SXKey != "secret-token" {
		t.Errorf("SXKey = %q", cfg.SXKey)
	}
}

func TestLoadConfig_DisableSlackSkipsTokenCheck(t *testing.T) {
	clearEnv(t, "SLACK_BOT_OAUTH_TOKEN", "SLACK_SOCKET_TOKEN")
	setEnv(t, map[string]string{
		"DISABLE_SLACK":     "1",
		"ANTHROPIC_API_KEY": "ant",
		"GITHUB_TOKEN":      "ghp",
		"GITHUB_REPO":       "owner/repo",
	})

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.DisableSlack {
		t.Error("DisableSlack should be true")
	}
	if cfg.SlackBotToken != "" || cfg.SlackSocketToken != "" {
		t.Errorf("slack tokens should be empty: %+v", cfg)
	}
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	clearEnv(t, "DISABLE_SLACK", "ANTHROPIC_API_KEY", "GITHUB_TOKEN", "GITHUB_REPO", "SLACK_BOT_OAUTH_TOKEN", "SLACK_SOCKET_TOKEN")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for missing env vars")
	}
	msg := err.Error()
	for _, key := range []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN", "GITHUB_REPO", "SLACK_BOT_OAUTH_TOKEN", "SLACK_SOCKET_TOKEN"} {
		if !strings.Contains(msg, key) {
			t.Errorf("error %q missing %s", msg, key)
		}
	}
}

func TestLoadConfig_MissingSomeRequired(t *testing.T) {
	clearEnv(t, "DISABLE_SLACK", "GITHUB_REPO")
	setEnv(t, map[string]string{
		"ANTHROPIC_API_KEY":     "ant",
		"GITHUB_TOKEN":          "ghp",
		"SLACK_BOT_OAUTH_TOKEN": "xoxb",
		"SLACK_SOCKET_TOKEN":    "xapp",
	})

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "GITHUB_REPO") {
		t.Errorf("error should mention GITHUB_REPO: %v", err)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN", "SLACK_BOT_OAUTH_TOKEN", "SLACK_SOCKET_TOKEN"} {
		if strings.Contains(err.Error(), key) {
			t.Errorf("error should not mention %s (it was set): %v", key, err)
		}
	}
}

func TestGetenvDefault(t *testing.T) {
	t.Run("returns_default_when_unset", func(t *testing.T) {
		t.Setenv("SF_TEST_VAR", "")
		got := getenvDefault("SF_TEST_VAR", "fallback")
		if got != "fallback" {
			t.Errorf("got %q, want fallback", got)
		}
	})
	t.Run("returns_value_when_set", func(t *testing.T) {
		t.Setenv("SF_TEST_VAR", "actual")
		got := getenvDefault("SF_TEST_VAR", "fallback")
		if got != "actual" {
			t.Errorf("got %q, want actual", got)
		}
	})
}
