// Package githubapp wraps the GitHub App auth + REST client for Hetchy.
//
// One App per environment (dev/staging/prod), configured from env vars:
//
//	GITHUB_APP_ID                — numeric App ID
//	GITHUB_APP_SLUG              — slug used to build install URLs
//	GITHUB_APP_PRIVATE_KEY       — PEM contents of the App's private key
//	GITHUB_APP_WEBHOOK_SECRET    — HMAC secret for inbound webhooks
//
// Per-installation auth happens at request time: we mint a short-lived
// JWT signed with the private key, exchange it for an installation
// access token (1h TTL), and hand that token to either git/gh inside
// the sandbox or a per-call REST client. Tokens are cached until 5min
// before expiry to avoid hitting GitHub on every request.
package githubapp

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Config carries the four secrets the App needs at process start.
// PrivateKeyPEM is the full multi-line PEM contents of the App's
// private key (downloaded once from the GitHub App settings page).
type Config struct {
	AppID         int64
	Slug          string
	ClientID      string
	PrivateKeyPEM string
	WebhookSecret string
}

// App is the long-lived GitHub App handle. Construct one at process
// start and pass it everywhere that needs to authenticate as the App
// or as one of its installations.
type App struct {
	cfg    Config
	key    *rsa.PrivateKey
	http   *http.Client
	log    *slog.Logger
	tokens *tokenCache
}

// New parses the PEM private key and returns an App. Returns an error
// if AppID/Slug/PrivateKeyPEM/WebhookSecret are missing or the PEM
// fails to parse — fail loudly at startup rather than at the first
// inbound install attempt.
func New(cfg Config, log *slog.Logger) (*App, error) {
	if cfg.AppID == 0 {
		return nil, errors.New("githubapp: AppID is required")
	}
	if cfg.Slug == "" {
		return nil, errors.New("githubapp: Slug is required")
	}
	if cfg.PrivateKeyPEM == "" {
		return nil, errors.New("githubapp: PrivateKeyPEM is required")
	}
	if cfg.WebhookSecret == "" {
		return nil, errors.New("githubapp: WebhookSecret is required")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(cfg.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return &App{
		cfg:    cfg,
		key:    key,
		http:   &http.Client{Timeout: 30 * time.Second},
		log:    log,
		tokens: newTokenCache(),
	}, nil
}

// AppID exposes the configured numeric App ID.
func (a *App) AppID() int64 { return a.cfg.AppID }

// Slug returns the App slug used to build the install URL.
func (a *App) Slug() string { return a.cfg.Slug }

// InstallURL returns the canonical "Install this GitHub App" URL with
// the supplied state token appended. GitHub sends the user back to
// the Setup URL configured on the App with the same state in its
// query, which lets us tie the install to the originating Hetchy org
// + user without ever trusting the installation_id alone.
func (a *App) InstallURL(state string) string {
	return fmt.Sprintf("https://github.com/apps/%s/installations/new?state=%s",
		a.cfg.Slug, state)
}

// jwtTTL is the lifetime of the App-level JWT used to mint
// installation tokens. GitHub allows up to 10 minutes; we use 9 to
// give ourselves a clock-skew margin without re-minting too often.
const jwtTTL = 9 * time.Minute

// appJWT mints a JWT signed with the App's private key, suitable for
// authenticating as the App itself (e.g. to mint installation tokens).
// The token is short-lived; do not cache it across requests.
func (a *App) appJWT() (string, error) {
	now := time.Now().Add(-30 * time.Second) // tolerate small clock skew
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtTTL)),
		Issuer:    strconv.FormatInt(a.cfg.AppID, 10),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signed, nil
}
