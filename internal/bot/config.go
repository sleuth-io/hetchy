package bot

import (
	"fmt"
	"os"
)

// Config holds runtime configuration loaded from the environment. Per-org
// settings (GitHub repo, GitHub PAT, Slack tokens, SX key, base branch)
// live in the database keyed by WorkOS organization_id — they're not in
// this struct.
type Config struct {
	AnthropicAPIKey string
	DaytonaAPIURL   string
	Snapshot        string
	DatabaseURL     string
	WebPort         string

	WorkOSAPIKey         string
	WorkOSClientID       string
	WorkOSCookiePassword string
	WorkOSRedirectURI    string
	LogoutReturnTo       string

	SecretsEncryptionKey string

	AuthBypass      bool
	AuthBypassUser  string
	AuthBypassOrg   string
	AuthBypassRole  string
	AuthBypassEmail string
}

// LoadConfig reads required and optional env vars. Set AUTH_BYPASS=1 to
// skip the WorkOS round-trip for tests/CI.
func LoadConfig() (Config, error) {
	bypass := os.Getenv("AUTH_BYPASS") != ""

	required := []string{
		"ANTHROPIC_API_KEY",
		"DATABASE_URL",
		"SECRETS_ENCRYPTION_KEY",
		"DAYTONA_SNAPSHOT",
	}
	if !bypass {
		required = append(required,
			"WORKOS_API_KEY",
			"WORKOS_CLIENT_ID",
			"WORKOS_COOKIE_PASSWORD",
			"WORKOS_REDIRECT_URI",
		)
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

	port := getenvDefault("WEB_PORT", "8080")
	logout := getenvDefault("LOGOUT_RETURN_TO", "http://localhost:"+port+"/")

	return Config{
		AnthropicAPIKey:      os.Getenv("ANTHROPIC_API_KEY"),
		DaytonaAPIURL:        os.Getenv("DAYTONA_API_URL"),
		Snapshot:             os.Getenv("DAYTONA_SNAPSHOT"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		WebPort:              port,
		WorkOSAPIKey:         os.Getenv("WORKOS_API_KEY"),
		WorkOSClientID:       os.Getenv("WORKOS_CLIENT_ID"),
		WorkOSCookiePassword: os.Getenv("WORKOS_COOKIE_PASSWORD"),
		WorkOSRedirectURI:    os.Getenv("WORKOS_REDIRECT_URI"),
		LogoutReturnTo:       logout,
		SecretsEncryptionKey: os.Getenv("SECRETS_ENCRYPTION_KEY"),
		AuthBypass:           bypass,
		AuthBypassUser:       getenvDefault("AUTH_BYPASS_USER", "user_bypass"),
		AuthBypassOrg:        os.Getenv("AUTH_BYPASS_ORG"),
		AuthBypassRole:       getenvDefault("AUTH_BYPASS_ROLE", "admin"),
		AuthBypassEmail:      getenvDefault("AUTH_BYPASS_EMAIL", "bypass@hetchy.local"),
	}, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
