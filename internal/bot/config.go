package bot

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/buildinfo"
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
	// SnapshotBase is the stable Daytona snapshot base name from
	// DAYTONA_SNAPSHOT. Snapshot is the resolved versioned snapshot name used
	// for sandbox creation.
	SnapshotBase           string
	Snapshot               string
	SandboxSnapshotVersion string
	// DaytonaCacheVolumesDisabled disables dependency cache volume
	// mounting when DAYTONA_CACHE_VOLUMES_DISABLED=1.
	DaytonaCacheVolumesDisabled bool
	// DaytonaCacheVolumePrefix is used in physical Daytona cache pool
	// volume names. Orgs are hashed into fixed dev/stg/prod pools so one
	// shared Daytona org stays within the 100-volume limit.
	DaytonaCacheVolumePrefix string
	// DaytonaCachePruneDays controls best-effort pruning of old
	// dependency cache files inside the local staging cache before it is
	// archived back to the mounted repo subpath.
	DaytonaCachePruneDays int
	// DaytonaAutoArchiveMinutes controls Daytona's auto-archive timer.
	// Successful runs stop the sandbox immediately and rely on Daytona to
	// archive it after this many continuously-stopped minutes.
	DaytonaAutoArchiveMinutes int
	DatabaseURL               string
	// DatabaseMaxConns caps the pgx connection pool. Zero leaves the
	// pgx default (max(4, NumCPU)) — fine for `make bot`, but in
	// staging/prod set DATABASE_MAX_CONNS so the pool doesn't starve
	// under SSE + recovery-loop concurrency.
	DatabaseMaxConns int32
	WebPort          string

	// JobDispatchIntervalSeconds is the in-process scheduled-job
	// dispatcher's tick interval. 0 disables the loop entirely for
	// deployments that prefer an external cron invoking
	// `hetchy --dispatch-due-jobs`. Claiming is FOR UPDATE SKIP LOCKED,
	// so an in-process loop and an external cron coexist safely during
	// a migration between the two.
	JobDispatchIntervalSeconds int
	// JobDispatchLimit caps how many due jobs one tick claims.
	JobDispatchLimit int
	// JobDispatchConcurrency caps how many claimed jobs run at once.
	JobDispatchConcurrency int
	// PRStatePollIntervalSeconds controls the in-process maintenance loop that
	// refreshes PR state for PAT-backed repos. 0 disables it.
	PRStatePollIntervalSeconds int
	// PRStatePollLimit caps how many stale PAT-backed PRs one tick refreshes.
	PRStatePollLimit int

	WorkOSAPIKey          string
	WorkOSClientID        string
	WorkOSCookiePassword  string
	WorkOSRedirectURI     string
	WorkOSWebhookSecret   string
	LogoutReturnTo        string
	PublicBaseURLOverride string
	// AuthMode selects the real auth backend: "workos" for hosted AuthKit
	// or "local" for self-hosted username/password auth. AUTH_BYPASS still
	// overrides both for tests and local development shortcuts.
	AuthMode string
	// CookieSecure is the Secure flag on the session cookie. Defaults to
	// true; set COOKIE_INSECURE=1 to disable it for local HTTP dev.
	CookieSecure bool
	// TrustedProxy allows Hetchy to trust X-Forwarded-For/X-Real-IP for
	// security-sensitive client IP detection. Enable only behind a proxy
	// that overwrites those headers.
	TrustedProxy bool

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
	// https://app.hetchy.ai/slack/oauth/callback for prod.
	SlackOAuthRedirectURI string

	// LinearClientID and LinearClientSecret identify the Hetchy Linear
	// OAuth app (one per environment) used by the agent install flow.
	// LinearWebhookSecret is that app's webhook signing secret, used to
	// verify inbound agent-session events via HMAC-SHA256.
	// LinearOAuthRedirectURI must match a redirect URL registered on
	// the Linear app. All empty disables the integration: the install
	// button is hidden and inbound webhooks are refused.
	LinearClientID         string
	LinearClientSecret     string
	LinearWebhookSecret    string
	LinearOAuthRedirectURI string

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

	// Stripe is the source of truth for paid subscriptions, payment
	// methods, invoices, hosted checkout, and customer portal sessions.
	// Free/trial and comped org enforcement is handled locally.
	StripeSecretKey            string
	StripeWebhookSecret        string
	StripeSubscriptionPriceID  string
	StripeSubscriptionPriceIDs map[string]string
	StripeTopupPriceID         string
	StripeTopupPriceIDs        map[string]string
	StripeReturnTo             string

	AuthBypass      bool
	AuthBypassUser  string
	AuthBypassOrg   string
	AuthBypassRole  string
	AuthBypassEmail string

	// ArtifactDir enables local filesystem proof-artifact storage. When set,
	// it takes precedence over S3 and Hetchy serves signed PUT/GET URLs from
	// the web process.
	ArtifactDir string
	// S3Bucket and S3Region pin the proof-artifact bucket the bot presigns
	// into when ArtifactDir is empty. Both empty disables the artifact upload
	// path. AWS credentials come from the standard SDK chain (env vars,
	// ~/.aws/credentials, IAM role) — we don't read them here.
	S3Bucket string
	S3Region string

	// SXPublicVaultURL is the git sx vault containing Hetchy's built-in
	// agent personas and role skills. Each sandbox installs it with
	// SX_BOT=<selected agent> before running Claude. The org's skills.new
	// vault, when configured, is installed separately.
	SXPublicVaultURL string
	// SXCacheDir is passed to the SX library as SX_CACHE_DIR so Git vault
	// clones and lockfile caches live on a known, volume-backed path. Empty
	// in dev lets SX use the normal OS user cache dir; non-dev auto-detects
	// Railway's /data volume when present.
	SXCacheDir string
	// SXCacheMinFreeBytes is checked before each Hetchy-side SX Git vault
	// operation. Zero disables the free-space check.
	SXCacheMinFreeBytes uint64
	// SXGitOperationTimeoutSeconds bounds Hetchy-side SX Git vault operations.
	SXGitOperationTimeoutSeconds int
	// SXGitMaxConcurrentOps caps concurrent SX Git vault work across orgs.
	SXGitMaxConcurrentOps int
}

const DefaultSXPublicVaultURL = "https://github.com/hetchyhq/hetchy-sx-vault.git"

const (
	defaultSXCacheMinFreeMiB            = 512
	defaultSXGitOperationTimeoutSeconds = 180
	defaultSXGitMaxConcurrentOps        = 4
)

const (
	defaultJobDispatchIntervalSeconds = 300
	defaultJobDispatchLimit           = 5
	defaultJobDispatchConcurrency     = 1
)

const (
	defaultPRStatePollIntervalSeconds = 300
	defaultPRStatePollLimit           = 100
)

// loadJobDispatchConfig reads the in-process scheduled-job dispatcher
// settings. Interval 0 disables the loop (external-cron deployments);
// limit and concurrency must stay positive.
func loadJobDispatchConfig() (interval, limit, concurrency int, err error) {
	interval = defaultJobDispatchIntervalSeconds
	if v := strings.TrimSpace(os.Getenv("HETCHY_JOB_DISPATCH_INTERVAL_SECONDS")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			return 0, 0, 0, fmt.Errorf("HETCHY_JOB_DISPATCH_INTERVAL_SECONDS must be a non-negative integer, 0 to disable (got %q)", v)
		}
		interval = n
	}
	limit = defaultJobDispatchLimit
	if v := strings.TrimSpace(os.Getenv("HETCHY_JOB_DISPATCH_LIMIT")); v != "" {
		// Upper bound matches the int32 the claim query takes; without
		// it an oversized value would wrap negative and silently
		// dispatch nothing.
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > math.MaxInt32 {
			return 0, 0, 0, fmt.Errorf("HETCHY_JOB_DISPATCH_LIMIT must be a positive int32 (got %q)", v)
		}
		limit = n
	}
	concurrency = defaultJobDispatchConcurrency
	if v := strings.TrimSpace(os.Getenv("HETCHY_JOB_DISPATCH_CONCURRENCY")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return 0, 0, 0, fmt.Errorf("HETCHY_JOB_DISPATCH_CONCURRENCY must be a positive integer (got %q)", v)
		}
		concurrency = n
	}
	return interval, limit, concurrency, nil
}

func loadPRStatePollConfig() (interval, limit int, err error) {
	interval = defaultPRStatePollIntervalSeconds
	if v := strings.TrimSpace(os.Getenv("HETCHY_PR_STATE_POLL_INTERVAL_SECONDS")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			return 0, 0, fmt.Errorf("HETCHY_PR_STATE_POLL_INTERVAL_SECONDS must be a non-negative integer, 0 to disable (got %q)", v)
		}
		interval = n
	}
	limit = defaultPRStatePollLimit
	if v := strings.TrimSpace(os.Getenv("HETCHY_PR_STATE_POLL_LIMIT")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > prStateBackfillMaxLimit {
			return 0, 0, fmt.Errorf("HETCHY_PR_STATE_POLL_LIMIT must be between 1 and %d (got %q)", prStateBackfillMaxLimit, v)
		}
		limit = n
	}
	return interval, limit, nil
}

func loadAuthModeEnv() (string, error) {
	authMode := strings.ToLower(strings.TrimSpace(os.Getenv("HETCHY_AUTH_MODE")))
	if authMode == "" {
		return "workos", nil
	}
	switch authMode {
	case "workos", "local":
		return authMode, nil
	default:
		return "", fmt.Errorf("HETCHY_AUTH_MODE must be workos or local (got %q)", authMode)
	}
}

func requiredConfigEnvKeys(bypass bool, authMode, env string) []string {
	required := []string{
		"DATABASE_URL",
		"SECRETS_ENCRYPTION_KEY",
		"DAYTONA_SNAPSHOT",
	}
	if !bypass && authMode == "workos" {
		required = append(required,
			"WORKOS_API_KEY",
			"WORKOS_CLIENT_ID",
			"WORKOS_COOKIE_PASSWORD",
			"WORKOS_REDIRECT_URI",
		)
	}
	if !bypass && env != "dev" {
		required = append(required, "HETCHY_PUBLIC_BASE_URL")
	}
	return required
}

func missingRequiredConfigEnv(keys []string) []string {
	var missing []string
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func loadCookieSecure(publicBaseURLOverride, logout string) bool {
	// CookieSecure defaults to true (required for production HTTPS). It is
	// forced to false when COOKIE_INSECURE=1 is set OR when WORKOS_REDIRECT_URI
	// starts with http:// — that scheme indicates the server is running over
	// plain HTTP (local dev), where browsers refuse Secure cookies.
	if os.Getenv("COOKIE_INSECURE") == "1" {
		return false
	}
	for _, raw := range []string{
		strings.TrimSpace(os.Getenv("WORKOS_REDIRECT_URI")),
		publicBaseURLOverride,
		logout,
	} {
		if u, err := url.Parse(raw); err == nil && u.Scheme == "http" {
			return false
		}
	}
	return true
}

func loadGitHubAppID() (int64, error) {
	v := os.Getenv("GITHUB_APP_ID")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("GITHUB_APP_ID must be numeric: %w", err)
	}
	return n, nil
}

func loadDatabaseMaxConns() (int32, error) {
	v := strings.TrimSpace(os.Getenv("DATABASE_MAX_CONNS"))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("DATABASE_MAX_CONNS must be a positive integer (got %q)", v)
	}
	return int32(n), nil
}

func truthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

type daytonaCacheConfig struct {
	volumePrefix       string
	pruneDays          int
	autoArchiveMinutes int
}

func loadDaytonaCacheConfig() (daytonaCacheConfig, error) {
	pruneDays, err := positiveIntEnv("DAYTONA_CACHE_PRUNE_DAYS", defaultCachePruneDays)
	if err != nil {
		return daytonaCacheConfig{}, err
	}
	volumePrefix := strings.TrimSpace(os.Getenv("DAYTONA_CACHE_VOLUME_PREFIX"))
	if volumePrefix == "" {
		volumePrefix = defaultCacheVolumePrefix
	}
	autoArchiveMinutes, err := positiveIntEnv("DAYTONA_AUTO_ARCHIVE_MINUTES", defaultDaytonaAutoArchiveMinutes)
	if err != nil {
		return daytonaCacheConfig{}, err
	}
	return daytonaCacheConfig{
		volumePrefix:       volumePrefix,
		pruneDays:          pruneDays,
		autoArchiveMinutes: autoArchiveMinutes,
	}, nil
}

// LoadConfig reads required and optional env vars. Set AUTH_BYPASS=1 to
// skip the WorkOS round-trip for tests/CI.
func LoadConfig() (Config, error) {
	bypass := os.Getenv("AUTH_BYPASS") != ""
	env := strings.TrimSpace(os.Getenv("HETCHY_ENV"))
	if env == "" {
		env = "prod"
	}
	authMode, err := loadAuthModeEnv()
	if err != nil {
		return Config{}, err
	}
	publicBaseURLOverride := strings.TrimSpace(os.Getenv("HETCHY_PUBLIC_BASE_URL"))
	if publicBaseURLOverride != "" && publicOrigin(publicBaseURLOverride) == "" {
		return Config{}, errors.New("HETCHY_PUBLIC_BASE_URL must be an http(s) URL with scheme and host")
	}

	if missing := missingRequiredConfigEnv(requiredConfigEnvKeys(bypass, authMode, env)); len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}

	snapshotBase := strings.TrimSpace(os.Getenv("DAYTONA_SNAPSHOT"))
	sandboxSnapshotVersion := getenvDefaultTrim("HETCHY_SANDBOX_VERSION", buildinfo.SandboxSnapshotVersion)
	resolvedSnapshot, err := resolveDaytonaSnapshot(env, snapshotBase, sandboxSnapshotVersion)
	if err != nil {
		return Config{}, err
	}

	port := getenvDefault("WEB_PORT", "8080")
	logout := getenvDefault("LOGOUT_RETURN_TO", "http://localhost:"+port+"/")
	stripeReturnTo := getenvDefault("STRIPE_RETURN_TO", logout)
	cookieSecure := loadCookieSecure(publicBaseURLOverride, logout)

	ghAppID, err := loadGitHubAppID()
	if err != nil {
		return Config{}, err
	}
	dbMaxConns, err := loadDatabaseMaxConns()
	if err != nil {
		return Config{}, err
	}
	cacheConfig, err := loadDaytonaCacheConfig()
	if err != nil {
		return Config{}, err
	}
	jobInterval, jobLimit, jobConcurrency, err := loadJobDispatchConfig()
	if err != nil {
		return Config{}, err
	}
	prStatePollInterval, prStatePollLimit, err := loadPRStatePollConfig()
	if err != nil {
		return Config{}, err
	}
	sxRuntime, err := loadSXRuntimeConfig(env)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Env:                         env,
		DaytonaAPIURL:               strings.TrimSpace(os.Getenv("DAYTONA_API_URL")),
		SnapshotBase:                snapshotBase,
		Snapshot:                    resolvedSnapshot,
		SandboxSnapshotVersion:      sandboxSnapshotVersion,
		DaytonaCacheVolumesDisabled: strings.TrimSpace(os.Getenv("DAYTONA_CACHE_VOLUMES_DISABLED")) == "1",
		DaytonaCacheVolumePrefix:    cacheConfig.volumePrefix,
		DaytonaCachePruneDays:       cacheConfig.pruneDays,
		DaytonaAutoArchiveMinutes:   cacheConfig.autoArchiveMinutes,
		DatabaseURL:                 os.Getenv("DATABASE_URL"),
		DatabaseMaxConns:            dbMaxConns,
		WebPort:                     port,
		JobDispatchIntervalSeconds:  jobInterval,
		JobDispatchLimit:            jobLimit,
		JobDispatchConcurrency:      jobConcurrency,
		PRStatePollIntervalSeconds:  prStatePollInterval,
		PRStatePollLimit:            prStatePollLimit,
		WorkOSAPIKey:                strings.TrimSpace(os.Getenv("WORKOS_API_KEY")),
		WorkOSClientID:              strings.TrimSpace(os.Getenv("WORKOS_CLIENT_ID")),
		WorkOSCookiePassword:        strings.TrimSpace(os.Getenv("WORKOS_COOKIE_PASSWORD")),
		WorkOSRedirectURI:           strings.TrimSpace(os.Getenv("WORKOS_REDIRECT_URI")),
		WorkOSWebhookSecret:         strings.TrimSpace(os.Getenv("WORKOS_WEBHOOK_SECRET")),
		LogoutReturnTo:              logout,
		PublicBaseURLOverride:       publicBaseURLOverride,
		AuthMode:                    authMode,
		CookieSecure:                cookieSecure,
		TrustedProxy:                truthyEnv("HETCHY_TRUSTED_PROXY"),
		SecretsEncryptionKey:        strings.TrimSpace(os.Getenv("SECRETS_ENCRYPTION_KEY")),
		SlackSigningSecret:          strings.TrimSpace(os.Getenv("SLACK_SIGNING_SECRET")),
		SlackClientID:               strings.TrimSpace(os.Getenv("SLACK_CLIENT_ID")),
		SlackClientSecret:           strings.TrimSpace(os.Getenv("SLACK_CLIENT_SECRET")),
		SlackOAuthRedirectURI:       strings.TrimSpace(os.Getenv("SLACK_OAUTH_REDIRECT_URI")),
		LinearClientID:              strings.TrimSpace(os.Getenv("LINEAR_CLIENT_ID")),
		LinearClientSecret:          strings.TrimSpace(os.Getenv("LINEAR_CLIENT_SECRET")),
		LinearWebhookSecret:         strings.TrimSpace(os.Getenv("LINEAR_WEBHOOK_SECRET")),
		LinearOAuthRedirectURI:      strings.TrimSpace(os.Getenv("LINEAR_OAUTH_REDIRECT_URI")),
		GitHubAppID:                 ghAppID,
		GitHubAppSlug:               strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG")),
		GitHubAppClientID:           strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID")),
		// Not trimmed: PEM contents are multi-line and the parser relies on
		// embedded newlines; trimming risks corrupting the key.
		GitHubAppPrivateKey:       os.Getenv("GITHUB_APP_PRIVATE_KEY"),
		GitHubAppWebhookSecret:    strings.TrimSpace(os.Getenv("GITHUB_APP_WEBHOOK_SECRET")),
		StripeSecretKey:           strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")),
		StripeWebhookSecret:       strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")),
		StripeSubscriptionPriceID: strings.TrimSpace(os.Getenv("STRIPE_SUBSCRIPTION_PRICE_ID")),
		StripeSubscriptionPriceIDs: stripeSubscriptionPriceIDs(
			os.Getenv("STRIPE_SUBSCRIPTION_PRICE_IDS"),
			os.Getenv("STRIPE_SUBSCRIPTION_PRICE_ID"),
		),
		StripeTopupPriceID:           strings.TrimSpace(os.Getenv("STRIPE_TOPUP_PRICE_ID")),
		StripeTopupPriceIDs:          stripePriceIDMap(os.Getenv("STRIPE_TOPUP_PRICE_IDS")),
		StripeReturnTo:               stripeReturnTo,
		AuthBypass:                   bypass,
		AuthBypassUser:               getenvDefault("AUTH_BYPASS_USER", "user_bypass"),
		AuthBypassOrg:                os.Getenv("AUTH_BYPASS_ORG"),
		AuthBypassRole:               getenvDefault("AUTH_BYPASS_ROLE", "admin"),
		AuthBypassEmail:              getenvDefault("AUTH_BYPASS_EMAIL", "bypass@hetchy.local"),
		ArtifactDir:                  strings.TrimSpace(os.Getenv("HETCHY_ARTIFACT_DIR")),
		S3Bucket:                     strings.TrimSpace(os.Getenv("HETCHY_S3_BUCKET")),
		S3Region:                     strings.TrimSpace(os.Getenv("HETCHY_S3_REGION")),
		SXPublicVaultURL:             getenvDefaultTrimAllowDisabled("HETCHY_SX_PUBLIC_VAULT_URL", DefaultSXPublicVaultURL),
		SXCacheDir:                   sxRuntime.cacheDir,
		SXCacheMinFreeBytes:          sxRuntime.cacheMinFreeBytes,
		SXGitOperationTimeoutSeconds: sxRuntime.gitOperationTimeoutSeconds,
		SXGitMaxConcurrentOps:        sxRuntime.gitMaxConcurrentOps,
	}, nil
}

type sxRuntimeConfig struct {
	cacheDir                   string
	cacheMinFreeBytes          uint64
	gitOperationTimeoutSeconds int
	gitMaxConcurrentOps        int
}

func loadSXRuntimeConfig(env string) (sxRuntimeConfig, error) {
	cacheDir := sxCacheDirFromEnv(env)
	cacheMinFreeMiB := 0
	if cacheDir != "" {
		cacheMinFreeMiB = defaultSXCacheMinFreeMiB
	}
	if v := strings.TrimSpace(os.Getenv("HETCHY_SX_CACHE_MIN_FREE_MB")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return sxRuntimeConfig{}, fmt.Errorf("HETCHY_SX_CACHE_MIN_FREE_MB must be a non-negative integer (got %q)", v)
		}
		cacheMinFreeMiB = n
	}
	timeoutSeconds, err := positiveIntEnv("HETCHY_SX_GIT_OPERATION_TIMEOUT_SECONDS", defaultSXGitOperationTimeoutSeconds)
	if err != nil {
		return sxRuntimeConfig{}, err
	}
	maxConcurrentOps, err := positiveIntEnv("HETCHY_SX_GIT_MAX_CONCURRENT_OPS", defaultSXGitMaxConcurrentOps)
	if err != nil {
		return sxRuntimeConfig{}, err
	}
	return sxRuntimeConfig{
		cacheDir:                   cacheDir,
		cacheMinFreeBytes:          uint64(cacheMinFreeMiB) << 20,
		gitOperationTimeoutSeconds: timeoutSeconds,
		gitMaxConcurrentOps:        maxConcurrentOps,
	}, nil
}

func positiveIntEnv(key string, fallback int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer (got %q)", key, v)
	}
	return n, nil
}

func sxCacheDirFromEnv(env string) string {
	if dir := strings.TrimSpace(os.Getenv("HETCHY_SX_CACHE_DIR")); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("SX_CACHE_DIR")); dir != "" {
		return dir
	}
	if env != "dev" {
		if st, err := os.Stat("/data"); err == nil && st.IsDir() {
			return "/data/hetchy/sx-cache"
		}
	}
	return ""
}

func stripeSubscriptionPriceIDs(raw, legacy string) map[string]string {
	out := stripePriceIDMap(raw)
	if legacy = strings.TrimSpace(legacy); legacy != "" {
		defaultPlan := billing.DefaultPaidPlan()
		if out[defaultPlan.Code] == "" {
			out[defaultPlan.Code] = legacy
		}
	}
	return out
}

func stripePriceIDMap(raw string) map[string]string {
	out := map[string]string{}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		plan := strings.ToLower(strings.TrimSpace(parts[0]))
		priceID := strings.TrimSpace(parts[1])
		if plan != "" && priceID != "" {
			out[plan] = priceID
		}
	}
	return out
}

func resolveDaytonaSnapshot(env, base, version string) (string, error) {
	base = strings.TrimSpace(base)
	version = strings.TrimSpace(version)
	if base == "" {
		return "", errors.New("DAYTONA_SNAPSHOT is required")
	}
	if env == "dev" && (version == "" || version == "dev" || version == "unknown") {
		return base, nil
	}
	if version == "" || version == "dev" || version == "unknown" {
		return "", errors.New("sandbox snapshot version is not set; build with internal/buildinfo.SandboxSnapshotVersion or set HETCHY_SANDBOX_VERSION")
	}
	return base + "-" + version, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvDefaultTrim(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return strings.TrimSpace(def)
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

// PublicBaseURL returns the externally-reachable base URL for non-Stripe
// app links and sandbox callbacks (no trailing slash). HETCHY_PUBLIC_BASE_URL
// is required outside dev and is the only safe source for deployed callback
// URLs. LOGOUT_RETURN_TO is kept as a legacy public-root fallback when set to
// an external URL; it no longer controls the post-logout redirect. In dev only,
// if it is unset and therefore defaulted to localhost, derive the public origin
// from OAuth callback URLs before falling back to the local bind URL.
func (c Config) PublicBaseURL() string {
	if base := publicOrigin(c.PublicBaseURLOverride); base != "" {
		return base
	}
	port := c.WebPort
	if port == "" {
		port = "8080"
	}
	local := "http://localhost:" + port
	if base := publicOrigin(c.LogoutReturnTo); base != "" && base != local {
		return base
	}
	if c.Env == "dev" {
		for _, raw := range []string{c.WorkOSRedirectURI, c.SlackOAuthRedirectURI} {
			if base := publicOrigin(raw); base != "" {
				return base
			}
		}
	}
	if base := publicOrigin(c.LogoutReturnTo); base != "" {
		return base
	}
	return local
}

func publicOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// StripeReturnBaseURL returns the base URL used for Stripe Checkout and
// Customer Portal return links. STRIPE_RETURN_TO is preferred; existing
// LOGOUT_RETURN_TO/PublicBaseURL config remains a fallback for deployments
// that have not renamed the variable yet.
func (c Config) StripeReturnBaseURL() string {
	if base := strings.TrimSuffix(c.StripeReturnTo, "/"); base != "" {
		return base
	}
	return c.PublicBaseURL()
}
