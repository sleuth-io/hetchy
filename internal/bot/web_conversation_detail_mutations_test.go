package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

// detailMutationHandler wires the conversation resource handler through the
// auth middleware exactly like the real router, so DELETE/PATCH requests carry
// a resolved principal (org_test/admin) the way production traffic does.
func detailMutationHandler(b *Bot) http.Handler {
	return b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.conversationResourceHandler(context.Background(), w, r)
	})))
}

// sameOrigin marks a request as first-party so requireSameOrigin passes.
// httptest.NewRequest defaults Host to example.com.
func sameOrigin(r *http.Request) *http.Request {
	r.Header.Set("Origin", "http://example.com")
	return r
}

func TestServeConversationDetailDelete(t *testing.T) {
	activeRun := runstore.Run{ID: "run-live", OrgID: "org_test", ThreadID: "thread-1", State: "running"}

	const inFlightMsg = "this chat has a turn in flight"

	cases := []struct {
		name       string
		setup      func(b *Bot)
		skipOrigin bool
		wantStatus int
		wantBody   string
	}{
		{
			name:       "missing same origin is rejected",
			setup:      func(b *Bot) { b.convs = &fakeConversationStore{} },
			skipOrigin: true,
			wantStatus: http.StatusForbidden,
		},
		{
			// b.runs stays nil, so a 409 here can only come from the
			// live-registry guard, not the durable-run guard.
			name: "live in-flight turn blocks delete",
			setup: func(b *Bot) {
				b.convs = &fakeConversationStore{}
				b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-1")
			},
			wantStatus: http.StatusConflict,
			wantBody:   inFlightMsg,
		},
		{
			// The live registry stays empty, so a 409 here can only come
			// from the durable-run guard, not the live-registry guard.
			name: "active durable run blocks delete",
			setup: func(b *Bot) {
				b.convs = &fakeConversationStore{}
				b.runs = &fakeRunStore{enabled: true, activeRun: activeRun}
			},
			wantStatus: http.StatusConflict,
			wantBody:   inFlightMsg,
		},
		{
			name: "durable run lookup error surfaces 500",
			setup: func(b *Bot) {
				b.convs = &fakeConversationStore{}
				b.runs = &fakeRunStore{enabled: true, activeErr: errors.New("db down")}
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "delete store error surfaces 500",
			setup: func(b *Bot) {
				b.convs = &fakeConversationStore{deleteErr: errors.New("delete failed")}
				b.runs = &fakeRunStore{enabled: true, activeErr: pgx.ErrNoRows}
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "no active run deletes successfully",
			setup: func(b *Bot) {
				b.convs = &fakeConversationStore{}
				b.runs = &fakeRunStore{enabled: true, activeErr: pgx.ErrNoRows}
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "delete succeeds when durable runs are disabled",
			setup:      func(b *Bot) { b.convs = &fakeConversationStore{} },
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBypassOrgBot(t, "admin")
			tc.setup(b)
			handler := detailMutationHandler(b)

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/conversations/thread-1", nil)
			if !tc.skipOrigin {
				req = sameOrigin(req)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestServeConversationDetailPatch(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		renameErr  error
		wantStatus int
	}{
		{name: "invalid json", body: `{"title":`, wantStatus: http.StatusBadRequest},
		{name: "empty title", body: `{"title":"   "}`, wantStatus: http.StatusBadRequest},
		{name: "title too long", body: `{"title":"` + strings.Repeat("x", 201) + `"}`, wantStatus: http.StatusBadRequest},
		{name: "rename not found", body: `{"title":"Renamed"}`, renameErr: convstore.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "rename store error", body: `{"title":"Renamed"}`, renameErr: errors.New("boom"), wantStatus: http.StatusInternalServerError},
		{name: "rename succeeds", body: `{"title":"Renamed"}`, wantStatus: http.StatusNoContent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBypassOrgBot(t, "admin")
			b.convs = &fakeConversationStore{renameErr: tc.renameErr}
			handler := detailMutationHandler(b)

			req := sameOrigin(httptest.NewRequest(http.MethodPatch, "/api/v1/conversations/thread-1", strings.NewReader(tc.body)))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestServeConversationDetailPatchTitleCapCountsRunes proves the 200 rune cap
// counts runes, not bytes: a 200-rune multibyte title is accepted while a
// 201-rune one is rejected.
func TestServeConversationDetailPatchTitleCapCountsRunes(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.convs = &fakeConversationStore{}
	handler := detailMutationHandler(b)

	okReq := sameOrigin(httptest.NewRequest(http.MethodPatch, "/api/v1/conversations/thread-1",
		strings.NewReader(`{"title":"`+strings.Repeat("界", 200)+`"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, okReq)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("200-rune title status = %d, want %d body=%q", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	tooLong := sameOrigin(httptest.NewRequest(http.MethodPatch, "/api/v1/conversations/thread-1",
		strings.NewReader(`{"title":"`+strings.Repeat("界", 201)+`"}`)))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, tooLong)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("201-rune title status = %d, want %d body=%q", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
