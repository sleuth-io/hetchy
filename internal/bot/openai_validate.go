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
// subscription auth comes from ChatGPT/Codex login, not the Platform
// API, so using /v1/models would incorrectly reject valid auth.
func validateOpenAICredential(ctx context.Context, kind openaiCredKind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("openai: empty credential")
	}
	if kind == openaiCredOAuthToken {
		return validateOpenAICodexCredential(value)
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

type openAICodexAuthJSON struct {
	AuthMode string `json:"auth_mode"`
	Tokens   *struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	AgentIdentity string `json:"agent_identity"`
}

func validateOpenAICodexCredential(value string) error {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "{") {
		return validateOpenAICodexAuthJSON(value)
	}
	return validateOpenAICodexAgentIdentity(value)
}

func validateOpenAICodexAuthJSON(value string) error {
	var auth openAICodexAuthJSON
	if err := json.Unmarshal([]byte(value), &auth); err != nil {
		return errOpenAIInvalidCredential
	}
	switch strings.TrimSpace(auth.AuthMode) {
	case "agentIdentity":
		return validateOpenAICodexAgentIdentity(auth.AgentIdentity)
	case "", "chatgpt":
		if auth.Tokens == nil ||
			strings.TrimSpace(auth.Tokens.IDToken) == "" ||
			strings.TrimSpace(auth.Tokens.AccessToken) == "" ||
			strings.TrimSpace(auth.Tokens.RefreshToken) == "" ||
			strings.TrimSpace(auth.Tokens.AccountID) == "" {
			return errOpenAIInvalidCredential
		}
		if _, err := decodeJWTClaims(auth.Tokens.IDToken); err != nil {
			return errOpenAIInvalidCredential
		}
		if _, err := decodeJWTClaims(auth.Tokens.AccessToken); err != nil {
			return errOpenAIInvalidCredential
		}
		return nil
	default:
		return errOpenAIInvalidCredential
	}
}

func validateOpenAICodexAgentIdentity(value string) error {
	claims, err := decodeJWTClaims(value)
	if err != nil {
		return errOpenAIInvalidCredential
	}
	for _, field := range []string{"agent_runtime_id", "agent_private_key", "account_id", "chatgpt_user_id"} {
		if s, ok := claims[field].(string); !ok || strings.TrimSpace(s) == "" {
			return errOpenAIInvalidCredential
		}
	}
	if exp, ok := numericClaim(claims["exp"]); ok && exp <= time.Now().Unix() {
		return errOpenAIInvalidCredential
	}
	return nil
}

func decodeJWTClaims(value string) (map[string]any, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, errOpenAIInvalidCredential
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errOpenAIInvalidCredential
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errOpenAIInvalidCredential
	}
	return claims, nil
}

func numericClaim(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
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
