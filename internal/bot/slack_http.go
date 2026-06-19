package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

// maxSlackBodyBytes caps the request body for /slack/* endpoints. Slack
// payloads are well under 1 MB; anything larger is hostile and would
// otherwise be read into memory before signature verification.
const maxSlackBodyBytes = 1 << 20

// slackLookupTimeout caps the org/team_id lookup inside the
// post-ack goroutine. The actual dispatched work (HandleRequest, sandbox
// boot, Claude run) can take minutes, so it runs under a fresh
// context.Background — only the lookup is bounded here.
const slackLookupTimeout = 5 * time.Second

// slackEventsHandler implements the public HTTPS endpoint that receives
// events from a distributable Slack app (i.e., the staging and prod
// installs — Socket Mode dev apps don't hit this).
//
// Three responsibilities:
//  1. Verify HMAC-SHA256 signature against SLACK_SIGNING_SECRET.
//  2. Respond to Slack's url_verification challenge on subscribe.
//  3. Look up the org by team_id and route the callback into the same
//     dispatcher used by the Socket Mode loop.
func (b *Bot) slackEventsHandler(w http.ResponseWriter, r *http.Request) {
	body, ok := b.readVerifiedSlackBody(w, r)
	if !ok {
		return
	}

	event, err := slackevents.ParseEvent(body, slackevents.OptionNoVerifyToken())
	if err != nil {
		b.log.Warn("slack events: parse failed", "error", err)
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}

	switch event.Type {
	case slackevents.URLVerification:
		// Slack sends this once when subscribing — echo the challenge
		// back as plain text within 3 seconds or it's rejected.
		var c slackevents.ChallengeResponse
		if err := json.Unmarshal(body, &c); err != nil {
			http.Error(w, "bad challenge", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(c.Challenge))
		return

	case slackevents.CallbackEvent:
		// Ack 200 immediately. Slack enforces a 3s deadline; doing the
		// org lookup + decrypt synchronously would risk timeouts under
		// load and trigger Slack's retry storm. The lookup, dispatch,
		// and any downstream work happen in a detached goroutine.
		w.WriteHeader(http.StatusOK)
		teamID := event.TeamID
		go b.handleSlackCallbackAsync(teamID, event)
		return

	default:
		b.log.Debug("slack events: ignoring type", "type", event.Type)
		w.WriteHeader(http.StatusOK)
	}
}

// handleSlackCallbackAsync runs the post-ack work for an Events API
// callback: org lookup, decrypt bot token, dispatch. Errors here can't
// surface to Slack (we already 200'd) so they're logged.
//
// Two contexts on purpose: a short bounded one for the DB lookup, and
// context.Background for the dispatch path. dispatchCallback spawns a
// goroutine that outlives this function, so reusing a deferred-cancel
// context here would cancel the in-flight handler the moment we
// return. Long-running work (sandbox boot, Claude run) is bounded
// elsewhere; we don't impose an artificial deadline here.
func (b *Bot) handleSlackCallbackAsync(teamID string, event slackevents.EventsAPIEvent) {
	lookupCtx, cancel := context.WithTimeout(context.Background(), slackLookupTimeout)
	defer cancel()
	oc, err := b.orgs.GetBySlackTeamID(lookupCtx, teamID)
	if err != nil {
		if errors.Is(err, orgcfg.ErrNotFound) {
			b.log.Warn("slack events: no org for team_id", "team", teamID)
			return
		}
		b.log.Error("slack events: org lookup failed", "team", teamID, "error", err)
		return
	}
	cli := slack.New(oc.SlackBotToken)
	b.slack.dispatchCallback(context.Background(), oc, event, cli)
}

// slackInteractivityHandler is the request URL Slack POSTs to when a
// user clicks a block-kit button, submits a modal, or runs a slash
// command. Stub for now: verifies the signature, returns 200 — full
// dispatch comes when we wire up the modals/commands UX.
func (b *Bot) slackInteractivityHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := b.readVerifiedSlackBody(w, r); !ok {
		return
	}
	// TODO: parse payload (form-encoded JSON in `payload` field), look
	// up org by team_id, dispatch to a future interactivity handler.
	w.WriteHeader(http.StatusOK)
}

// readVerifiedSlackBody reads the request body, runs the signing-secret
// HMAC over it, and returns the raw bytes if valid. On any failure it
// writes an HTTP error and returns ok=false; callers should return
// immediately. The signing secret comes from process-level config — one
// per Slack app, per environment.
func (b *Bot) readVerifiedSlackBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if b.cfg.SlackSigningSecret == "" {
		// Fail closed: refuse to handle any inbound HTTP Slack traffic
		// when we have no secret to verify it with. The staging/prod
		// envs must set SLACK_SIGNING_SECRET; pure-dev (Socket Mode)
		// installs never reach these endpoints.
		http.Error(w, "slack http transport not configured", http.StatusServiceUnavailable)
		return nil, false
	}
	// Bound the body before any read — protects memory even if the
	// signature is going to fail. MaxBytesReader writes 413 on exceed.
	r.Body = http.MaxBytesReader(w, r.Body, maxSlackBodyBytes)
	verifier, err := slack.NewSecretsVerifier(r.Header, b.cfg.SlackSigningSecret)
	if err != nil {
		http.Error(w, "bad signature header", http.StatusUnauthorized)
		return nil, false
	}
	body, err := io.ReadAll(io.TeeReader(r.Body, &verifier))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return nil, false
	}
	if err := verifier.Ensure(); err != nil {
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}
