package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// anthropicAPIBase is the base URL used by validateAnthropicCredential.
// Tests override it to point at an httptest.Server. Production code never
// mutates it.
var anthropicAPIBase = "https://api.anthropic.com"

// anthropicValidateTimeout caps the user-facing wait when we ping
// Anthropic during a settings save. Anthropic typically responds in well
// under a second; an 8 s budget keeps the form snappy without
// false-flagging credentials when their API is briefly slow.
const anthropicValidateTimeout = 8 * time.Second

// anthropicCredKind tags which auth method a credential uses. The two
// methods need different request headers (x-api-key vs Authorization
// Bearer plus the OAuth beta header), so the caller has to be explicit.
type anthropicCredKind int

const (
	anthropicCredAPIKey anthropicCredKind = iota
	anthropicCredOAuthToken
)

// errAnthropicInvalidCredential is returned when Anthropic definitively
// rejects the credential (401 / 403). Callers map this to a user-facing
// "your key is invalid" message; anything else from validate is treated
// as a transient failure so we don't lock users out when Anthropic has
// an outage.
var errAnthropicInvalidCredential = errors.New("anthropic: credential rejected")

// validateAnthropicCredential makes a single, free POST to
// /v1/messages/count_tokens with the supplied credential and reports
// whether Anthropic accepted it.
//
// We pick count_tokens because it (a) requires the same auth as the
// real /v1/messages endpoint the agent will use, (b) does not consume
// any model usage credit, and (c) works with both Anthropic Console API
// keys and the long-lived OAuth tokens minted by `claude setup-token`.
// /v1/models would be cheaper to call but OAuth tokens scoped to
// user:inference can't hit it, so we'd get a 403 on a perfectly good
// credential — defeating the whole point of the check.
//
// Return contract:
//   - nil                            → credential validated.
//   - errAnthropicInvalidCredential  → Anthropic returned 401/403; the
//     key is wrong and the caller should refuse to save it.
//   - any other error                → transient (network, 5xx, DNS,
//     timeout). The caller decides how to handle this; the current
//     settings flow surfaces it to the user and refuses the save so we
//     never store an unverified key.
func validateAnthropicCredential(ctx context.Context, kind anthropicCredKind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("anthropic: empty credential")
	}

	body, err := json.Marshal(map[string]any{
		// Any currently-served model works here — count_tokens only
		// validates the shape of the request and the auth header. We
		// pick a small, broadly-available model so this doesn't break
		// the day a larger one is retired.
		"model":    "claude-haiku-4-5",
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	})
	if err != nil {
		return fmt.Errorf("anthropic: marshal body: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, anthropicValidateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, anthropicAPIBase+"/v1/messages/count_tokens", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("anthropic: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	switch kind {
	case anthropicCredAPIKey:
		req.Header.Set("x-api-key", value)
	case anthropicCredOAuthToken:
		req.Header.Set("Authorization", "Bearer "+value)
		// OAuth tokens minted by `claude setup-token` are gated behind
		// this beta header; without it Anthropic returns 401 even for
		// a valid token, which would false-flag every subscription
		// user.
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	default:
		return fmt.Errorf("anthropic: unknown credential kind %d", kind)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic: request: %w", err)
	}
	defer resp.Body.Close()
	// Drain a small slice so callers can include API-side detail when
	// the request fails. The body is capped to keep a hostile or
	// chatty error response from blowing up logs.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errAnthropicInvalidCredential
	default:
		return fmt.Errorf("anthropic: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}
