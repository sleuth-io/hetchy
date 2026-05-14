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

var anthropicAPIBase = "https://api.anthropic.com"

const (
	anthropicValidateTimeout = 8 * time.Second
	anthropicValidateModel   = "claude-3-5-haiku-20241022"
)

type anthropicCredKind int

const (
	anthropicCredAPIKey anthropicCredKind = iota
	anthropicCredOAuthToken
)

var errAnthropicInvalidCredential = errors.New("anthropic: credential rejected")

// validateAnthropicCredential makes a no-output token-count request to
// prove Anthropic accepts a newly saved credential before we persist it.
func validateAnthropicCredential(ctx context.Context, kind anthropicCredKind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("anthropic: empty credential")
	}

	body, err := json.Marshal(map[string]any{
		"model":    anthropicValidateModel,
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
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	default:
		return fmt.Errorf("anthropic: unknown credential kind %d", kind)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic: request: %w", err)
	}
	defer resp.Body.Close()
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
