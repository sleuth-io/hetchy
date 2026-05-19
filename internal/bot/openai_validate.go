package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// validateOpenAICredential checks the credential before we persist it.
// API keys are validated with a cheap OpenAI Platform request. Codex
// subscription tokens come from ChatGPT/Codex login, not the Platform
// API, so using /v1/models would incorrectly reject valid tokens.
func validateOpenAICredential(ctx context.Context, kind openaiCredKind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("openai: empty credential")
	}
	if kind == openaiCredOAuthToken {
		return validateOpenAICodexToken(value)
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

func validateOpenAICodexToken(value string) error {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return errOpenAIInvalidCredential
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errOpenAIInvalidCredential
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return errOpenAIInvalidCredential
	}
	if claims.ExpiresAt != 0 && claims.ExpiresAt <= time.Now().Unix() {
		return errOpenAIInvalidCredential
	}
	return nil
}

// applyOpenAIAuth attaches the credential-specific headers for an
// outbound OpenAI Platform call.
func applyOpenAIAuth(req *http.Request, kind openaiCredKind, value string) bool {
	switch kind {
	case openaiCredAPIKey:
		req.Header.Set("Authorization", "Bearer "+value)
		return true
	case openaiCredOAuthToken:
		return false
	default:
		return false
	}
}
