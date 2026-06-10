package bot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func signedLinearRequest(body, secret string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/integrations/linear/webhook", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	req.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	return req
}

func linearSessionEventBody(action string) string {
	return fmt.Sprintf(`{
		"type":"AgentSessionEvent","action":%q,"organizationId":"ws-1",
		"webhookTimestamp":%d,
		"agentSession":{"id":"sess-1","issue":{"id":"iss-1","identifier":"ENG-1","title":"Fix it","url":"https://linear.app/acme/issue/ENG-1"}},
		"promptContext":"Fix the login bug"
	}`, action, time.Now().UnixMilli())
}

func newLinearWebhookTestBot(secret string) *Bot {
	return &Bot{
		log:              discardLogger(),
		cfg:              Config{LinearWebhookSecret: secret},
		orgs:             &fakeOrgStore{},
		linearWebhookSem: make(chan struct{}, 1),
	}
}

func TestLinearWebhookRequiresSecret(t *testing.T) {
	b := newLinearWebhookTestBot("")
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, httptest.NewRequest(http.MethodPost, "/integrations/linear/webhook", strings.NewReader(`{}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestLinearWebhookRejectsBadSignature(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, signedLinearRequest(linearSessionEventBody("created"), "wrong-secret"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestLinearWebhookRejectsStaleTimestamp(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	body := fmt.Sprintf(`{
		"type":"AgentSessionEvent","action":"created","organizationId":"ws-1",
		"webhookTimestamp":%d,
		"agentSession":{"id":"sess-1"}
	}`, time.Now().Add(-time.Hour).UnixMilli())
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, signedLinearRequest(body, "whsec"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLinearWebhookIgnoresOtherTypes(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	body := fmt.Sprintf(`{"type":"Issue","action":"create","organizationId":"ws-1","webhookTimestamp":%d}`, time.Now().UnixMilli())
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, signedLinearRequest(body, "whsec"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestLinearWebhookIgnoresUnknownActions(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, signedLinearRequest(linearSessionEventBody("deleted"), "whsec"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestLinearWebhookRejectsMissingSessionID(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	body := fmt.Sprintf(`{"type":"AgentSessionEvent","action":"created","organizationId":"ws-1","webhookTimestamp":%d,"agentSession":{"id":""}}`, time.Now().UnixMilli())
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, signedLinearRequest(body, "whsec"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLinearWebhookRejectsBadJSON(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, signedLinearRequest(`not-json`, "whsec"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLinearWebhookAcksValidEvent(t *testing.T) {
	// The fake org store returns ErrNotFound-free zero config (empty
	// token), so the async dispatch logs and bails — this test pins
	// the HTTP contract: a valid signed event acks 200 immediately.
	b := newLinearWebhookTestBot("whsec")
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, signedLinearRequest(linearSessionEventBody("created"), "whsec"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Drain the dispatch goroutine so it can't race test teardown.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case b.linearWebhookSem <- struct{}{}:
			<-b.linearWebhookSem
			return
		case <-deadline:
			t.Fatal("dispatch goroutine did not release its slot")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestLinearWebhookDedupRejectsReplay(t *testing.T) {
	var d linearWebhookDedup
	now := time.Now()
	ttl := 10 * time.Minute

	if !d.firstDelivery("wh-1", now, ttl) {
		t.Fatal("first delivery rejected")
	}
	if d.firstDelivery("wh-1", now.Add(time.Minute), ttl) {
		t.Fatal("replay within ttl accepted")
	}
	if !d.firstDelivery("wh-2", now, ttl) {
		t.Fatal("distinct id rejected")
	}
	// Past the ttl the id is forgotten (Linear wouldn't redeliver a
	// fresh-timestamped event that late anyway).
	if !d.firstDelivery("wh-1", now.Add(ttl+time.Minute), ttl) {
		t.Fatal("expired id still rejected")
	}
	// Empty ids can't be deduplicated and always pass.
	for range 2 {
		if !d.firstDelivery("", now, ttl) {
			t.Fatal("empty id was deduplicated")
		}
	}
}

func TestLinearWebhookDuplicateDeliveryAcks200(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	body := fmt.Sprintf(`{
		"type":"AgentSessionEvent","action":"created","organizationId":"ws-1",
		"webhookId":"wh-dup-1","webhookTimestamp":%d,
		"agentSession":{"id":"sess-1"},
		"promptContext":"Fix it"
	}`, time.Now().UnixMilli())

	first := httptest.NewRecorder()
	b.linearWebhookHandler(first, signedLinearRequest(body, "whsec"))
	if first.Code != http.StatusOK {
		t.Fatalf("first delivery status = %d", first.Code)
	}
	second := httptest.NewRecorder()
	b.linearWebhookHandler(second, signedLinearRequest(body, "whsec"))
	if second.Code != http.StatusOK {
		t.Fatalf("duplicate delivery status = %d, want 200 (acked, not dispatched)", second.Code)
	}
}

func TestLinearWebhookSaturatedDispatcherReturns503(t *testing.T) {
	b := newLinearWebhookTestBot("whsec")
	// Occupy the only dispatch slot so enqueue times out.
	b.linearWebhookSem <- struct{}{}
	defer func() { <-b.linearWebhookSem }()

	req := signedLinearRequest(linearSessionEventBody("created"), "whsec")
	ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	b.linearWebhookHandler(rec, req.WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
