// Package bot implements the multi-tenant Slack + web bot that turns
// natural-language requests into pull requests via Claude Code running in
// a Daytona sandbox. Each organization brings its own GitHub/Slack
// credentials and target repo, looked up per-request from the database.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/apikeys"
	"github.com/hetchyhq/hetchy/internal/artifacts"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/billing"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
	"github.com/hetchyhq/hetchy/internal/secrets"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

// maxBlocksPerTurn caps the legacy response_blocks projection when a
// durable run row is not available. Durable runs use
// durableMaxBlocksPerTurn so the conversation projection can replay the
// full bootstrap/skills stream from run events after reload.
const (
	maxBlocksPerTurn        = 200
	durableMaxBlocksPerTurn = 0
)

const (
	maxRetries        = 3
	initialBackoff    = 2 * time.Second
	backoffMultiplier = 2
)

// workdirRoot is the parent directory inside the sandbox under which
// every repo is cloned. The actual checkout lands at
// repoWorkdir(slug), which appends the repo name so sx install can
// detect the right repo by reading the .git remote at a path whose
// last segment matches the repository name. See repoWorkdir for the
// rationale.
const workdirRoot = "/home/daytona/work"

// repoWorkdir returns the absolute path inside the sandbox where the
// repo identified by slug ("owner/name") gets cloned. The trailing
// segment matches the repo name on purpose — `sx install` walks the
// target directory to detect git context (and skills.new uses that
// context to scope per-repo skills), so a clone path that looks like
// `/home/daytona/work/<repo-name>` keeps the sandbox layout aligned
// with how a developer would check the repo out locally. A slug with
// no slash (or an empty/odd basename) falls back to "repo" so we
// never return the parent dir as the workdir. ".." is rejected so a
// future caller passing untrusted input can't escape the workdir
// root via `path.Base` — GitHub slugs can't contain ".." today, but
// the helper is the natural extension point and a one-line guard
// here is cheaper than relying on every future caller to sanitise.
func repoWorkdir(slug string) string {
	name := path.Base(strings.TrimSpace(slug))
	if name == "" || name == "." || name == ".." || name == "/" {
		name = "repo"
	}
	return workdirRoot + "/" + name
}

const sandboxReadySSETag = "sandbox_ready"

// Bot wires the web UI, Slack manager, Daytona, and per-org config
// together. It owns no per-request mutable state; conversation state lives
// in the database.
type Bot struct {
	cfg       Config
	log       *slog.Logger
	daytona   *daytona.Client
	cacheVols daytonaCacheVolumeService
	store     *db.Store
	orgs      orgStore
	convs     conversationStore
	runs      runStore
	billing   *billing.Service
	agents    *agents.Store
	apiKeys   *apikeys.Store
	sx        sxManager
	auth      *auth.Service
	slack     *slackManager
	bootstrap bootstrapStore
	// artifacts is the S3 presigner used to mint per-request proof
	// artifact upload slots for the validation prompt. Nil when
	// HETCHY_S3_BUCKET / HETCHY_S3_REGION aren't configured.
	artifacts artifactMinter
	// artifactSlots tracks run-scoped bearer tokens for in-sandbox
	// requests that need more slots than the default batch.
	artifactSlots *artifactSlotBroker
	// live tracks in-flight chat turns so the conversation events API
	// reattach endpoint can find them and replay buffered
	// SSE events to a reloading tab. Goroutine-safe.
	live *liveRegistry
	// app is the GitHub App handle (per-environment dev/staging/prod).
	// Nil when GITHUB_APP_* env vars aren't configured — the install
	// button is hidden and inbound webhooks refused in that case, so
	// every read of this field must nil-check.
	app *githubapp.App
	// slackUsers maps Slack user IDs to WorkOS user IDs so a chat
	// started in Slack is attributed to the right hetchy user. Lazily
	// populated and cached for the process lifetime — see
	// slack_user_resolver.go.
	slackUsers *slackUserResolver
	// githubWebhookSem caps the number of concurrent goroutines
	// fanned out from the GitHub webhook endpoint. See
	// github_webhook.go for the rationale + tuning.
	githubWebhookSem chan struct{}
	// githubWebhookErrLog suppresses repeat log lines from the
	// webhook parse-error paths so a leaked webhook secret can't be
	// used to flood logs at our expense.
	githubWebhookErrLog webhookErrLogger
	// cipher is reused for the OAuth state token (Slack install flow,
	// GitHub App setup callback). AES-GCM gives confidentiality +
	// tamper detection in a single step, so we don't need a separate
	// signing key for state.
	cipher *secrets.Cipher

	// createFn is called by createSandboxWithRetry; overridable in tests.
	createFn func(context.Context, any) (*daytona.Sandbox, error)
	// startFn is called by resumeSandbox; overridable in tests.
	startFn      func(context.Context, *daytona.Sandbox, time.Duration) error
	retryBackoff time.Duration
	// resolveRepoFn/runAgentFn/runFollowUpFn/getSandboxFn/deleteSandboxSessionFn
	// are narrow seams around external systems used by the chat state machine.
	// Tests install hand-written fakes here so core request logic can be
	// exercised without GitHub, Daytona, or shell execution.
	resolveRepoFn             repoResolveFunc
	runAgentFn                agentRunFunc
	runFollowUpFn             followUpRunFunc
	runScriptFn               scriptRunFunc
	shLinesFn                 shLinesFunc
	createBootstrapSessionFn  bootstrapSessionFunc
	runInlineScriptFn         inlineScriptFunc
	detectViaSandboxFn        bootstrapDetectFunc
	bootstrapRunFn            bootstrapRunFunc
	recoverRunFn              recoveryLaunchFunc
	validateRecoveredPRFn     recoveredPRValidationFunc
	getSandboxFn              func(context.Context, string) (*daytona.Sandbox, error)
	resumeSandboxFn           func(context.Context, *daytona.Sandbox, blocks.Emitter) error
	deleteSandboxSessionFn    func(*daytona.Sandbox, string)
	stopAndArchiveFn          func(context.Context, *daytona.Sandbox)
	setAutoArchiveIntervalFn  func(context.Context, *daytona.Sandbox, *int) error
	stopSandboxFn             func(context.Context, *daytona.Sandbox) error
	archiveSandboxFn          func(context.Context, *daytona.Sandbox) error
	cleanupSandboxFn          sandboxCleanupFunc
	ensureSandboxStartedFn    sandboxStartCheckFunc
	commandLogSnapshotFn      commandLogSnapshotFunc
	sessionCommandStatusFn    sessionCommandStatusFunc
	downloadSandboxFileFn     func(context.Context, *daytona.Sandbox, string) ([]byte, error)
	usersOnlyInOrgFn          func(context.Context, string) ([]string, error)
	deleteWorkOSOrgFn         func(context.Context, string) error
	workOSOrgHasFeatureFlagFn func(context.Context, string, string) (bool, error)
	// branchNameFn lets tests bypass the LLM round-trip in
	// branchNameFor. Production code leaves this nil; the default
	// path calls Anthropic and falls back to "sf" on any failure.
	branchNameFn        func(context.Context, orgcfg.Config, string) string
	followUpModeFn      followUpModeFunc
	deleteWorkOSUsersFn func(context.Context, []string) error
	lookupRepoFn        func(context.Context, string, string, string) (sqlc.GithubRepo, error)
	// cleanupSandboxByIDFn is called by chatCancelHandler for opportunistic
	// cleanup of a fresh-run sandbox; overridable in tests.
	cleanupSandboxByIDFn func(string, string)
	// heartbeatInterval controls how often resumeSandbox emits elapsed-time
	// progress lines. Zero is treated as 15 s (the production default);
	// tests set it to 1 ms so the ticker fires without sleeping.
	heartbeatInterval time.Duration
	workerID          string
}

// New constructs a Bot from config and a logger. It opens the database
// pool, builds the encryption cipher, configures the WorkOS auth service,
// and prepares the Slack manager (no sockets are opened yet — Run does
// that).
func New(cfg Config, log *slog.Logger) (*Bot, error) {
	daytonaCfg := &types.DaytonaConfig{}
	if cfg.DaytonaAPIURL != "" {
		daytonaCfg.APIUrl = cfg.DaytonaAPIURL
	}
	dc, err := daytona.NewClientWithConfig(daytonaCfg)
	if err != nil {
		return nil, fmt.Errorf("daytona client: %w", err)
	}
	mode, url := daytonaLogTarget(cfg.DaytonaAPIURL)
	log.Info("daytona configured", "mode", mode, "url", url)

	store, err := db.Open(context.Background(), cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return nil, fmt.Errorf("database open: %w", err)
	}
	log.Info("database connected", "pool_max_conns", cfg.DatabaseMaxConns)

	cipher, err := secrets.New(cfg.SecretsEncryptionKey)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("secrets cipher: %w", err)
	}

	authSvc, err := auth.New(auth.Config{
		APIKey:         cfg.WorkOSAPIKey,
		ClientID:       cfg.WorkOSClientID,
		CookiePassword: cfg.WorkOSCookiePassword,
		RedirectURI:    cfg.WorkOSRedirectURI,
		CookieSecure:   cfg.CookieSecure,
		Bypass:         cfg.AuthBypass,
		BypassUser:     cfg.AuthBypassUser,
		BypassOrg:      cfg.AuthBypassOrg,
		BypassRole:     cfg.AuthBypassRole,
		BypassEmail:    cfg.AuthBypassEmail,
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("auth: %w", err)
	}

	// Artifact upload signer. ErrNotConfigured is the "feature
	// disabled" sentinel — log + continue. Other errors mean AWS
	// config loading itself failed (corrupt ~/.aws/config, etc.); we
	// also continue without the feature rather than refusing to
	// start, since hetchy is useful without proof artifact upload.
	artifactSigner, err := artifacts.New(context.Background(), cfg.S3Bucket, cfg.S3Region)
	switch {
	case errors.Is(err, artifacts.ErrNotConfigured):
		log.Info("artifact upload disabled: HETCHY_S3_BUCKET / HETCHY_S3_REGION not set")
		artifactSigner = nil
	case err != nil:
		log.Warn("artifact signer disabled", "error", err)
		artifactSigner = nil
	default:
		log.Info("artifact upload configured", "bucket", cfg.S3Bucket, "region", cfg.S3Region)
	}

	orgStore := orgcfg.New(store, cipher)
	agentStore := agents.NewStoreWithCipher(store, cipher)
	b := &Bot{
		cfg:              cfg,
		log:              log,
		daytona:          dc,
		cacheVols:        dc.Volume,
		store:            store,
		orgs:             orgStore,
		convs:            convstore.New(store),
		runs:             runstore.New(store),
		billing:          billing.NewService(billing.NewStore(store), newStripeAutoTopupper(cfg)),
		agents:           agentStore,
		apiKeys:          apikeys.New(store),
		bootstrap:        bootstrap.New(store, cipher),
		artifacts:        artifactSigner,
		artifactSlots:    newArtifactSlotBroker(artifactSigner),
		live:             newLiveRegistry(),
		auth:             authSvc,
		cipher:           cipher,
		retryBackoff:     initialBackoff,
		githubWebhookSem: make(chan struct{}, webhookDispatchConcurrency),
		workerID:         newWorkerID(),
	}
	b.createFn = func(ctx context.Context, params any) (*daytona.Sandbox, error) {
		return dc.Create(ctx, params)
	}
	b.startFn = func(ctx context.Context, sb *daytona.Sandbox, timeout time.Duration) error {
		return sb.StartWithTimeout(ctx, timeout)
	}
	b.slackUsers = newSlackUserResolver(log, authSvc)
	b.slack = newSlackManager(log, b.orgs, b.handleSlackEvent)
	b.warnIfSlackOAuthMisconfigured()

	// Surface a few config values that are easy to misset on a dev box
	// and produce confusing failure modes (cookie not stored, OAuth
	// state mismatch, etc.). Shown at info level on every startup.
	log.Info("bot startup",
		"env", cfg.Env,
		"web_port", cfg.WebPort,
		"cookie_secure", cfg.CookieSecure,
		"daytona_snapshot_base", cfg.SnapshotBase,
		"daytona_snapshot", cfg.Snapshot,
		"sandbox_snapshot_version", cfg.SandboxSnapshotVersion,
		"daytona_auto_archive_minutes", cfg.DaytonaAutoArchiveMinutes,
		"sx_cache_dir", cfg.SXCacheDir,
		"sx_git_operation_timeout_seconds", cfg.SXGitOperationTimeoutSeconds,
		"sx_git_max_concurrent_ops", cfg.SXGitMaxConcurrentOps,
	)

	// GitHub App is optional in dev — without env vars the integrations
	// page hides the install button and inbound webhooks refuse traffic.
	// In staging/prod we expect every var; warn loudly on missing pieces
	// so a misconfigured deploy is discovered at startup rather than at
	// the first install attempt.
	if cfg.GitHubAppID != 0 {
		app, err := githubapp.New(githubapp.Config{
			AppID:         cfg.GitHubAppID,
			Slug:          cfg.GitHubAppSlug,
			ClientID:      cfg.GitHubAppClientID,
			PrivateKeyPEM: cfg.GitHubAppPrivateKey,
			WebhookSecret: cfg.GitHubAppWebhookSecret,
		}, log)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("github app: %w", err)
		}
		b.app = app
		log.Info("github app configured", "app_id", cfg.GitHubAppID, "slug", cfg.GitHubAppSlug)
	} else if cfg.Env != "dev" {
		log.Warn("github app: GITHUB_APP_ID is not set — integration install button + webhooks disabled",
			"env", cfg.Env,
		)
	}
	b.sx = sxsync.NewManagerWithOptions(store, orgStore, agentStore, b.app, sxsync.Options{
		CacheDir:            cfg.SXCacheDir,
		CacheMinFreeBytes:   cfg.SXCacheMinFreeBytes,
		GitOperationTimeout: time.Duration(cfg.SXGitOperationTimeoutSeconds) * time.Second,
		MaxConcurrentGitOps: cfg.SXGitMaxConcurrentOps,
	})
	if status, err := b.sx.CheckCache(); err != nil {
		if cfg.SXCacheDir != "" {
			store.Close()
			return nil, fmt.Errorf("sx cache: %w", err)
		}
		log.Warn("sx cache check failed; using sx default cache location", "error", err)
	} else if status.Configured {
		log.Info("sx cache configured",
			"path", status.Path,
			"available_bytes", status.AvailableBytes,
			"min_free_bytes", status.MinFreeBytes,
		)
	} else if cfg.Env != "dev" {
		log.Warn("sx cache dir is not configured; Git vault clones will use the process default cache location")
	}
	return b, nil
}

func daytonaLogTarget(apiURL string) (mode, url string) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		return "cloud", "app.daytona.io"
	}
	if strings.Contains(apiURL, "app.daytona.io") {
		return "cloud", apiURL
	}
	if strings.Contains(apiURL, "localhost") || strings.Contains(apiURL, "127.0.0.1") || strings.Contains(apiURL, "api:3000") {
		return "local", apiURL
	}
	return "custom", apiURL
}

// warnIfSlackOAuthMisconfigured surfaces a startup-time warning when
// the env is anything other than dev but the Slack OAuth/HTTP-transport
// env vars are missing or partial. Without this, a misconfigured
// staging/prod box silently starts up and Slack starts retrying every
// event into a closed-from-our-side endpoint — discoverable only via
// log volume an hour later. dev mode legitimately runs with these
// blank (Socket Mode does its own auth via xapp- tokens).
func (b *Bot) warnIfSlackOAuthMisconfigured() {
	if b.cfg.Env == "dev" {
		return
	}
	missing := []string{}
	if b.cfg.SlackSigningSecret == "" {
		missing = append(missing, "SLACK_SIGNING_SECRET")
	}
	if b.cfg.SlackClientID == "" {
		missing = append(missing, "SLACK_CLIENT_ID")
	}
	if b.cfg.SlackClientSecret == "" {
		missing = append(missing, "SLACK_CLIENT_SECRET")
	}
	if b.cfg.SlackOAuthRedirectURI == "" {
		missing = append(missing, "SLACK_OAUTH_REDIRECT_URI")
	}
	if len(missing) > 0 {
		b.log.Warn("slack: HTTP transport not configured — install + events endpoints will refuse traffic",
			"env", b.cfg.Env,
			"missing", missing,
		)
	}
}

// Close releases external resources held by the bot. Safe to call once
// after Run returns.
func (b *Bot) Close() {
	if b.store != nil {
		b.store.Close()
	}
}

// Run starts the web UI and the per-org Slack manager. Either failing
// returns the first error and cancels the other.
func (b *Bot) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go b.runRecoveryLoop(ctx)
	go func() { errCh <- b.runWeb(ctx) }()
	go func() { errCh <- b.slack.Run(ctx) }()

	err := <-errCh
	cancel()
	<-errCh
	return err
}

// Chat task option keys are persisted in conversations.task_options.
// Keep them stable: the web API uses the same names.
const (
	chatTaskValidateKey              = "validate"
	chatTaskReviewCodeBeforePushKey  = "review_code_before_push"
	chatTaskActionPRChecksForDoneKey = "action_pr_checks_for_done"
)

// chatTaskOptions are the resolved per-turn conditional tasks surfaced
// in the web composer. Missing stored keys default on.
type chatTaskOptions struct {
	ValidateChanges       bool
	ReviewCodeBeforePush  bool
	ActionPRChecksForDone bool
}

// chatTaskOptionPatch carries only the option values present on an
// inbound request. Applying it over the saved generic task_options bag
// preserves unknown future keys and avoids resetting older clients'
// omitted fields.
type chatTaskOptionPatch map[string]bool

func defaultChatTaskOptions() chatTaskOptions {
	return chatTaskOptions{
		ValidateChanges:       true,
		ReviewCodeBeforePush:  true,
		ActionPRChecksForDone: true,
	}
}

func resolveChatTaskOptions(saved map[string]bool, patch chatTaskOptionPatch) (chatTaskOptions, map[string]bool) {
	merged := mergeChatTaskOptionValues(saved, patch)
	return chatTaskOptions{
		ValidateChanges:       chatTaskOptionEnabled(merged, chatTaskValidateKey),
		ReviewCodeBeforePush:  chatTaskOptionEnabled(merged, chatTaskReviewCodeBeforePushKey),
		ActionPRChecksForDone: chatTaskOptionEnabled(merged, chatTaskActionPRChecksForDoneKey),
	}, merged
}

func mergeChatTaskOptionValues(saved map[string]bool, patch chatTaskOptionPatch) map[string]bool {
	if len(saved) == 0 && len(patch) == 0 {
		return nil
	}
	merged := make(map[string]bool, len(saved)+len(patch))
	maps.Copy(merged, saved)
	maps.Copy(merged, patch)
	return merged
}

func chatTaskOptionEnabled(values map[string]bool, key string) bool {
	if v, ok := values[key]; ok {
		return v
	}
	return true
}
