package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// openaiAPIBaseRef is an atomic pointer to the OpenAI base URL so
// withOpenAIBase can swap it from test helpers without racing against
// concurrent settings POSTs. The pointer is initialized at package
// load and only ever overwritten (never partially written), which
// keeps validateOpenAICredential read-side free of locks while still
// being safe under `go test -race`.
var openaiAPIBaseRef atomic.Pointer[string]

func init() {
	prod := "https://api.openai.com"
	openaiAPIBaseRef.Store(&prod)
}

func openaiAPIBase() string {
	if p := openaiAPIBaseRef.Load(); p != nil {
		return *p
	}
	return "https://api.openai.com"
}

const openaiValidateTimeout = 8 * time.Second

type openaiCredKind int

const (
	openaiCredAPIKey openaiCredKind = iota
	openaiCredOAuthToken
)

var errOpenAIInvalidCredential = errors.New("openai: credential rejected")

// validateOpenAICredential issues a cheap GET against the models
// endpoint to confirm OpenAI accepts the supplied credential before we
// persist it. Mirrors validateAnthropicCredential: 2xx → ok, 401/403 →
// rejected, anything else → unverified (so the user gets the
// "couldn't reach OpenAI" banner instead of a misleading "invalid key"
// when the failure is upstream).
func validateOpenAICredential(ctx context.Context, kind openaiCredKind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("openai: empty credential")
	}

	reqCtx, cancel := context.WithTimeout(ctx, openaiValidateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, openaiAPIBase()+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("openai: build request: %w", err)
	}
	if !applyOpenAIAuth(req, kind, value) {
		return fmt.Errorf("openai: unknown credential kind %d", kind)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("openai: request: %w", err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errOpenAIInvalidCredential
	default:
		return fmt.Errorf("openai: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}

// applyOpenAIAuth attaches the credential-specific headers for an
// outbound OpenAI call. Both kinds use Bearer auth today (the Codex
// subscription token is also a bearer JWT), but routing through one
// function keeps the call site honest: when OpenAI introduces a
// different header for subscription tokens we only have to change
// here.
func applyOpenAIAuth(req *http.Request, kind openaiCredKind, value string) bool {
	switch kind {
	case openaiCredAPIKey, openaiCredOAuthToken:
		req.Header.Set("Authorization", "Bearer "+value)
		return true
	default:
		return false
	}
}
