package bot

import (
	"fmt"
	"os"
)

// Config holds runtime configuration loaded from the environment. Per-org
// settings (GitHub repo, GitHub PAT, Slack tokens, SX key, Anthropic API
// key, base branch) live in the database keyed by WorkOS organization_id
// — they're not in this struct.
type Config struct {
	// Env is "dev", "staging", or "prod" (HETCHY_ENV). Defaults to
	// "prod" so a missing var is the safe choice — the dev-only
	// affordances (e.g. the manual Slack-token paste form) only render
	// when this is explicitly "dev". `make bot` and `make bot-tee`
	// set it to "dev" automatically.
	Env string

	DaytonaAPIURL string
	Snapshot      string
	DatabaseURL   string
	WebPort       string

	WorkOSAPIKey         string
	WorkOSClientID       string
	WorkOSCookiePassword string
	WorkOSRedirectURI    string
	LogoutReturnTo       string
	// CookieSecure is the Secure flag on the session cookie. Defaults to
	// true; set COOKIE_INSECURE=1 to disable it for local HTTP dev.
	CookieSecure bool

	SecretsEncryptionKey string

	// SlackSigningSecret is the process-level signing secret of the
	// Hetchy Slack app (one per environment: dev/staging/prod). Used by
	// the HTTP webhook transport to verify inbound Slack requests via
	// HMAC-SHA256. Empty in pure Socket Mode dev setups.
	SlackSigningSecret string
	// SlackClientID and SlackClientSecret are used by the OAuth install
	// flow to exchange an authorization code for a bot token. Empty in
	// pure Socket Mode dev setups.
	SlackClientID     string
	SlackClientSecret string
	// SlackOAuthRedirectURI must match one of the redirect URLs
	// registered on the Slack app. Per-env: e.g.
	// https://hetchy-hetchy-staging.demo.okteto.dev/slack/oauth/callback
	// for staging, https://app.hetchy.ai/slack/oauth/callback for prod.
	SlackOAuthRedirectURI string

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
	cookieSecure := os.Getenv("COOKIE_INSECURE") == ""

	return Config{
		Env:                   getenvDefault("HETCHY_ENV", "prod"),
		DaytonaAPIURL:         os.Getenv("DAYTONA_API_URL"),
		Snapshot:              os.Getenv("DAYTONA_SNAPSHOT"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		WebPort:               port,
		WorkOSAPIKey:          os.Getenv("WORKOS_API_KEY"),
		WorkOSClientID:        os.Getenv("WORKOS_CLIENT_ID"),
		WorkOSCookiePassword:  os.Getenv("WORKOS_COOKIE_PASSWORD"),
		WorkOSRedirectURI:     os.Getenv("WORKOS_REDIRECT_URI"),
		LogoutReturnTo:        logout,
		CookieSecure:          cookieSecure,
		SecretsEncryptionKey:  os.Getenv("SECRETS_ENCRYPTION_KEY"),
		SlackSigningSecret:    os.Getenv("SLACK_SIGNING_SECRET"),
		SlackClientID:         os.Getenv("SLACK_CLIENT_ID"),
		SlackClientSecret:     os.Getenv("SLACK_CLIENT_SECRET"),
		SlackOAuthRedirectURI: os.Getenv("SLACK_OAUTH_REDIRECT_URI"),
		AuthBypass:            bypass,
		AuthBypassUser:        getenvDefault("AUTH_BYPASS_USER", "user_bypass"),
		AuthBypassOrg:         os.Getenv("AUTH_BYPASS_ORG"),
		AuthBypassRole:        getenvDefault("AUTH_BYPASS_ROLE", "admin"),
		AuthBypassEmail:       getenvDefault("AUTH_BYPASS_EMAIL", "bypass@hetchy.local"),
	}, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
