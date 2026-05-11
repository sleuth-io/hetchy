package bot

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration loaded from the environment.
// Per-org settings (Slack tokens, Anthropic API key, default repo
// selection) live in the database keyed by WorkOS organization_id —
// they're not in this struct. GitHub access is via the GitHub App
// installation cache, also in the database; this struct only carries
// the App-level credentials needed to mint installation tokens.
type Config struct {
	// Env is "dev", "staging", or "prod" (HETCHY_ENV). Defaults to
	// "prod" so a missing var is the safe choice — the dev-only
	// affordances (e.g. the manual Slack-token paste form) only render
	// when this is explicitly "dev". `make bot` sets it to "dev"
	// automatically.
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

	// GitHubAppID / GitHubAppSlug / GitHubAppPrivateKey / GitHubAppWebhookSecret
	// configure the GitHub App used for per-org integrations. One App per
	// environment (dev / staging / prod). Without these, the integration
	// install button is hidden and inbound webhooks are refused.
	//
	// GitHubAppPrivateKey is the multi-line PEM contents of the App's
	// private key — Doppler stores it as a multi-line secret; the env
	// var arrives here with newlines preserved.
	GitHubAppID            int64
	GitHubAppSlug          string
	GitHubAppClientID      string
	GitHubAppPrivateKey    string
	GitHubAppWebhookSecret string

	AuthBypass      bool
	AuthBypassUser  string
	AuthBypassOrg   string
	AuthBypassRole  string
	AuthBypassEmail string

	// S3Bucket and S3Region pin the screenshot-attachment bucket the bot
	// presigns into. Both empty disables the screenshot upload path —
	// callers fall back to the legacy /tmp/hetchy-validate filename
	// references that the agent puts in PR markdown. AWS credentials
	// come from the standard SDK chain (env vars, ~/.aws/credentials,
	// IAM role) — we don't read them here.
	S3Bucket string
	S3Region string

	// SXPublicVaultURL is the git sx vault containing Hetchy's built-in
	// agent personas and role skills. Each sandbox installs it with
	// SX_BOT=<selected agent> before running Claude. The org's skills.new
	// vault, when configured, is installed separately.
	SXPublicVaultURL string
}

const DefaultSXPublicVaultURL = "https://github.com/hetchyhq/hetchy-sx-vault.git"

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
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}

	port := getenvDefault("WEB_PORT", "8080")
	logout := getenvDefault("LOGOUT_RETURN_TO", "http://localhost:"+port+"/")
	cookieSecure := os.Getenv("COOKIE_INSECURE") == ""

	var ghAppID int64
	if v := os.Getenv("GITHUB_APP_ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("GITHUB_APP_ID must be numeric: %w", err)
		}
		ghAppID = n
	}

	return Config{
		Env:                   getenvDefault("HETCHY_ENV", "prod"),
		DaytonaAPIURL:         strings.TrimSpace(os.Getenv("DAYTONA_API_URL")),
		Snapshot:              os.Getenv("DAYTONA_SNAPSHOT"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		WebPort:               port,
		WorkOSAPIKey:          strings.TrimSpace(os.Getenv("WORKOS_API_KEY")),
		WorkOSClientID:        strings.TrimSpace(os.Getenv("WORKOS_CLIENT_ID")),
		WorkOSCookiePassword:  strings.TrimSpace(os.Getenv("WORKOS_COOKIE_PASSWORD")),
		WorkOSRedirectURI:     strings.TrimSpace(os.Getenv("WORKOS_REDIRECT_URI")),
		LogoutReturnTo:        logout,
		CookieSecure:          cookieSecure,
		SecretsEncryptionKey:  strings.TrimSpace(os.Getenv("SECRETS_ENCRYPTION_KEY")),
		SlackSigningSecret:    strings.TrimSpace(os.Getenv("SLACK_SIGNING_SECRET")),
		SlackClientID:         strings.TrimSpace(os.Getenv("SLACK_CLIENT_ID")),
		SlackClientSecret:     strings.TrimSpace(os.Getenv("SLACK_CLIENT_SECRET")),
		SlackOAuthRedirectURI: strings.TrimSpace(os.Getenv("SLACK_OAUTH_REDIRECT_URI")),
		GitHubAppID:           ghAppID,
		GitHubAppSlug:         strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG")),
		GitHubAppClientID:     strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID")),
		// Not trimmed: PEM contents are multi-line and the parser relies on
		// embedded newlines; trimming risks corrupting the key.
		GitHubAppPrivateKey:    os.Getenv("GITHUB_APP_PRIVATE_KEY"),
		GitHubAppWebhookSecret: strings.TrimSpace(os.Getenv("GITHUB_APP_WEBHOOK_SECRET")),
		AuthBypass:             bypass,
		AuthBypassUser:         getenvDefault("AUTH_BYPASS_USER", "user_bypass"),
		AuthBypassOrg:          os.Getenv("AUTH_BYPASS_ORG"),
		AuthBypassRole:         getenvDefault("AUTH_BYPASS_ROLE", "admin"),
		AuthBypassEmail:        getenvDefault("AUTH_BYPASS_EMAIL", "bypass@hetchy.local"),
		S3Bucket:               strings.TrimSpace(os.Getenv("HETCHY_S3_BUCKET")),
		S3Region:               strings.TrimSpace(os.Getenv("HETCHY_S3_REGION")),
		SXPublicVaultURL:       getenvDefaultTrimAllowDisabled("HETCHY_SX_PUBLIC_VAULT_URL", DefaultSXPublicVaultURL),
	}, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvDefaultTrimAllowDisabled(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "disabled", "off", "none", "-":
		return ""
	default:
		return v
	}
}

// PublicBaseURL returns the externally-reachable base URL for the web
// app (no trailing slash). Doppler sets LOGOUT_RETURN_TO per-env (it's
// the canonical "public app root" — required by WorkOS for the logout
// redirect), so we reuse it here for any link that needs to point back
// into our running instance from elsewhere (e.g. Slack deep links).
// Falls back to the local bind URL when LOGOUT_RETURN_TO is unset, so
// dev without Doppler still works.
func (c Config) PublicBaseURL() string {
	if base := strings.TrimSuffix(c.LogoutReturnTo, "/"); base != "" {
		return base
	}
	return "http://localhost:" + c.WebPort
}
