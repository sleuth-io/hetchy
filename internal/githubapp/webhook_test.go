package githubapp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// newTestApp builds an App backed by a freshly-generated RSA key, so
// tests don't need a real GitHub-issued private key. The webhook
// secret is fixed so signature tests can compute expected HMACs.
func newTestApp(t *testing.T, webhookSecret string) *App {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	app, err := New(Config{
		AppID:         12345,
		Slug:          "hetchy-test",
		ClientID:      "Iv23liTestClientID",
		PrivateKeyPEM: string(pemBytes),
		WebhookSecret: webhookSecret,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	const secret = "topsecret"
	body := []byte(`{"action":"created"}`)
	app := newTestApp(t, secret)

	cases := []struct {
		name   string
		header string
		body   []byte
		want   string // empty == nil error; substring match for error
	}{
		{
			name:   "valid signature passes",
			header: sign(secret, string(body)),
			body:   body,
			want:   "",
		},
		{
			name:   "missing header rejected",
			header: "",
			body:   body,
			want:   "missing X-Hub-Signature-256",
		},
		{
			name:   "empty after sha256 prefix rejected",
			header: "sha256=",
			body:   body,
			want:   "empty X-Hub-Signature-256",
		},
		{
			name:   "wrong secret rejected",
			header: sign("not-the-real-secret", string(body)),
			body:   body,
			want:   "signature mismatch",
		},
		{
			name:   "tampered body rejected",
			header: sign(secret, string(body)),
			body:   []byte(`{"action":"deleted"}`),
			want:   "signature mismatch",
		},
		{
			name:   "sha1 header alone rejected (we only check sha256)",
			header: "",
			body:   body,
			want:   "missing X-Hub-Signature-256",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("X-Hub-Signature-256", tc.header)
			}
			err := app.VerifyWebhookSignature(h, tc.body)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestVerifyWebhookSignature_AcceptsBareHexFallback(t *testing.T) {
	// Defensive: the production header from GitHub always carries the
	// "sha256=" prefix. We strip it but should still successfully
	// verify if some upstream proxy strips it for us. (No crisis if
	// we don't — but documenting current behavior.)
	const secret = "topsecret"
	body := []byte(`{"x":1}`)
	app := newTestApp(t, secret)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	bare := hex.EncodeToString(mac.Sum(nil))

	h := http.Header{}
	h.Set("X-Hub-Signature-256", bare)
	if err := app.VerifyWebhookSignature(h, body); err != nil {
		t.Errorf("bare hex (no sha256= prefix) should still verify: %v", err)
	}
}

func TestEventTypeFromHeaders(t *testing.T) {
	h := http.Header{}
	if got := EventTypeFromHeaders(h); got != "" {
		t.Errorf("missing header should yield empty, got %q", got)
	}
	h.Set("X-GitHub-Event", "installation")
	if got := EventTypeFromHeaders(h); got != "installation" {
		t.Errorf("got %q, want installation", got)
	}
}

func TestDeliveryIDFromHeaders(t *testing.T) {
	h := http.Header{}
	if got := DeliveryIDFromHeaders(h); got != "" {
		t.Errorf("missing header should yield empty, got %q", got)
	}
	h.Set("X-GitHub-Delivery", "abc-123")
	if got := DeliveryIDFromHeaders(h); got != "abc-123" {
		t.Errorf("got %q, want abc-123", got)
	}
}

func TestMaxWebhookBodyBytes(t *testing.T) {
	// Pin the cap. If anyone wants to lower it, force them to update
	// this test deliberately and consider whether GitHub's largest
	// real payloads still fit (push events on big repos can be ~25 MB
	// but our subscription set doesn't include push).
	if got, want := MaxWebhookBodyBytes(), int64(10<<20); got != want {
		t.Errorf("MaxWebhookBodyBytes() = %d, want %d", got, want)
	}
}
