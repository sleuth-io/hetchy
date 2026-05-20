// Package bot implements the multi-tenant Slack + web bot that turns
// natural-language requests into pull requests via Claude Code running in
// a Daytona sandbox. Each organization brings its own GitHub/Slack
// credentials and target repo, looked up per-request from the database.
package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/apikeys"
	"github.com/hetchyhq/hetchy/internal/artifacts"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

// maxBlocksPerTurn caps how many blocks we persist per turn. The full
// stream still reaches the user in real time via SSE; the persisted copy
// only needs enough blocks for a reopened chat to be readable. A long
// agent run with verbose tool output can otherwise easily push hundreds
// of blocks into a single JSONB[] cell.
const maxBlocksPerTurn = 200

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
	agents    *agents.Store
	apiKeys   *apikeys.Store
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
	resolveRepoFn            repoResolveFunc
	runAgentFn               agentRunFunc
	runFollowUpFn            followUpRunFunc
	runScriptFn              scriptRunFunc
	shLinesFn                shLinesFunc
	createBootstrapSessionFn bootstrapSessionFunc
	runInlineScriptFn        inlineScriptFunc
	detectViaSandboxFn       bootstrapDetectFunc
	bootstrapRunFn           bootstrapRunFunc
	recoverRunFn             recoveryLaunchFunc
	validateRecoveredPRFn    recoveredPRValidationFunc
	getSandboxFn             func(context.Context, string) (*daytona.Sandbox, error)
	resumeSandboxFn          func(context.Context, *daytona.Sandbox, blocks.Emitter) error
	deleteSandboxSessionFn   func(*daytona.Sandbox, string)
	stopAndArchiveFn         func(context.Context, *daytona.Sandbox)
	setAutoArchiveIntervalFn func(context.Context, *daytona.Sandbox, *int) error
	stopSandboxFn            func(context.Context, *daytona.Sandbox) error
	archiveSandboxFn         func(context.Context, *daytona.Sandbox) error
	cleanupSandboxFn         sandboxCleanupFunc
	ensureSandboxStartedFn   sandboxStartCheckFunc
	commandLogSnapshotFn     commandLogSnapshotFunc
	sessionCommandStatusFn   sessionCommandStatusFunc
	downloadSandboxFileFn    func(context.Context, *daytona.Sandbox, string) ([]byte, error)
	usersOnlyInOrgFn         func(context.Context, string) ([]string, error)
	deleteWorkOSOrgFn        func(context.Context, string) error
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

	b := &Bot{
		cfg:              cfg,
		log:              log,
		daytona:          dc,
		cacheVols:        dc.Volume,
		store:            store,
		orgs:             orgcfg.New(store, cipher),
		convs:            convstore.New(store),
		runs:             runstore.New(store),
		agents:           agents.NewStore(store),
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
		"daytona_auto_archive_minutes", cfg.DaytonaAutoArchiveMinutes,
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

// HandleRequest is the shared core. It expects an already-resolved org
// config — callers (web/slack) pull oc from the principal's org id (web)
// or the org that owns the inbound socket (slack) and pass it in.
//
// `out` is the transport-side Emitter (web SSE, Slack, …). HandleRequest
// wraps it with a Recorder so the same blocks reach both the user and
// persistence.
//
// Per-conversation state machine: when no conversation row exists for
// (org, thread), the request opens a new one. The repo is resolved
// from oc.DefaultGitHubOwner/Repo if set, otherwise the bot saves a
// partial conversation (no sandbox, empty repo fields) and asks the
// user to reply with `owner/name`. The next message into a conversation
// in that "awaiting repo" state is interpreted as the repo selection,
// not as a new task.
//
// The incoming option patch is applied over the conversation's saved
// task_options JSON object.
// opts.ValidateChanges gates the repo-bootstrap pipeline + post-change
// validation prompt: when true (the default for missing saved keys) the
// agent does first-time bootstrap, applies the saved spec, and is told
// to produce proof artifacts/test evidence before opening the PR. When
// false (web user explicitly unchecks the "Validate changes with
// end-to-end testing" box) we skip both and fall back to the legacy
// "make the change, open the PR" flow — useful for trivial edits where
// the bootstrap's overhead outweighs the validation benefit.
//
// Slack sends no option patch, so saved values are reused and missing
// keys default on. Follow-ups also receive the resolved options; when
// validation is true and a saved spec exists, they rerun the validation
// handoff without re-bootstrap.
func (b *Bot) prepareAgentRun(ctx context.Context, orgID, threadID, requestID, text string, out blocks.Emitter) (context.Context, runstore.Run, bool) {
	run, runOK, runErr := b.createAgentRun(ctx, orgID, threadID, requestID, text)
	if runErr != nil {
		b.log.Error("agent run create failed", "org", orgID, "thread", threadID, "request_id", requestID, "error", runErr)
		emitPreRunError(ctx, out, "Run could not start", "Hetchy could not create a durable run record for this turn. Try again.")
		return ctx, runstore.Run{}, false
	}
	if !runOK {
		b.log.Info("duplicate in-flight agent run ignored",
			"org", orgID, "thread", threadID, "request_id", requestID, "run_id", run.ID, "state", run.State)
		if run.RequestID != "" && run.RequestID != requestID {
			emitPreRunError(ctx, out, "Run already in flight", "This chat already has a turn in flight. Reload to reattach before sending another message.")
		}
		return ctx, run, false
	}
	if run.ID != "" {
		ctx = contextWithAgentRun(ctx, run)
	}
	return ctx, run, true
}

func (b *Bot) HandleRequest(ctx context.Context, oc orgcfg.Config, text, requestID, threadID, userID string, optionPatch chatTaskOptionPatch, requestedAgent *string, requestedRepo *string, model ClaudeModel, out blocks.Emitter, incomingAttachments ...convstore.Attachment) {
	model = normalizeClaudeModel(model)
	requestedRepoExplicit := requestedRepo != nil
	requestedOwner, requestedName, requestedRepoOK := parseRequestedRepo(requestedRepo)
	b.log.Info("request received",
		"org", oc.OrgID,
		"request_id", requestID,
		"thread_id", threadID,
		"requested_agent", requestedAgentSlug(requestedAgent),
		"requested_repo", requestedRepoSlug(requestedOwner, requestedName),
		"model", model,
		"text_len", len(text),
		"attachments", len(incomingAttachments),
		"text_preview", truncate(text, 200),
	)

	recorder := blocks.NewRecorder(maxBlocksPerTurn)
	var run runstore.Run
	var ok bool
	if ctx, run, ok = b.prepareAgentRun(ctx, oc.OrgID, threadID, requestID, text, out); !ok {
		return
	}

	// Wrap the transport emitter with a Recorder so every block streamed
	// to the user is also captured for the legacy response_blocks
	// projection. When a durable run row exists, the first tee target is
	// the canonical SSE event appender; web live fanout also happens
	// there so DB replay and live reattach use the same event IDs.
	emitters := []blocks.Emitter{recorder, out}
	if run.ID != "" && b.runs != nil && b.runs.Enabled() {
		runEmitter := newAgentRunEmitter(b.runs, run.ID, b.workerID, liveRunFromContext(ctx))
		ctx = contextWithAgentRunEmitter(ctx, runEmitter)
		emitters = []blocks.Emitter{
			runEmitter,
			recorder,
			out,
		}
	}
	emit := blocks.Tee(emitters...)

	rec, err := b.convs.Get(ctx, oc.OrgID, threadID)
	var opts chatTaskOptions
	var taskOptions map[string]bool
	if err == nil {
		model = modelForConversation(rec, model)
		rec.Model = string(model)
		opts, taskOptions = resolveChatTaskOptions(rec.TaskOptions, optionPatch)
		rec.TaskOptions = taskOptions
		if len(optionPatch) > 0 {
			if saveErr := b.convs.SaveTaskOptions(ctx, rec.OrgID, rec.ThreadID, taskOptions); saveErr != nil {
				b.log.Warn("save chat task options",
					"org", rec.OrgID, "thread", rec.ThreadID, "error", saveErr)
			}
		}
	} else {
		opts, taskOptions = resolveChatTaskOptions(nil, optionPatch)
	}
	if title, body, missing := missingCredentialError(model, oc); missing {
		b.log.Warn("org missing agent credentials", "org", oc.OrgID, "model", model, "provider", modelProvider(model))
		emit.Error(title, body)
		b.markRunState(ctx, runstore.StateFailed, errors.New("missing agent credentials"))
		return
	}
	switch {
	case err == nil && rec.SandboxID != "" && rec.PRURL != "":
		// Live conversation — agent succeeded at least once, PR exists.
		agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, rec.AgentSlug, emit)
		if !ok {
			b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
			return
		}
		if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, len(rec.History), incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.handleFollowUp(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
		return
	case err == nil && rec.SandboxID != "":
		// Sandbox was created but the agent failed before producing a
		// PR. Retry: archive the orphan sandbox + spawn a fresh one.
		agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, mutableConversationAgentSlug(rec.AgentSlug, requestedAgent), emit)
		if !ok {
			b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
			return
		}
		if err := b.replaceIncomingAttachmentsForTurn(ctx, oc.OrgID, threadID, 0, incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.handleRetryAfterFailure(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
		return
	case err == nil:
		saveAttachments := b.saveIncomingAttachments
		if rec.GitHubOwner != "" && rec.GitHubRepo != "" {
			saveAttachments = b.replaceIncomingAttachmentsForTurn
		}
		if err := saveAttachments(ctx, oc.OrgID, threadID, 0, incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.handlePendingConversation(ctx, oc, rec, text, requestID, requestedAgent, requestedOwner, requestedName, requestedRepoOK, opts, model, recorder, emit)
		return
	case errors.Is(err, convstore.ErrNotFound):
		// fall through — new conversation
	default:
		b.log.Error("convstore get", "error", err)
		emit.Error("Conversation lookup failed", fmt.Sprintf("`%v`", err))
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	// New conversation. Prefer an explicit per-turn picker selection
	// (composer repo dropdown), fall back to the org default, and as
	// a last resort stash the request and ask the user which repo to
	// use.
	agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, requestedAgentSlug(requestedAgent), emit)
	if !ok {
		b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
		return
	}
	owner, name, ok := resolveRequestedOrDefaultRepo(requestedOwner, requestedName, requestedRepoOK, requestedRepoExplicit, oc.DefaultGitHubOwner, oc.DefaultGitHubRepo)
	if !ok {
		emit.Notify("Which repository?", "Reply with `owner/name`.\n(You can save a default at /settings/org → Integrations.)")
		partial := convstore.Record{
			OrgID:          oc.OrgID,
			ThreadID:       threadID,
			History:        []string{text},
			ResponseBlocks: [][]blocks.Block{recorder.Snapshot()},
			CreatorID:      userID,
			AgentSlug:      agent.Slug,
			Model:          string(model),
			TaskOptions:    taskOptions,
		}
		if err := b.convs.Upsert(ctx, partial); err != nil {
			b.log.Error("convstore upsert (awaiting repo)", "error", err, "org", oc.OrgID, "thread", threadID)
		}
		if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, 0, incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.markRunState(ctx, runstore.StateSucceeded, nil)
		return
	}

	rec = convstore.Record{
		OrgID:       oc.OrgID,
		ThreadID:    threadID,
		History:     []string{text},
		GitHubOwner: owner,
		GitHubRepo:  name,
		CreatorID:   userID,
		AgentSlug:   agent.Slug,
		Model:       string(model),
		TaskOptions: taskOptions,
	}
	// Persist the row immediately — before we spend 10–30s creating the
	// sandbox — so the LHN sidebar and /api/v1/conversations both see this
	// chat as soon as the user clicks Send. Without this, a reload during
	// sandbox creation finds nothing and the chat disappears from the
	// list until the first persister tick fires inside runFreshAgent.
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert (new chat)", "error", err, "org", oc.OrgID, "thread", threadID)
	}
	if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, 0, incomingAttachments); err != nil {
		b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
		emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	b.runFreshAgent(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
}

// handlePendingConversation routes the "row exists but no PR yet"
// branches: a sandbox-built failure that needs a retry on the same
// (or picker-overridden) repo, and the awaiting-repo state where the
// user is either typing `owner/name` or has picked one in the
// composer. Extracted from HandleRequest so the main entry point
// stays under the cyclomatic-complexity lint cap.
func (b *Bot) handlePendingConversation(ctx context.Context, oc orgcfg.Config, rec convstore.Record, text, requestID string, requestedAgent *string, requestedOwner, requestedName string, requestedRepoOK bool, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	// Sub-state 1: prior turn resolved a repo but sandbox creation
	// failed. Retry with the new text — unless the user has picked a
	// different repo via the composer, in which case respect the
	// override before resuming.
	if rec.GitHubOwner != "" && rec.GitHubRepo != "" {
		agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, mutableConversationAgentSlug(rec.AgentSlug, requestedAgent), emit)
		if !ok {
			b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
			return
		}
		if requestedRepoOK {
			rec.GitHubOwner = requestedOwner
			rec.GitHubRepo = requestedName
		}
		b.handleRetryAfterFailure(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
		return
	}
	// Sub-state 2: awaiting-repo. Composer picker selection trumps
	// the parsed `owner/name` answer; we launch against the
	// preserved first-turn request rather than the picker turn's
	// text.
	agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, mutableConversationAgentSlug(rec.AgentSlug, requestedAgent), emit)
	if !ok {
		b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
		return
	}
	if requestedRepoOK {
		rec.GitHubOwner = requestedOwner
		rec.GitHubRepo = requestedName
		rec.AgentSlug = agent.Slug
		rec.Model = string(model)
		originalRequest := text
		if len(rec.History) > 0 && rec.History[0] != "" {
			originalRequest = rec.History[0]
		}
		b.runFreshAgent(ctx, oc, rec, agent, originalRequest, requestID, opts, model, recorder, emit)
		return
	}
	b.handleAwaitingRepoReply(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
}

func requestedAgentSlug(requested *string) string {
	if requested == nil {
		return ""
	}
	return strings.TrimSpace(*requested)
}

// parseRequestedRepo extracts an (owner, name) from the optional composer
// repo picker selection. A nil or unparseable value is treated as "no
// selection" — callers fall back to whatever the conversation/org already
// has. We deliberately reuse parseOwnerRepo so a future API client
// passing `https://github.com/owner/name` is handled the same way as the
// chat-text reply path.
func parseRequestedRepo(requested *string) (owner, name string, ok bool) {
	if requested == nil {
		return "", "", false
	}
	return parseOwnerRepo(*requested)
}

func requestedRepoSlug(owner, name string) string {
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

// resolveRequestedOrDefaultRepo returns the repo to use for a new
// conversation. An explicit composer-picker selection wins outright;
// when the picker explicitly sends an empty repository, skip the org
// default so the caller can ask the user which repo to use. Older
// clients that omit the field still fall back to the org default.
func resolveRequestedOrDefaultRepo(reqOwner, reqName string, hasReq, explicitRepoField bool, defOwner, defName string) (owner, name string, ok bool) {
	if hasReq && reqOwner != "" && reqName != "" {
		return reqOwner, reqName, true
	}
	if explicitRepoField {
		return "", "", false
	}
	if defOwner != "" && defName != "" {
		return defOwner, defName, true
	}
	return "", "", false
}

func mutableConversationAgentSlug(pinnedSlug string, requested *string) string {
	if requested != nil {
		return requestedAgentSlug(requested)
	}
	return strings.TrimSpace(pinnedSlug)
}

func (b *Bot) selectAgentForConversation(ctx context.Context, orgID, slug string, emit blocks.Emitter) (agents.Profile, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return agents.Profile{}, true
	}
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	agent, err := store.Resolve(ctx, orgID, slug)
	if err != nil {
		emit.Error("Unknown agent", fmt.Sprintf("I couldn't find an enabled Hetchy agent matching `%s`.", strings.TrimSpace(slug)))
		return agents.Profile{}, false
	}
	return agent, true
}

func modelForConversation(rec convstore.Record, requested ClaudeModel) ClaudeModel {
	if strings.TrimSpace(rec.Model) != "" {
		return normalizeClaudeModel(ClaudeModel(rec.Model))
	}
	return normalizeClaudeModel(requested)
}

// handleAwaitingRepoReply parses the user's reply as `owner/name`. On
// success it stamps the conversation with the chosen repo and runs the
// agent against the original request stored in History[0]. On failure
// it nudges the user to retry without modifying the saved row, so the
// state machine stays in `awaiting repo` until they get it right.
//
// rec on entry may have GitHubOwner already set (from a previous
// resolve-failed attempt); we'll overwrite both with whatever this
// message resolves to.
func (b *Bot) handleAwaitingRepoReply(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	owner, name, ok := parseOwnerRepo(text)
	if !ok {
		emit.Notify("Try again", "I couldn't parse that as `owner/name`. For example `acme/website`.")
		// Persist the bot's nudge so a UI replay shows it; keep the
		// row otherwise unchanged.
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (parse retry)", "error", err)
		}
		b.markRunState(ctx, runstore.StateSucceeded, nil)
		return
	}
	if len(rec.History) == 0 {
		// Defensive: a partial row should always have History[0]
		// (the original request that triggered the question). Fall
		// back to treating the parsed text as the request itself
		// rather than crashing on the empty slice.
		rec.History = []string{fmt.Sprintf("Work in %s/%s.", owner, name)}
	}
	rec.GitHubOwner = owner
	rec.GitHubRepo = name
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	originalRequest := rec.History[0]
	b.runFreshAgent(ctx, oc, rec, agent, originalRequest, requestID, opts, model, recorder, emit)
}

// clearRepoOnFailure rewrites a partial conversation back to the
// "awaiting repo" state when runFreshAgent's resolveRepo fails. Without
// this, the row would be persisted with GitHubOwner set but no
// SandboxID, and the user's next message would re-enter the same dead
// branch — they'd be stuck. Clearing the repo lets them answer with a
// different `owner/name` on the next turn.
func clearRepoOnFailure(rec *convstore.Record) {
	rec.GitHubOwner = ""
	rec.GitHubRepo = ""
}

// handleRetryAfterFailure resumes a fresh-agent attempt that
// previously failed (either before producing a sandbox or after the
// agent crashed mid-run). The repo was resolved successfully on the
// prior turn, so we keep it and re-run with the new user message as
// the request — the repo isn't the problem and forcing the user to
// retype `owner/name` would be noise. The new message replaces
// History[0] (this is still the first real turn — the row exists from
// the entry-Upsert HandleRequest does before the agent even starts,
// plus any failure blocks the persister recorded mid-run) so a
// follow-up only sees the request that actually shipped.
//
// If the failed attempt left an orphan sandbox (rec.SandboxID set,
// rec.PRURL empty), archive it best-effort before spawning a fresh
// one. The user retrying is the signal that they're done debugging
// the previous failure; without this we'd leak a Daytona sandbox per
// retry.
func (b *Bot) handleRetryAfterFailure(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	if rec.SandboxID != "" {
		if sb, err := b.getSandbox(ctx, rec.SandboxID); err == nil {
			b.cleanupSandbox(ctx, sb, "orphan retry")
		} else {
			b.log.Warn("orphan sandbox lookup failed; assuming already gone", "sandbox", rec.SandboxID, "error", err)
		}
		rec.SandboxID = ""
	}
	rec.History = []string{text}
	rec.ResponseBlocks = nil
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	b.runFreshAgent(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
}

// runFreshAgent creates a new sandbox, mints an installation token
// scoped to rec's repo, runs the agent on userRequest, and persists
// the resulting conversation. Shared by the new-conversation, awaiting-
// repo-reply, and "had repo but no sandbox" paths so they all stamp
// the row identically.
func (b *Bot) runFreshAgent(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, userRequest, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	model = normalizeClaudeModel(model)
	rec.Model = string(model)
	b.markRunKind(ctx, "fresh")
	repo, err := b.resolveRepoForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before the sandbox was created.")
			appendBlocksToFirstTurn(&rec, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (cancel before sandbox)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Warn("resolve repo failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo not accessible", fmt.Sprintf("`%s/%s` isn't accessible to this organization's GitHub App installations. Install the App on it at /settings/org → Integrations and try again, or reply with a different `owner/name`.", rec.GitHubOwner, rec.GitHubRepo))
		// Drop back to the awaiting-repo state so the user's next
		// message can pick a different repo without being interpreted
		// as a follow-up to a half-launched conversation.
		clearRepoOnFailure(&rec)
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (resolve fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	rec.AgentSlug = agent.Slug
	// Notify first so the user sees activity even if branchNameFor
	// stalls on Anthropic — the slug request has a tight timeout but
	// blocking the "Starting" message on it makes a slow network look
	// like the chat is frozen.
	if agent.Slug == "" {
		emit.Notify("Starting", fmt.Sprintf("Spinning up an isolated sandbox for your request in `%s` (base: `%s`)…", repo.Slug, repo.BaseBranch))
	} else {
		emit.Notify("Starting", fmt.Sprintf("Spinning up `%s` in an isolated sandbox for your request in `%s` (base: `%s`)…", agent.DisplayName, repo.Slug, repo.BaseBranch))
	}
	branch := b.branchNameFor(ctx, oc, userRequest)
	b.markRunBranch(ctx, branch)

	envVars := map[string]string{}
	if oc.SXKey != "" {
		envVars["SX_KEY"] = oc.SXKey
	}
	volumes := []types.VolumeMount(nil)
	cacheVolumeID := ""
	if mount, mounted := b.resolveDaytonaCacheMount(ctx, oc, repo); mounted {
		volumes = append(volumes, mount)
		repo.CacheMounted = true
		cacheVolumeID = mount.VolumeID
	}
	addDaytonaCacheEnv(envVars, b.cfg, oc, repo, repo.CacheMounted)
	labels := daytonaSandboxLabels(b.cfg, oc, cacheVolumeID)
	autoArchiveMinutes := b.daytonaAutoArchiveMinutes()
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars:             envVars,
			Labels:              labels,
			Volumes:             volumes,
			AutoArchiveInterval: &autoArchiveMinutes,
		},
		Snapshot: b.cfg.Snapshot,
	})
	if err != nil {
		if liveRunCancelled(ctx) {
			b.log.Info("sandbox create stopped", "request_id", requestID, "error", err)
			emit.Result("Stopped", "Stopped before the sandbox finished starting.")
		} else if ctx.Err() != nil {
			b.log.Error("sandbox create cancelled", "request_id", requestID, "error", err)
			emit.Error("Sandbox cancelled", "Sandbox creation was cancelled before it could start. Try again.")
		} else {
			b.log.Error("sandbox create failed", "request_id", requestID, "error", err)
			emit.Error("Sandbox failed", "Couldn't start a sandbox for your request. Check the server logs for details and try again.")
		}
		// Persist the streamed blocks so a refresh shows the failure
		// instead of an empty chat. For a brand-new conversation the
		// row hasn't been written yet — without this the user loses
		// every block they just watched stream by. The dispatcher
		// recognises (GitHubOwner != "" && SandboxID == "" && first
		// turn already has blocks) as "retry pending" and re-runs on
		// the next message instead of asking for a repo.
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (sandbox create fail)", "error", uerr)
		}
		if liveRunCancelled(ctx) {
			b.markRunState(ctx, runstore.StateCancelled, err)
		} else {
			b.markRunState(ctx, runstore.StateFailed, err)
		}
		return
	}
	// Mark this fresh-run sandbox as owned by the current turn. The
	// conversation cancel handler uses this only as an opportunistic cleanup path;
	// the agent goroutine below remains the authoritative cleanup owner
	// because a cancel can arrive in the small window before this ID is set.
	setLiveRunSandboxID(ctx, sb.ID, true)
	rec.SandboxID = sb.ID
	rec.Branch = branch
	b.markRunSandbox(ctx, sb.ID)
	if err := b.convs.Upsert(context.Background(), rec); err != nil {
		b.log.Error("convstore upsert (sandbox ready)", "error", err)
	}
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID, "auto_archive_minutes", autoArchiveMinutes, "state", sb.State)
	sandboxReadyID := emit.Start(blocks.KindNotify, "Sandbox ready", map[string]any{"tag": sandboxReadySSETag})
	emit.Append(sandboxReadyID, fmt.Sprintf("`%s` is up — cloning repo and starting %s.", sb.ID, agentRuntimeDisplayName(model)))
	emit.Done(sandboxReadyID, "")

	agentRequest, err := b.promptWithSandboxAttachments(ctx, sb, rec.OrgID, rec.ThreadID, 0, requestID, userRequest, emit)
	if err != nil {
		b.log.Error("sandbox attachment upload failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Attachment upload failed", fmt.Sprintf("Could not copy the attached files into sandbox `%s`. Try again.", sb.ID))
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (attachment upload fail)", "error", uerr)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		b.cleanupSandboxWithTimeout(sb, "attachment upload failed")
		return
	}

	// Persist progress every 2 s for the rest of the run so a
	// reload (or bot crash) doesn't lose blocks. The persister
	// writes only history + response_blocks + creator_id via
	// SaveProgress; the terminal Upsert below remains the
	// canonical write for sandbox_id / branch / pr_url.
	// appendToFirstTurn matches appendBlocksToFirstTurn used by
	// every terminal Upsert in this function — a mismatch would
	// let a late tick overwrite the terminal save with a different
	// shape and drop turns from the UI.
	persister := newChatPersister(b.log, b.convs, recorder, rec, appendToFirstTurn, 2*time.Second)
	persisterCtx, cancelPersister := context.WithCancel(ctx)
	go persister.Run(persisterCtx)
	defer func() {
		cancelPersister()
		persister.Stop()
	}()

	prURL, runErr := b.runAgentForRequest(ctx, sb, repo, oc, agent, agentRequest, requestID, branch, opts, model, emit)
	if runErr != nil {
		if liveRunCancelled(ctx) {
			b.log.Info("agent run stopped", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
			b.cleanupSandboxWithTimeout(sb, "cancelled fresh run")
			emit.Result("Stopped", fmt.Sprintf("Stopped the run and archived sandbox `%s`.", sb.ID))
			appendBlocksToFirstTurn(&rec, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (agent stopped)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, runErr)
			return
		}
		if errors.Is(runErr, errAgentRunDurability) {
			b.log.Error("agent run durability failed; leaving run recoverable", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
			b.markRunState(ctx, runstore.StateRecovering, runErr)
			return
		}
		b.log.Error("agent run failed", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
		if isAgentTimeout(runErr) {
			emit.Error("Agent timed out", fmt.Sprintf("The agent exceeded its time limit on sandbox `%s`. Reply here to retry (the orphan sandbox will be archived automatically) or check the server logs for details.", sb.ID))
		} else if errors.Is(runErr, errReportedPRNotVerified) {
			emit.Error("PR not verified", fmt.Sprintf("The agent reported a PR URL, but GitHub did not verify it for branch `%s`. Sandbox `%s` is left running for debugging — check the transcript and server logs for details.", branch, sb.ID))
		} else {
			emit.Error("Agent failed", fmt.Sprintf("Something went wrong while running the agent. Sandbox `%s` is left running for debugging — reply here to retry (the orphan sandbox will be archived automatically) or check the server logs for details.", sb.ID))
		}
		// Persist sb.ID so handleRetryAfterFailure can archive the
		// stale sandbox on the next user message — without this we'd
		// leak a sandbox per retry. PRURL stays empty, which is how
		// the dispatcher tells "agent failed mid-run, clean up first"
		// apart from a real follow-up.
		rec.SandboxID = sb.ID
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (agent fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, runErr)
		return
	}

	if prURL == "" {
		emit.Result("Done!", noPullRequestResultBody(false))
		if err := agentRunDurabilityErr(ctx); err != nil {
			b.markRunState(ctx, runstore.StateRecovering, err)
			return
		}
		rec.SandboxID = ""
		rec.Branch = ""
		rec.PRURL = ""
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (agent answer-only)", "error", err)
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.markRunState(ctx, runstore.StateSucceeded, nil)
		b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "agent-"+requestID))
		b.stopAndArchiveSandbox(ctx, sb)
		return
	}

	emit.Result("Done!", prURL+"\n\nReply here to make further changes to this PR.")
	if err := agentRunDurabilityErr(ctx); err != nil {
		b.markRunState(ctx, runstore.StateRecovering, err)
		return
	}

	rec.SandboxID = sb.ID
	rec.Branch = branch
	rec.PRURL = prURL
	appendBlocksToFirstTurn(&rec, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	b.markRunState(ctx, runstore.StateSucceeded, nil)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "agent-"+requestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func (b *Bot) handleFollowUp(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	model = modelForConversation(rec, model)
	modeDecision := b.decideFollowUpMode(ctx, oc, rec, text)
	mode := modeDecision.Mode
	b.log.Info("follow-up received", "org", oc.OrgID, "sandbox", rec.SandboxID, "branch", rec.Branch, "pr", rec.PRURL, "agent", agent.Slug, "model", model, "mode", mode, "mode_confidence", modeDecision.Confidence, "mode_reason", modeDecision.Reason)
	b.markRunKind(ctx, "followup")
	b.markRunBranch(ctx, rec.Branch)
	b.markRunSandbox(ctx, rec.SandboxID)
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	if agent.Slug == "" {
		emit.Notify("Resuming", fmt.Sprintf("Resuming work on %s…", rec.PRURL))
	} else {
		emit.Notify("Resuming", fmt.Sprintf("Resuming `%s` on %s…", agent.DisplayName, rec.PRURL))
	}

	repo, err := b.resolveRepoForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before resuming the sandbox.")
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped before resume)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Warn("resolve repo for follow-up failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo access lost", fmt.Sprintf("Lost access to `%s/%s` — check the GitHub App install at /settings/org → Integrations.", rec.GitHubOwner, rec.GitHubRepo))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up resolve fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	sb, err := b.getSandbox(ctx, rec.SandboxID)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before the sandbox was resumed.")
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped before sandbox get)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Error("sandbox get failed", "sandbox", rec.SandboxID, "request_id", requestID, "error", err)
		emit.Error("Sandbox missing", fmt.Sprintf("Could not find sandbox `%s` — it may have been archived or removed. Start a new chat to continue.", rec.SandboxID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up sandbox missing)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	// Follow-ups reuse the conversation sandbox. Do not let the cancel
	// handler archive it; stopping this turn should leave the chat able to
	// continue on the same sandbox.
	setLiveRunSandboxID(ctx, sb.ID, false)
	if err := b.resumeSandboxForRun(ctx, sb, emit); err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", fmt.Sprintf("Stopped this turn. Sandbox `%s` is still available; send another message to continue.", sb.ID))
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped during resume)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, err)
			return
		}
		b.log.Error("sandbox resume failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		var timeoutErr *sdkerrors.DaytonaTimeoutError
		title := "Sandbox resume failed"
		msg := fmt.Sprintf("Could not start sandbox `%s`. Try again, or open a fresh chat.", sb.ID)
		if errors.As(err, &timeoutErr) {
			title = "Sandbox slow to start"
			msg = fmt.Sprintf("Sandbox `%s` is taking unusually long to start. Wait a moment and reload, or open a fresh chat if it persists.", sb.ID)
		}
		emit.Error(title, msg)
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up sandbox resume)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	agentText, err := b.promptWithSandboxAttachments(ctx, sb, rec.OrgID, rec.ThreadID, len(rec.History), requestID, text, emit)
	if err != nil {
		b.log.Error("sandbox attachment upload failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Attachment upload failed", fmt.Sprintf("Could not copy the attached files into sandbox `%s`. Try again.", sb.ID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (follow-up attachment upload fail)", "error", uerr)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	// Persister sees a forward-looking rec where the new user turn's
	// text is already in history — otherwise a mid-run reload would
	// render the user's message back in the previous turn instead of
	// the in-flight one. appendAsNewTurn matches appendBlocksAsNewTurn
	// used by the terminal Upsert in this function.
	recForPersist := rec
	recForPersist.History = append(append([]string(nil), rec.History...), text)
	persister := newChatPersister(b.log, b.convs, recorder, recForPersist, appendAsNewTurn, 2*time.Second)
	persisterCtx, cancelPersister := context.WithCancel(ctx)
	go persister.Run(persisterCtx)
	defer func() {
		cancelPersister()
		persister.Stop()
	}()

	prURL, err := b.runFollowUpForRequest(ctx, sb, repo, oc, rec, agent, agentText, requestID, opts, model, mode, emit)
	if err != nil {
		if liveRunCancelled(ctx) {
			b.log.Info("follow-up stopped", "sandbox", sb.ID, "request_id", requestID, "error", err)
			emit.Result("Stopped", fmt.Sprintf("Stopped this turn. Sandbox `%s` is still available; send another message to continue.", sb.ID))
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, err)
			b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
			return
		}
		if errors.Is(err, errAgentRunDurability) {
			b.log.Error("follow-up durability failed; leaving run recoverable", "sandbox", sb.ID, "request_id", requestID, "error", err)
			b.markRunState(ctx, runstore.StateRecovering, err)
			return
		}
		b.log.Error("follow-up failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		if errors.Is(err, errReportedPRNotVerified) {
			emit.Error("PR not verified", fmt.Sprintf("The agent reported a PR URL, but GitHub did not verify it for branch `%s`. Sandbox `%s` is left running for debugging — check the transcript and server logs for details.", rec.Branch, sb.ID))
		} else {
			emit.Error("Agent failed", fmt.Sprintf("Something went wrong while running the agent. Sandbox `%s` is left running for debugging — check the server logs for details.", sb.ID))
		}
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up agent fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
		return
	}

	// Result first so the recorded snapshot includes the closing block,
	// then upsert with the new user turn + this turn's blocks.
	resultBody := prURL
	if resultBody == "" {
		resultBody = noPullRequestResultBody(true)
	}
	emit.Result("Done!", resultBody)
	if err := agentRunDurabilityErr(ctx); err != nil {
		b.markRunState(ctx, runstore.StateRecovering, err)
		return
	}

	if prURL != "" {
		rec.PRURL = prURL
	}
	appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	b.markRunState(ctx, runstore.StateSucceeded, nil)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func noPullRequestResultBody(followup bool) string {
	if followup {
		return "No new pull request URL was reported; keeping the existing PR."
	}
	return "No pull request was created."
}

func (b *Bot) resolveRepoForRun(ctx context.Context, orgID, owner, name string) (repoCtx, error) {
	if b.resolveRepoFn != nil {
		return b.resolveRepoFn(ctx, orgID, owner, name)
	}
	return b.resolveRepo(ctx, orgID, owner, name)
}

func (b *Bot) runAgentForRequest(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, userRequest, requestID, branch string, opts chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
	if b.runAgentFn != nil {
		return b.runAgentFn(ctx, sb, repo, oc, agent, userRequest, requestID, branch, opts, model, emit)
	}
	return b.runAgent(ctx, sb, repo, oc, agent, userRequest, requestID, branch, opts, model, emit)
}

func (b *Bot) runFollowUpForRequest(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, mode followUpMode, emit blocks.Emitter) (string, error) {
	if b.runFollowUpFn != nil {
		return b.runFollowUpFn(ctx, sb, repo, oc, rec, agent, text, requestID, opts, model, mode, emit)
	}
	return b.runFollowUp(ctx, sb, repo, oc, rec, agent, text, requestID, opts, model, mode, emit)
}

func (b *Bot) getSandbox(ctx context.Context, sandboxID string) (*daytona.Sandbox, error) {
	if b.getSandboxFn != nil {
		return b.getSandboxFn(ctx, sandboxID)
	}
	if b.daytona == nil {
		return nil, errors.New("daytona client not configured")
	}
	return b.daytona.Get(ctx, sandboxID)
}

func (b *Bot) resumeSandboxForRun(ctx context.Context, sb *daytona.Sandbox, emit blocks.Emitter) error {
	if b.resumeSandboxFn != nil {
		return b.resumeSandboxFn(ctx, sb, emit)
	}
	return b.resumeSandbox(ctx, sb, emit)
}

func (b *Bot) daytonaAutoArchiveMinutes() int {
	if b.cfg.DaytonaAutoArchiveMinutes > 0 {
		return b.cfg.DaytonaAutoArchiveMinutes
	}
	return defaultDaytonaAutoArchiveMinutes
}

func (b *Bot) stopAndArchiveSandbox(ctx context.Context, sb *daytona.Sandbox) {
	if sb == nil {
		return
	}
	if b.stopAndArchiveFn != nil {
		b.stopAndArchiveFn(ctx, sb)
		return
	}
	autoArchiveMinutes := b.daytonaAutoArchiveMinutes()
	if err := b.setSandboxAutoArchiveInterval(ctx, sb, autoArchiveMinutes); err != nil {
		b.log.Error("sandbox auto-archive configuration failed; archiving immediately",
			"sandbox", sb.ID,
			"state", sb.State,
			"auto_archive_minutes", autoArchiveMinutes,
			"error", err,
		)
		b.stopAndArchiveImmediately(ctx, sb, "auto-archive configuration failed")
		return
	}
	started := time.Now()
	if err := b.stopSandbox(ctx, sb); err != nil {
		b.log.Error("sandbox stop failed",
			"sandbox", sb.ID,
			"state", sb.State,
			"auto_archive_minutes", autoArchiveMinutes,
			"duration", time.Since(started).Round(time.Millisecond),
			"error", err,
		)
		return
	}
	b.log.Info("sandbox stopped; auto-archive scheduled",
		"sandbox", sb.ID,
		"state", sb.State,
		"auto_archive_minutes", autoArchiveMinutes,
		"duration", time.Since(started).Round(time.Millisecond),
	)
}

func (b *Bot) setSandboxAutoArchiveInterval(ctx context.Context, sb *daytona.Sandbox, minutes int) error {
	if b.setAutoArchiveIntervalFn != nil {
		return b.setAutoArchiveIntervalFn(ctx, sb, &minutes)
	}
	return sb.SetAutoArchiveInterval(ctx, &minutes)
}

func (b *Bot) stopSandbox(ctx context.Context, sb *daytona.Sandbox) error {
	if b.stopSandboxFn != nil {
		return b.stopSandboxFn(ctx, sb)
	}
	return sb.Stop(ctx)
}

func (b *Bot) archiveSandbox(ctx context.Context, sb *daytona.Sandbox) error {
	if b.archiveSandboxFn != nil {
		return b.archiveSandboxFn(ctx, sb)
	}
	return sb.Archive(ctx)
}

func (b *Bot) stopAndArchiveImmediately(ctx context.Context, sb *daytona.Sandbox, reason string) {
	started := time.Now()
	if err := b.stopSandbox(ctx, sb); err != nil {
		b.log.Warn("sandbox immediate archive stop failed",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(started).Round(time.Millisecond),
			"error", err,
		)
	} else {
		b.log.Info("sandbox stopped before immediate archive",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(started).Round(time.Millisecond),
		)
	}
	archiveStarted := time.Now()
	if err := b.archiveSandbox(ctx, sb); err != nil {
		b.log.Warn("sandbox immediate archive failed",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(archiveStarted).Round(time.Millisecond),
			"error", err,
		)
	} else {
		b.log.Info("sandbox immediate archive ok",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(archiveStarted).Round(time.Millisecond),
		)
	}
}

// resolveRepo joins org → installations → repos to find which
// installation grants access to (owner, name), then mints a fresh
// installation token scoped to that single repo. The 1-hour token is
// cached inside githubapp.App until 5 min before expiry.
func (b *Bot) resolveRepo(ctx context.Context, orgID, owner, name string) (repoCtx, error) {
	if owner == "" || name == "" {
		return repoCtx{}, fmt.Errorf("repo not selected (owner=%q name=%q)", owner, name)
	}
	if b.app == nil {
		return repoCtx{}, errors.New("github app not configured for this environment")
	}
	row, err := b.store.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  name,
	})
	if err != nil {
		return repoCtx{}, fmt.Errorf("lookup %s/%s for org %s: %w", owner, name, orgID, err)
	}
	tok, exp, err := b.app.InstallationToken(ctx, row.InstallationID, []int64{row.RepoID})
	if err != nil {
		// IDs not visible in the caller's "resolve repo failed" log.
		b.log.Warn("github installation token mint failed",
			"installation_id", row.InstallationID, "repo_id", row.RepoID, "error", err)
		return repoCtx{}, fmt.Errorf("mint installation token: %w", err)
	}
	return repoCtx{
		Slug:         row.Owner + "/" + row.Name,
		BaseBranch:   row.DefaultBranch,
		GitHubToken:  tok,
		InstallID:    row.InstallationID,
		RepoID:       row.RepoID,
		TokenExpires: exp,
	}, nil
}

// parseOwnerRepo extracts (owner, name) from a free-form chat reply.
// Tolerates surrounding whitespace, trailing punctuation, and a leading
// `https://github.com/` URL — but rejects anything that doesn't look
// like exactly one `/`-separated pair so we don't silently accept
// gibberish like "the auth one".
func parseOwnerRepo(s string) (owner, name string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(s, ".git")
	// Strip trailing path segments past owner/name (e.g. /tree/main).
	if i := strings.Index(s, "/"); i >= 0 {
		if j := strings.Index(s[i+1:], "/"); j >= 0 {
			s = s[:i+1+j]
		}
	}
	s = strings.TrimRight(s, ".,;:!?)")
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])
	if owner == "" || name == "" {
		return "", "", false
	}
	if !validGitHubName(owner) || !validGitHubName(name) {
		return "", "", false
	}
	return owner, name, true
}

// validGitHubName is a conservative check: GitHub allows letters,
// digits, hyphens, underscores, and dots in repo names; owners are
// stricter (no leading hyphen, no consecutive hyphens) but for the
// purpose of this parse we accept the union and let a downstream lookup
// fail if the value is not a real repo. We do reject the path-traversal
// shapes ".", "..", and any name that leads with "." or "-" so that
// echoing the value back in an error message can't smuggle a relative
// path through to a UI that renders it as a link.
func validGitHubName(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	if s == "." || s == ".." {
		return false
	}
	if s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// appendBlocksToFirstTurn appends `next` to rec.ResponseBlocks[0],
// allocating the slice if empty. Used to grow the bot's response across
// multi-step interactions on the original turn (ask-for-repo → answer →
// agent run) without adding a History entry.
func appendBlocksToFirstTurn(rec *convstore.Record, next []blocks.Block) {
	if len(next) == 0 {
		return
	}
	if len(rec.ResponseBlocks) == 0 {
		rec.ResponseBlocks = [][]blocks.Block{next}
		return
	}
	rec.ResponseBlocks[0] = append(rec.ResponseBlocks[0], next...)
}

// appendBlocksAsNewTurn appends a new (text, blocks) entry to History
// and ResponseBlocks, keeping them index-paired.
func appendBlocksAsNewTurn(rec *convstore.Record, text string, next []blocks.Block) {
	rec.History = append(rec.History, text)
	if next == nil {
		next = []blocks.Block{}
	}
	rec.ResponseBlocks = append(rec.ResponseBlocks, next)
}

// isAgentTimeout reports whether err came from a wall-clock or idle
// timeout in shLines — used to surface a more actionable error message
// to the user than the generic "something went wrong" fallback.
func isAgentTimeout(err error) bool {
	return errors.Is(err, ErrStepWallTimeout) || errors.Is(err, ErrStepIdleTimeout)
}

// isTransientError reports whether err is a retryable Daytona API error:
// rate-limit (429), server-side 5xx responses, and network-level failures
// (StatusCode == 0) are all considered transient.
//
// DaytonaTimeoutError is explicitly excluded: it embeds *DaytonaError with
// StatusCode==0, which would otherwise look like a network failure. A sandbox
// that timed out is genuinely slow — retrying would just add another full
// timeout on top of the one already spent.
func isTransientError(err error) bool {
	var timeoutErr *sdkerrors.DaytonaTimeoutError
	if errors.As(err, &timeoutErr) {
		return false
	}
	var rateLimitErr *sdkerrors.DaytonaRateLimitError
	if errors.As(err, &rateLimitErr) {
		return true
	}
	var dayErr *sdkerrors.DaytonaError
	if errors.As(err, &dayErr) {
		return dayErr.StatusCode == 0 ||
			dayErr.StatusCode == http.StatusTooManyRequests ||
			(dayErr.StatusCode >= 500 && dayErr.StatusCode < 600)
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// heredocWriteCmd builds a shell command that writes body to path via
// a single-quoted heredoc with a content-derived terminator. If
// chmodExec is true, the command also `chmod +x` the resulting file.
//
// The terminator is "SFEOF_" plus the first 8 hex chars of sha256(body),
// which makes a collision with a body line cryptographically negligible.
// The previous hardcoded "SFEOF" terminator silently truncated any file
// whose body happened to contain a bare line of that text — a real
// hazard for the bootstrap prompt, which embeds README/Makefile/compose
// excerpts from arbitrary user repos.
func heredocWriteCmd(path, body string, chmodExec bool) string {
	sum := sha256.Sum256([]byte(body))
	term := "SFEOF_" + hex.EncodeToString(sum[:])[:8]
	quoted := shellQuote(path)
	cmd := "cat > " + quoted + " << '" + term + "'\n" + body + "\n" + term
	if chmodExec {
		cmd += "\nchmod +x " + quoted
	}
	return cmd
}

// truncate caps s to at most n runes, appending "..." when it cuts.
// Rune-aware (not byte-aware) so multi-byte characters (emoji, CJK,
// non-ASCII filenames in Bash command titles) don't get split mid-
// codepoint and surface as mojibake in the UI.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// retryWithBackoff executes fn up to maxRetries times with exponential backoff
// for transient errors (rate-limit, 5xx, network failures). operation is a human-readable
// name used in log messages.
func (b *Bot) retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	var lastErr error
	backoff := b.retryBackoff

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := fn()
		if err == nil {
			if attempt > 1 {
				b.log.Info("operation succeeded after retry", "operation", operation, "attempt", attempt)
			}
			return nil
		}

		lastErr = err

		if attempt == maxRetries || !isTransientError(err) {
			break
		}

		b.log.Warn("operation failed, retrying",
			"operation", operation,
			"attempt", attempt,
			"max_retries", maxRetries,
			"backoff", backoff,
			"error", err,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= backoffMultiplier
		}
	}

	return lastErr
}

// createSandboxWithRetry attempts to create a Daytona sandbox with retry logic
// for transient errors. It tries up to maxRetries times with progressive backoff.
func (b *Bot) createSandboxWithRetry(ctx context.Context, params types.SnapshotParams) (*daytona.Sandbox, error) {
	var sb *daytona.Sandbox
	err := b.retryWithBackoff(ctx, "sandbox create", func() error {
		var err error
		sb, err = b.createFn(ctx, params)
		return err
	})
	return sb, err
}

func (b *Bot) cleanupSandboxByID(sandboxID, reason string) {
	if b.daytona == nil || strings.TrimSpace(sandboxID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sb, err := b.daytona.Get(ctx, sandboxID)
	if err != nil {
		b.log.Warn("sandbox cleanup lookup failed", "sandbox", sandboxID, "reason", reason, "error", err)
		return
	}
	b.cleanupSandbox(ctx, sb, reason)
}

func (b *Bot) currentAgentRunSessionID(ctx context.Context, fallback string) string {
	run, ok := agentRunFromContext(ctx)
	if !ok {
		return fallback
	}
	if run.SessionID != "" {
		return run.SessionID
	}
	if b.runs == nil || !b.runs.Enabled() {
		return fallback
	}
	latest, err := b.runs.Get(context.Background(), run.ID)
	if err != nil {
		b.log.Warn("agent run session lookup failed", "run_id", run.ID, "error", err)
		return fallback
	}
	if latest.SessionID == "" {
		return fallback
	}
	return latest.SessionID
}

func (b *Bot) cancelDurableRun(ctx context.Context, run runstore.Run, actor string) error {
	if b.runs == nil || !b.runs.Enabled() || run.ID == "" {
		return nil
	}
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		return fmt.Errorf("list cancel events: %w", err)
	}
	cancelEvents := cancelledAgentRunEvents(events)
	cancelled, err := b.runs.Cancel(ctx, run.ID, "cancel requested", b.workerID, agentRunLeaseDuration, cancelEvents)
	if err != nil {
		return fmt.Errorf("cancel run: %w", err)
	}
	projectEvents := appendPendingRunEvents(events, cancelled.ID, cancelEvents)
	if err := b.projectCancelledDurableRun(ctx, cancelled, projectEvents); err != nil {
		b.log.Warn("project cancelled run",
			"run_id", cancelled.ID,
			"org", cancelled.OrgID,
			"thread", cancelled.ThreadID,
			"error", err,
		)
	}
	cleanupOnCancel := cancelled.RunKind != "followup"
	b.log.Info("durable chat cancel requested",
		"org", cancelled.OrgID,
		"thread", cancelled.ThreadID,
		"run_id", cancelled.ID,
		"user", actor,
		"sandbox", cancelled.SandboxID,
		"session", cancelled.SessionID,
		"cleanup_on_cancel", cleanupOnCancel,
	)
	b.cleanupCancelledDurableRun(cancelled)
	return nil
}

func (b *Bot) projectCancelledDurableRun(ctx context.Context, run runstore.Run, events []runstore.Event) error {
	if b.convs == nil {
		return nil
	}
	return b.projectRecoveredConversation(ctx, run, "", events)
}

func appendPendingRunEvents(events []runstore.Event, runID string, pending []runstore.PendingEvent) []runstore.Event {
	out := append([]runstore.Event(nil), events...)
	var seq int64
	if len(out) > 0 {
		seq = out[len(out)-1].Seq
	}
	now := time.Now().UTC()
	for _, ev := range pending {
		seq++
		out = append(out, runstore.Event{
			RunID:     runID,
			Seq:       seq,
			Event:     ev.Event,
			Data:      ev.Data,
			CreatedAt: now,
		})
	}
	return out
}

func (b *Bot) cleanupCancelledDurableRun(run runstore.Run) {
	sandboxID := strings.TrimSpace(run.SandboxID)
	if sandboxID == "" {
		return
	}
	cleanupOnCancel := run.RunKind != "followup"
	if b.daytona == nil {
		if cleanupOnCancel {
			cleanup := b.cleanupSandboxByID
			if b.cleanupSandboxByIDFn != nil {
				cleanup = b.cleanupSandboxByIDFn
			}
			go cleanup(sandboxID, "cancel requested")
		}
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		sb, err := b.daytona.Get(ctx, sandboxID)
		if err != nil {
			b.log.Warn("cancelled run sandbox lookup failed", "sandbox", sandboxID, "run_id", run.ID, "error", err)
			return
		}
		if run.SessionID != "" {
			b.deleteSandboxSession(sb, run.SessionID)
		}
		if cleanupOnCancel {
			b.cleanupSandbox(ctx, sb, "cancel requested")
		}
	}()
}

func (b *Bot) cleanupSandbox(ctx context.Context, sb *daytona.Sandbox, reason string) {
	if sb == nil {
		return
	}
	if b.cleanupSandboxFn != nil {
		b.cleanupSandboxFn(ctx, sb, reason)
		return
	}
	b.log.Info("sandbox cleanup start", "sandbox", sb.ID, "reason", reason, "state", sb.State)
	b.stopAndArchiveImmediately(ctx, sb, reason)
}

func (b *Bot) cleanupSandboxWithTimeout(sb *daytona.Sandbox, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b.cleanupSandbox(ctx, sb, reason)
}

func (b *Bot) deleteSandboxSession(sb *daytona.Sandbox, sessionID string) {
	if sb == nil || sessionID == "" {
		return
	}
	if b.deleteSandboxSessionFn != nil {
		b.deleteSandboxSessionFn(sb, sessionID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.retryWithBackoff(ctx, "delete session", func() error {
		return sb.Process.DeleteSession(ctx, sessionID)
	}); err != nil {
		b.log.Warn("sandbox session delete failed", "sandbox", sb.ID, "session", sessionID, "error", err)
	}
}

// resumeSandbox starts a sandbox that has been stopped or archived,
// showing a live progress block while waiting. It uses a 5-minute
// timeout — much longer than the SDK default of 60 seconds, which is
// too short for a sandbox warming up from archive.
//
// Transient HTTP/network errors are retried with backoff. DaytonaTimeoutError
// is not retried: the sandbox is alive but slow, so another full 5-minute
// attempt would double the wait without helping.
func (b *Bot) resumeSandbox(ctx context.Context, sb *daytona.Sandbox, emit blocks.Emitter) error {
	const startTimeout = 5 * time.Minute

	setupID := emit.Start(blocks.KindSetup, "Resuming sandbox", nil)
	emit.Append(setupID, "[hetchy] starting sandbox "+sb.ID+"\n")
	started := time.Now()
	b.log.Info("sandbox resume start",
		"sandbox", sb.ID,
		"state", sb.State,
		"auto_archive_minutes", sb.AutoArchiveInterval,
	)

	// Heartbeat goroutine: append elapsed time periodically so the user
	// sees a live indicator rather than a frozen spinner.
	// heartbeatDone is closed when the goroutine exits; we wait on it before
	// any emit.Done/Fail call to avoid a concurrent-write race on the emitter.
	interval := b.heartbeatInterval
	if interval == 0 {
		interval = 15 * time.Second
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat() // belt-and-suspenders: ensures cancel on any future early return
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				emit.Append(setupID, fmt.Sprintf("[hetchy] still waiting… (%v elapsed)\n",
					time.Since(start).Round(time.Second)))
			}
		}
	}()

	startErr := b.retryWithBackoff(ctx, "sandbox start", func() error {
		return b.startFn(ctx, sb, startTimeout)
	})
	cancelHeartbeat()
	<-heartbeatDone

	if startErr != nil {
		b.log.Warn("sandbox resume failed",
			"sandbox", sb.ID,
			"state", sb.State,
			"auto_archive_minutes", sb.AutoArchiveInterval,
			"duration", time.Since(started).Round(time.Millisecond),
			"error", startErr,
		)
		emit.Fail(setupID, "Failed to start")
		return startErr
	}
	b.log.Info("sandbox resumed",
		"sandbox", sb.ID,
		"state", sb.State,
		"auto_archive_minutes", sb.AutoArchiveInterval,
		"duration", time.Since(started).Round(time.Millisecond),
	)
	emit.Done(setupID, "Sandbox ready")
	return nil
}
