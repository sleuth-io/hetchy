package bot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signedSlackRequest(method, path, body, secret string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

func TestSlackEventsHandlerURLVerification(t *testing.T) {
	const secret = "slack-secret"
	body := `{"token":"ignored","challenge":"challenge-123","type":"url_verification"}`
	b := &Bot{log: discardLogger(), cfg: Config{SlackSigningSecret: secret}}
	rec := httptest.NewRecorder()

	b.slackEventsHandler(rec, signedSlackRequest(http.MethodPost, "/slack/events", body, secret))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "challenge-123" {
		t.Fatalf("body = %q, want challenge", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", got)
	}
}

func TestSlackEventsHandlerRejectsBadSignature(t *testing.T) {
	b := &Bot{log: discardLogger(), cfg: Config{SlackSigningSecret: "slack-secret"}}
	rec := httptest.NewRecorder()
	req := signedSlackRequest(http.MethodPost, "/slack/events", `{"type":"url_verification"}`, "wrong-secret")

	b.slackEventsHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestSlackEventsHandlerRequiresSigningSecret(t *testing.T) {
	b := &Bot{log: discardLogger()}
	rec := httptest.NewRecorder()

	b.slackEventsHandler(rec, httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(`{}`)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestSlackInteractivityHandlerVerifiesAndAcks(t *testing.T) {
	const secret = "slack-secret"
	b := &Bot{log: discardLogger(), cfg: Config{SlackSigningSecret: secret}}
	rec := httptest.NewRecorder()

	b.slackInteractivityHandler(rec, signedSlackRequest(http.MethodPost, "/slack/interactivity", `payload={}`, secret))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
}
