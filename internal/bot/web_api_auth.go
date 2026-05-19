package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hetchyhq/hetchy/internal/auth"
)

func (b *Bot) apiAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := apiBearerToken(r); token != "" {
			if b.apiKeys == nil {
				writeAPIAuthError(w, http.StatusUnauthorized, "api key authentication is not configured")
				return
			}
			key, ok, err := b.apiKeys.Authenticate(r.Context(), token)
			if err != nil {
				b.log.Warn("api key auth failed", "error", err)
				writeAPIAuthError(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			if !ok {
				writeAPIAuthError(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			go func() {
				if err := b.apiKeys.Touch(context.Background(), key.ID); err != nil {
					b.log.Warn("touch api key", "org", key.OrgID, "key", key.ID, "error", err)
				}
			}()
			sessionID := "api_key:" + key.ID
			p := auth.Principal{
				UserID:    sessionID,
				OrgID:     key.OrgID,
				Role:      "member",
				SessionID: sessionID,
				IsAPIKey:  true,
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
			return
		}

		b.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := auth.FromContext(r.Context())
			if !ok {
				writeAPIAuthError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if !p.HasOrg() {
				writeAPIAuthError(w, http.StatusForbidden, "organization required")
				return
			}
			next.ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})
}

func apiBearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return ""
	}
	scheme, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func isAPIKeyRequest(ctx context.Context) bool {
	p, ok := auth.FromContext(ctx)
	return ok && p.IsAPIKey
}

func requireSameOriginUnlessAPIKey(r *http.Request) error {
	if isAPIKeyRequest(r.Context()) {
		return nil
	}
	return requireSameOrigin(r)
}

func writeAPIAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
