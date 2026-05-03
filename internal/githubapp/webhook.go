package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// maxWebhookBodyBytes caps the request body for /integrations/github/webhook.
// GitHub's largest payloads (push events on big repos) can approach 25 MB
// per docs; we use 10 MB which is enough for installation/team/repo
// events while still rejecting wildly oversized payloads before signature
// verification reads them into memory.
const maxWebhookBodyBytes = 10 << 20

// VerifyWebhookSignature returns nil if header `X-Hub-Signature-256`
// is a valid HMAC-SHA256 of `body` keyed with the App's webhook
// secret. GitHub still emits the older sha1 header alongside this; we
// only check sha256 because the sha1 form is officially deprecated.
func (a *App) VerifyWebhookSignature(header http.Header, body []byte) error {
	got := header.Get("X-Hub-Signature-256")
	if got == "" {
		return errors.New("missing X-Hub-Signature-256")
	}
	got = strings.TrimPrefix(got, "sha256=")
	if got == "" {
		return errors.New("empty X-Hub-Signature-256")
	}
	mac := hmac.New(sha256.New, []byte(a.cfg.WebhookSecret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(got)) {
		return errors.New("signature mismatch")
	}
	return nil
}

// MaxWebhookBodyBytes is exposed so the HTTP handler can wrap
// r.Body with http.MaxBytesReader before reading.
func MaxWebhookBodyBytes() int64 { return maxWebhookBodyBytes }

// EventTypeFromHeaders returns the GitHub event name from the
// X-GitHub-Event header, or "" if absent. Centralised here so handler
// code uses a consistent header name and doesn't accidentally read a
// case-variant.
func EventTypeFromHeaders(h http.Header) string {
	return h.Get("X-GitHub-Event")
}

// DeliveryIDFromHeaders returns the unique X-GitHub-Delivery id
// useful for log correlation when debugging webhook traffic.
func DeliveryIDFromHeaders(h http.Header) string {
	return h.Get("X-GitHub-Delivery")
}
