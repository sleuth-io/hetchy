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
	"net/http"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
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

const workdir = "/home/daytona/work"

// Bot wires the web UI, Slack manager, Daytona, and per-org config
// together. It owns no per-request mutable state; conversation state lives
// in the database.
type Bot struct {
	cfg       Config
	log       *slog.Logger
	daytona   *daytona.Client
	store     *db.Store
	orgs      *orgcfg.Store
	convs     *convstore.Store
	auth      *auth.Service
	slack     *slackManager
	bootstrap *bootstrap.Store
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
	createFn     func(context.Context, any) (*daytona.Sandbox, error)
	retryBackoff time.Duration
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
	if cfg.DaytonaAPIURL != "" {
		log.Info("daytona configured", "mode", "local", "url", cfg.DaytonaAPIURL)
	} else {
		log.Info("daytona configured", "mode", "cloud", "url", "app.daytona.io")
	}

	store, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("database open: %w", err)
	}
	log.Info("database connected")

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
		LogoutReturnTo: cfg.LogoutReturnTo,
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

	b := &Bot{
		cfg:              cfg,
		log:              log,
		daytona:          dc,
		store:            store,
		orgs:             orgcfg.New(store, cipher),
		convs:            convstore.New(store),
		bootstrap:        bootstrap.New(store, cipher),
		auth:             authSvc,
		cipher:           cipher,
		retryBackoff:     initialBackoff,
		githubWebhookSem: make(chan struct{}, webhookDispatchConcurrency),
	}
	b.createFn = func(ctx context.Context, params any) (*daytona.Sandbox, error) {
		return dc.Create(ctx, params)
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
	go func() { errCh <- b.runWeb(ctx) }()
	go func() { errCh <- b.slack.Run(ctx) }()

	err := <-errCh
	cancel()
	<-errCh
	return err
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
func (b *Bot) HandleRequest(ctx context.Context, oc orgcfg.Config, text, requestID, threadID, userID string, out blocks.Emitter) {
	b.log.Info("request received",
		"org", oc.OrgID,
		"request_id", requestID,
		"thread_id", threadID,
		"text_len", len(text),
		"text_preview", truncate(text, 200),
	)

	// Wrap the transport emitter with a Recorder so every block streamed
	// to the user is also captured for persistence. A reopened chat
	// replays the same block tree the user originally saw.
	recorder := blocks.NewRecorder(maxBlocksPerTurn)
	emit := blocks.Tee(recorder, out)

	if oc.AnthropicAPIKey == "" && oc.ClaudeCodeOAuthToken == "" {
		b.log.Warn("org missing claude credentials", "org", oc.OrgID)
		emit.Error("Missing Claude credentials", "This organization has neither a Claude API key nor a subscription token set. Add one at /settings/org → Integrations → Claude (Anthropic).")
		return
	}

	rec, err := b.convs.Get(ctx, oc.OrgID, threadID)
	switch {
	case err == nil && rec.SandboxID != "" && rec.PRURL != "":
		// Live conversation — agent succeeded at least once, PR exists.
		b.handleFollowUp(ctx, oc, rec, text, requestID, recorder, emit)
		return
	case err == nil && rec.SandboxID != "":
		// Sandbox was created but the agent failed before producing a
		// PR. Retry: archive the orphan sandbox + spawn a fresh one.
		b.handleRetryAfterFailure(ctx, oc, rec, text, requestID, recorder, emit)
		return
	case err == nil:
		// No sandbox was ever created. Two sub-states distinguished by
		// GitHubOwner:
		//   1. GitHubOwner == "" → we asked for a repo and the user is
		//      answering. handleAwaitingRepoReply parses owner/name.
		//   2. GitHubOwner != "" → resolveRepo+sandbox-create failed.
		//      The repo isn't the problem; treat the new message as
		//      the new request and re-run on the same repo.
		if rec.GitHubOwner != "" && rec.GitHubRepo != "" {
			b.handleRetryAfterFailure(ctx, oc, rec, text, requestID, recorder, emit)
			return
		}
		b.handleAwaitingRepoReply(ctx, oc, rec, text, requestID, recorder, emit)
		return
	case errors.Is(err, convstore.ErrNotFound):
		// fall through — new conversation
	default:
		b.log.Error("convstore get", "error", err)
		emit.Error("Conversation lookup failed", fmt.Sprintf("`%v`", err))
		return
	}

	// New conversation. Use the org's default repo if set; otherwise
	// stash the request and ask the user which repo to use.
	if oc.DefaultGitHubOwner == "" || oc.DefaultGitHubRepo == "" {
		emit.Notify("Which repository?", "Reply with `owner/name`.\n(You can save a default at /settings/org → Integrations.)")
		partial := convstore.Record{
			OrgID:          oc.OrgID,
			ThreadID:       threadID,
			History:        []string{text},
			ResponseBlocks: [][]blocks.Block{recorder.Snapshot()},
			CreatorID:      userID,
		}
		if err := b.convs.Upsert(ctx, partial); err != nil {
			b.log.Error("convstore upsert (awaiting repo)", "error", err, "org", oc.OrgID, "thread", threadID)
		}
		return
	}

	rec = convstore.Record{
		OrgID:       oc.OrgID,
		ThreadID:    threadID,
		History:     []string{text},
		GitHubOwner: oc.DefaultGitHubOwner,
		GitHubRepo:  oc.DefaultGitHubRepo,
		CreatorID:   userID,
	}
	b.runFreshAgent(ctx, oc, rec, text, requestID, recorder, emit)
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
func (b *Bot) handleAwaitingRepoReply(ctx context.Context, oc orgcfg.Config, rec convstore.Record, text, requestID string, recorder *blocks.Recorder, emit blocks.Emitter) {
	owner, name, ok := parseOwnerRepo(text)
	if !ok {
		emit.Notify("Try again", "I couldn't parse that as `owner/name`. For example `acme/website`.")
		// Persist the bot's nudge so a UI replay shows it; keep the
		// row otherwise unchanged.
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (parse retry)", "error", err)
		}
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
	originalRequest := rec.History[0]
	b.runFreshAgent(ctx, oc, rec, originalRequest, requestID, recorder, emit)
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
// History[0] (this is still the first real turn — the row exists only
// because we persisted the failure blocks for refresh visibility) so a
// follow-up only sees the request that actually shipped.
//
// If the failed attempt left an orphan sandbox (rec.SandboxID set,
// rec.PRURL empty), archive it best-effort before spawning a fresh
// one. The user retrying is the signal that they're done debugging
// the previous failure; without this we'd leak a Daytona sandbox per
// retry.
func (b *Bot) handleRetryAfterFailure(ctx context.Context, oc orgcfg.Config, rec convstore.Record, text, requestID string, recorder *blocks.Recorder, emit blocks.Emitter) {
	if rec.SandboxID != "" {
		if sb, err := b.daytona.Get(ctx, rec.SandboxID); err == nil {
			if err := sb.Stop(ctx); err != nil {
				b.log.Warn("orphan sandbox stop failed", "sandbox", rec.SandboxID, "error", err)
			} else if err := sb.Archive(ctx); err != nil {
				b.log.Warn("orphan sandbox archive failed", "sandbox", rec.SandboxID, "error", err)
			}
		} else {
			b.log.Warn("orphan sandbox lookup failed; assuming already gone", "sandbox", rec.SandboxID, "error", err)
		}
		rec.SandboxID = ""
	}
	rec.History = []string{text}
	rec.ResponseBlocks = nil
	b.runFreshAgent(ctx, oc, rec, text, requestID, recorder, emit)
}

// runFreshAgent creates a new sandbox, mints an installation token
// scoped to rec's repo, runs the agent on userRequest, and persists
// the resulting conversation. Shared by the new-conversation, awaiting-
// repo-reply, and "had repo but no sandbox" paths so they all stamp
// the row identically.
func (b *Bot) runFreshAgent(ctx context.Context, oc orgcfg.Config, rec convstore.Record, userRequest, requestID string, recorder *blocks.Recorder, emit blocks.Emitter) {
	repo, err := b.resolveRepo(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
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
		return
	}

	emit.Notify("Starting", fmt.Sprintf("Spinning up an isolated sandbox for your request in `%s` (base: `%s`)…", repo.Slug, repo.BaseBranch))

	envVars := map[string]string{}
	if oc.SXKey != "" {
		envVars["SX_KEY"] = oc.SXKey
	}
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{EnvVars: envVars},
		Snapshot:          b.cfg.Snapshot,
	})
	if err != nil {
		if ctx.Err() != nil {
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
		if uerr := b.convs.Upsert(ctx, rec); uerr != nil {
			b.log.Error("convstore upsert (sandbox create fail)", "error", uerr)
		}
		return
	}
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID)
	emit.Notify("Sandbox ready", fmt.Sprintf("`%s` is up — cloning repo and starting Claude Code.", sb.ID))

	branch := "feature/sf-" + requestID
	prURL, runErr := b.runAgent(ctx, sb, repo, oc, userRequest, requestID, emit)
	if runErr != nil {
		b.log.Error("agent run failed", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
		emit.Error("Agent failed", fmt.Sprintf("Something went wrong while running the agent. Sandbox `%s` is left running for debugging — reply here to retry (the orphan sandbox will be archived automatically) or check the server logs for details.", sb.ID))
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
		return
	}

	if err := sb.Stop(ctx); err != nil {
		b.log.Error("sandbox stop failed", "sandbox", sb.ID, "error", err)
	} else if err := sb.Archive(ctx); err != nil {
		b.log.Error("sandbox archive failed", "sandbox", sb.ID, "error", err)
	}

	emit.Result("Done!", prURL+"\n\nReply here to make further changes to this PR.")

	rec.SandboxID = sb.ID
	rec.Branch = branch
	rec.PRURL = prURL
	appendBlocksToFirstTurn(&rec, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
	}
}

func (b *Bot) handleFollowUp(ctx context.Context, oc orgcfg.Config, rec convstore.Record, text, requestID string, recorder *blocks.Recorder, emit blocks.Emitter) {
	b.log.Info("follow-up received", "org", oc.OrgID, "sandbox", rec.SandboxID, "branch", rec.Branch, "pr", rec.PRURL)
	emit.Notify("Resuming", fmt.Sprintf("Resuming work on %s…", rec.PRURL))

	repo, err := b.resolveRepo(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		b.log.Warn("resolve repo for follow-up failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo access lost", fmt.Sprintf("Lost access to `%s/%s` — check the GitHub App install at /settings/org → Integrations.", rec.GitHubOwner, rec.GitHubRepo))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up resolve fail)", "error", err)
		}
		return
	}

	sb, err := b.daytona.Get(ctx, rec.SandboxID)
	if err != nil {
		b.log.Error("sandbox get failed", "sandbox", rec.SandboxID, "request_id", requestID, "error", err)
		emit.Error("Sandbox missing", fmt.Sprintf("Could not find sandbox `%s` — it may have been archived or removed. Start a new chat to continue.", rec.SandboxID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up sandbox missing)", "error", err)
		}
		return
	}
	if err := sb.Start(ctx); err != nil {
		b.log.Error("sandbox start failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Sandbox start failed", fmt.Sprintf("Failed to resume sandbox `%s`. Check the server logs for details.", sb.ID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up sandbox start)", "error", err)
		}
		return
	}
	if err := sb.WaitForStart(ctx, 2*time.Minute); err != nil {
		b.log.Error("sandbox wait-for-start failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Sandbox slow to start", fmt.Sprintf("Sandbox `%s` did not start in time. Try again, or open a fresh chat.", sb.ID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up sandbox wait)", "error", err)
		}
		return
	}

	prURL, err := b.runFollowUp(ctx, sb, repo, oc, rec, text, requestID, emit)
	if err != nil {
		b.log.Error("follow-up failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Agent failed", fmt.Sprintf("Something went wrong while running the agent. Sandbox `%s` is left running for debugging — check the server logs for details.", sb.ID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up agent fail)", "error", err)
		}
		return
	}

	if err := sb.Stop(ctx); err != nil {
		b.log.Error("sandbox stop failed", "sandbox", sb.ID, "error", err)
	} else if err := sb.Archive(ctx); err != nil {
		b.log.Error("sandbox archive failed", "sandbox", sb.ID, "error", err)
	}

	// Result first so the recorded snapshot includes the closing block,
	// then upsert with the new user turn + this turn's blocks.
	emit.Result("Done!", prURL)

	rec.PRURL = prURL
	appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
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

// isTransientError reports whether err is a retryable Daytona API error:
// rate-limit (429), server-side 5xx responses, and network-level failures
// (StatusCode == 0) are all considered transient.
func isTransientError(err error) bool {
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
