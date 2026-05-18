package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hetchyhq/hetchy/internal/webui"
)

func (b *Bot) runWeb(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/assets/", webui.AssetHandler())

	mux.HandleFunc("/login", b.auth.LoginHandler)
	mux.HandleFunc("/signup", b.auth.SignupHandler)
	mux.HandleFunc("/callback", b.auth.CallbackHandler)
	mux.HandleFunc("/logout", b.auth.LogoutHandler)
	mux.HandleFunc("/stripe/webhook", b.stripeWebhookHandler)

	// Slack HTTP transport — public endpoints that Slack POSTs to. No
	// WorkOS auth middleware: these are verified instead by HMAC over
	// the SLACK_SIGNING_SECRET inside the handlers.
	mux.HandleFunc("/slack/events", b.slackEventsHandler)
	mux.HandleFunc("/slack/interactivity", b.slackInteractivityHandler)
	mux.HandleFunc("/slack/oauth/callback", b.slackOAuthCallbackHandler)
	// /slack/install initiates the OAuth flow. Auth-gated so we know
	// which org this install should be bound to (state carries that).
	mux.Handle("/slack/install", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.slackInstallHandler))))
	mux.Handle("/slack/disconnect", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.slackDisconnectHandler))))

	// GitHub App transport. The webhook endpoint is unauthenticated —
	// it's verified by HMAC inside the handler. The setup callback is
	// also unauthenticated (state token does the binding). The install
	// kick-off is auth-gated so we know which org the install belongs to.
	mux.HandleFunc("/integrations/github/webhook", b.githubWebhookHandler)
	mux.HandleFunc("/integrations/github/setup", b.githubSetupHandler)
	mux.Handle("/integrations/github/install", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubInstallHandler))))
	mux.Handle("/integrations/github/sync", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubSyncHandler))))
	mux.Handle("/integrations/github/disconnect", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubDisconnectHandler))))

	// Sandbox artifact uploads use bearer tokens minted per agent run,
	// not WorkOS cookies. The handler validates the token before issuing
	// any S3 presigned URLs.
	mux.HandleFunc(artifactSlotPath, b.artifactSlotsHandler)

	mux.Handle("/", b.auth.Middleware(http.HandlerFunc(b.indexHandler)))
	mux.Handle("/onboarding", b.auth.Middleware(b.auth.RequireAuth(http.HandlerFunc(b.onboardingHandler))))
	mux.Handle("/welcome", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.welcomeHandler))))
	mux.Handle("/settings/org", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler))))
	mux.Handle("/settings/org/delete", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.orgDeleteHandler))))
	mux.Handle("/settings/org/agents/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentSettingsActionHandler))))
	mux.Handle("/settings/org/repositories/flavor", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoFlavorSettingsHandler))))
	mux.Handle("/settings/org/billing/topup-settings", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.billingTopupSettingsHandler))))
	mux.Handle("/billing/checkout", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.billingCheckoutHandler))))
	mux.Handle("/billing/topup", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.billingTopupHandler))))
	mux.Handle("/billing/portal", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.billingPortalHandler))))
	mux.Handle("/settings/org/invite", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.inviteHandler))))
	mux.Handle("/settings/org/invitations/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.invitationActionHandler))))
	mux.Handle("/settings/org/members/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.memberActionHandler))))
	mux.Handle("/settings/profile", b.auth.Middleware(b.auth.RequireAuth(http.HandlerFunc(b.profileHandler))))
	mux.Handle("/settings/profile/password-reset", b.auth.Middleware(b.auth.RequireAuth(http.HandlerFunc(b.passwordResetHandler))))
	mux.Handle("/chat", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.chatHandler(ctx, w, r)
	}))))
	mux.Handle("/chat/cancel", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.chatCancelHandler))))
	mux.Handle("/chat/stream", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.chatStreamHandler))))
	mux.Handle("/api/repo-secrets", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoSecretsHandler))))
	mux.Handle("/api/repo-bootstrap", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoBootstrapResetHandler))))
	mux.Handle("/api/conversations", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationsHandler))))
	mux.Handle("/api/conversations/download/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationDownloadHandler))))
	mux.Handle("/api/conversations/attachments/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationAttachmentDownloadHandler))))
	mux.Handle("/api/conversations/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationDetailHandler))))
	mux.Handle("/api/agents", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentsHandler))))
	mux.Handle("/api/repositories", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repositoriesHandler))))
	mux.Handle("/api/members", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.membersHandler))))

	addr := ":" + b.cfg.WebPort
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// Log both the bind address (where the kernel will accept
	// connections) and the public URL the user should hit in a
	// browser (which differs in dev when /etc/hosts maps a real-
	// looking hostname to localhost).
	b.log.Info("web ui listening", "addr", addr, "public_url", b.cfg.PublicBaseURL())
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web server: %w", err)
	}
	return nil
}
