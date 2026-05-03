package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/githubapp"
)

// githubWebhookHandler is the public endpoint GitHub posts events to.
// Verifies the HMAC, parses the event type, and dispatches to a
// specific handler. Always 200s after dispatch (errors are logged
// only) so GitHub doesn't enter its retry loop on transient bot
// problems.
func (b *Bot) githubWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if b.app == nil {
		http.Error(w, "github app not configured", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, githubapp.MaxWebhookBodyBytes())
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	if err := b.app.VerifyWebhookSignature(r.Header, body); err != nil {
		b.log.Warn("github webhook: signature verify failed",
			"error", err,
			"event", githubapp.EventTypeFromHeaders(r.Header),
			"delivery", githubapp.DeliveryIDFromHeaders(r.Header),
		)
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return
	}

	event := githubapp.EventTypeFromHeaders(r.Header)
	delivery := githubapp.DeliveryIDFromHeaders(r.Header)
	b.log.Info("github webhook received", "event", event, "delivery", delivery, "bytes", len(body))

	w.WriteHeader(http.StatusOK)
	// Detached context: dispatch can outlive the request lifetime
	// (sync calls hit GitHub APIs and may take seconds).
	go b.dispatchGithubEvent(context.Background(), event, body)
}

// dispatchGithubEvent fans an event payload out to the right handler.
// Unknown event types are silently ignored — we subscribe to a small
// set on the App side, but GitHub may deliver a few extras (like
// ping) that we don't care about.
func (b *Bot) dispatchGithubEvent(ctx context.Context, event string, body []byte) {
	switch event {
	case "ping":
		// Sent once when GitHub first verifies the webhook URL.
		return
	case "installation":
		b.handleInstallationEvent(ctx, body)
	case "installation_repositories":
		b.handleInstallationReposEvent(ctx, body)
	case "team", "team_add", "membership", "member", "organization":
		b.handleOrgScopedEvent(ctx, event, body)
	default:
		b.log.Debug("github webhook: ignoring event", "event", event)
	}
}

// handleInstallationEvent reacts to install lifecycle changes:
// `created`/`unsuspend` → upsert, sync. `deleted`/`suspend` → mark
// suspended (or remove). The action determines which.
func (b *Bot) handleInstallationEvent(ctx context.Context, body []byte) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
				Type  string `json:"type"`
				ID    int64  `json:"id"`
			} `json:"account"`
			SuspendedAt *time.Time `json:"suspended_at"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		b.log.Error("github webhook: parse installation event", "error", err)
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	switch p.Action {
	case "deleted":
		// Cascade drops repos/teams/members via FK. The setup-callback
		// row is removed; if the user reinstalls a fresh row appears.
		if err := b.store.Queries.DeleteGithubInstallation(ctx, p.Installation.ID); err != nil {
			b.log.Error("github webhook: delete installation", "id", p.Installation.ID, "error", err)
		}
		b.app.InvalidateInstallation(p.Installation.ID)
		return
	case "suspend", "unsuspend", "created", "new_permissions_accepted":
		row, err := b.store.Queries.GetGithubInstallation(ctx, p.Installation.ID)
		if err != nil {
			// `created` arrives before the setup callback in some
			// flows (e.g. install without redirect); skip silently —
			// the setup callback writes the row.
			b.log.Debug("github webhook: installation not yet recorded", "id", p.Installation.ID)
			return
		}
		var suspended pgtype.Timestamptz
		if p.Installation.SuspendedAt != nil {
			suspended = pgtype.Timestamptz{Time: *p.Installation.SuspendedAt, Valid: true}
		}
		if _, err := b.store.Queries.UpsertGithubInstallation(ctx, sqlc.UpsertGithubInstallationParams{
			InstallationID: row.InstallationID,
			OrgID:          row.OrgID,
			AccountLogin:   p.Installation.Account.Login,
			AccountType:    p.Installation.Account.Type,
			AccountID:      p.Installation.Account.ID,
			SuspendedAt:    suspended,
		}); err != nil {
			b.log.Error("github webhook: upsert installation on event", "id", p.Installation.ID, "error", err)
			return
		}
		b.app.InvalidateInstallation(p.Installation.ID)
		// Sync to pick up any repo/team changes that came with the event.
		if _, err := b.app.SyncInstallation(ctx, b.store, p.Installation.ID); err != nil {
			b.log.Error("github webhook: sync after installation event", "id", p.Installation.ID, "error", err)
		}
	}
}

// handleInstallationReposEvent fires when the user adds or removes
// repos from the installation via GitHub's UI. Just re-sync — the
// payload contains the list but a fresh sync is simpler and idempotent.
func (b *Bot) handleInstallationReposEvent(ctx context.Context, body []byte) {
	var p struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		b.log.Error("github webhook: parse installation_repositories", "error", err)
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	b.app.InvalidateInstallation(p.Installation.ID)
	if _, err := b.app.SyncInstallation(ctx, b.store, p.Installation.ID); err != nil {
		b.log.Error("github webhook: sync after install_repos event", "id", p.Installation.ID, "error", err)
	}
}

// handleOrgScopedEvent re-syncs every installation owned by the
// affected org. We don't bother diffing the payload: team/member
// events fire a few times per change at most, and a full org sync
// with a tiny number of teams is fast.
func (b *Bot) handleOrgScopedEvent(ctx context.Context, event string, body []byte) {
	var p struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		b.log.Error("github webhook: parse org event", "event", event, "error", err)
		return
	}
	if p.Installation.ID != 0 {
		if _, err := b.app.SyncInstallation(ctx, b.store, p.Installation.ID); err != nil {
			b.log.Error("github webhook: sync after org event", "event", event, "id", p.Installation.ID, "error", err)
		}
	}
}
