package bot

import (
	"fmt"
	"os"
)

// Config holds runtime configuration loaded from the environment.
type Config struct {
	SlackBotToken    string
	SlackSocketToken string
	AnthropicAPIKey  string
	GitHubToken      string
	GitHubRepo       string
	BaseBranch       string
	Snapshot         string
	DaytonaAPIURL    string
	WebPort          string
	DisableSlack     bool
}

// LoadConfig reads required and optional env vars. It returns an error listing
// any required variables that are missing rather than fatal-exiting. Set
// DISABLE_SLACK=1 to drop the Slack tokens from the required list and run the
// web UI alone.
func LoadConfig() (Config, error) {
	disableSlack := os.Getenv("DISABLE_SLACK") != ""

	required := []string{
		"ANTHROPIC_API_KEY",
		"GITHUB_TOKEN",
		"GITHUB_REPO",
	}
	if !disableSlack {
		required = append(required, "SLACK_BOT_OAUTH_TOKEN", "SLACK_SOCKET_TOKEN")
	}
	var missing []string
	for _, key := range required {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}

	repo := os.Getenv("GITHUB_REPO")
	return Config{
		SlackBotToken:    os.Getenv("SLACK_BOT_OAUTH_TOKEN"),
		SlackSocketToken: os.Getenv("SLACK_SOCKET_TOKEN"),
		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		GitHubToken:      os.Getenv("GITHUB_TOKEN"),
		GitHubRepo:       repo,
		BaseBranch:       getenvDefault("GITHUB_BASE_BRANCH", "main"),
		Snapshot:         getenvDefault("DAYTONA_SNAPSHOT", fmt.Sprintf("ghcr.io/%s/sandbox:latest", repo)),
		DaytonaAPIURL:    os.Getenv("DAYTONA_API_URL"),
		WebPort:          getenvDefault("WEB_PORT", "8080"),
		DisableSlack:     disableSlack,
	}, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
