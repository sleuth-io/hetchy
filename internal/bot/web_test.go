package bot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/webui"
)

var chatScriptAssets = []string{
	"chat_core.js",
	"chat_composer.js",
	"chat_blocks.js",
	"chat_sidebar.js",
	"chat_metadata.js",
	"chat_stream.js",
	"chat_members.js",
	"chat_layout.js",
	"chat_init.js",
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newBypassBot builds a Bot suitable for unit-testing handler logic. Auth
// runs in bypass mode (no WorkOS round-trip), the database/orgcfg/convstore
// fields are nil — handlers that need them must either be tested against a
// real DB or skipped here.
func newBypassBot(t *testing.T) *Bot {
	t.Helper()
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassEmail: "test@hetchy.local",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	return &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a, convs: convstore.New(nil)}
}

func newBypassOrgBot(t *testing.T, role string) *Bot {
	t.Helper()
	if role == "" {
		role = "admin"
	}
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassOrg:   "org_test",
		BypassRole:  role,
		BypassEmail: "test@hetchy.local",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	return &Bot{
		log:   discardLogger(),
		cfg:   Config{WebPort: "0"},
		auth:  a,
		convs: convstore.New(nil),
		live:  newLiveRegistry(),
		followUpModeFn: func(context.Context, orgcfg.Config, convstore.Record, string) followUpModeDecision {
			return followUpModeDecision{Mode: followUpModeChange, Confidence: 1, Reason: "test default"}
		},
	}
}

func readWebUIAsset(t *testing.T, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
	webui.AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset %s status = %d body=%q", name, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func readChatScripts(t *testing.T) string {
	t.Helper()
	parts := make([]string, 0, len(chatScriptAssets))
	for _, name := range chatScriptAssets {
		parts = append(parts, readWebUIAsset(t, name))
	}
	return strings.Join(parts, "\n")
}
