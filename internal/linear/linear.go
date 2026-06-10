// Package linear is a minimal client for the slice of Linear's GraphQL
// API that Hetchy's agent integration uses: posting agent activities,
// attaching external URLs to agent sessions, moving issues into a
// started workflow state, and the OAuth (actor=app) install handshake.
//
// Linear's official SDK is TypeScript-only; the handful of operations
// here don't justify a generated client, so queries are inline strings
// against the stable public schema.
package linear

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPIEndpoint is Linear's public GraphQL endpoint.
	DefaultAPIEndpoint = "https://api.linear.app/graphql"
	// DefaultTokenEndpoint exchanges OAuth authorization codes.
	DefaultTokenEndpoint = "https://api.linear.app/oauth/token"
	// DefaultRevokeEndpoint revokes an OAuth access token.
	DefaultRevokeEndpoint = "https://api.linear.app/oauth/revoke"
	// AuthorizeEndpoint is the browser-facing OAuth consent URL.
	AuthorizeEndpoint = "https://linear.app/oauth/authorize"
)

// maxResponseBytes caps GraphQL/OAuth response bodies. Linear's
// responses for the operations here are tiny; anything larger is
// broken or hostile.
const maxResponseBytes = 1 << 20

// Client is an authenticated handle to one workspace's Linear API.
// The token is an OAuth actor=app access token from the install flow.
type Client struct {
	httpClient *http.Client
	endpoint   string
	token      string
}

// NewClient builds a Client for token. Endpoint defaults to the public
// API; tests override via NewClientWithEndpoint.
func NewClient(token string) *Client {
	return NewClientWithEndpoint(token, DefaultAPIEndpoint)
}

// NewClientWithEndpoint is NewClient with an explicit GraphQL endpoint.
func NewClientWithEndpoint(token, endpoint string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		endpoint:   endpoint,
		token:      token,
	}
}

// graphQLError is one entry of a GraphQL `errors` array.
type graphQLError struct {
	Message string `json:"message"`
}

// do executes a GraphQL operation and decodes `data` into out (when
// non-nil). GraphQL-level errors are returned as a single joined error.
func (c *Client) do(ctx context.Context, query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("linear: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("linear: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linear: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("linear: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: http %d: %s", resp.StatusCode, truncateForError(body))
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("linear: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("linear: graphql: %s", strings.Join(msgs, "; "))
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("linear: decode data: %w", err)
		}
	}
	return nil
}

func truncateForError(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// Identity describes the installed app within a workspace: the app's
// own user id (viewer) plus the workspace's organization id and name.
type Identity struct {
	AppUserID     string
	WorkspaceID   string
	WorkspaceName string
}

// Identity fetches the app's viewer id and the workspace organization
// id/name. Called once at OAuth-install time; the results are stored
// on the org config for webhook routing.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	const query = `query HetchyIdentity { viewer { id } organization { id name } }`
	var data struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
		Organization struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"organization"`
	}
	if err := c.do(ctx, query, nil, &data); err != nil {
		return Identity{}, err
	}
	if data.Viewer.ID == "" || data.Organization.ID == "" {
		return Identity{}, errors.New("linear: identity response missing viewer or organization id")
	}
	return Identity{
		AppUserID:     data.Viewer.ID,
		WorkspaceID:   data.Organization.ID,
		WorkspaceName: data.Organization.Name,
	}, nil
}

// ActivityContent is the polymorphic `content` object of an agent
// activity. Type is one of: thought, action, elicitation, response,
// error. Body carries markdown for everything except action, which
// uses Action/Parameter/Result instead.
type ActivityContent struct {
	Type      string `json:"type"`
	Body      string `json:"body,omitempty"`
	Action    string `json:"action,omitempty"`
	Parameter string `json:"parameter,omitempty"`
	Result    string `json:"result,omitempty"`
}

// CreateActivity posts one agent activity onto a session. Ephemeral
// activities display temporarily and are replaced by the next activity
// — used for high-churn progress so the issue timeline stays readable.
func (c *Client) CreateActivity(ctx context.Context, sessionID string, content ActivityContent, ephemeral bool) error {
	const mutation = `mutation HetchyAgentActivityCreate($input: AgentActivityCreateInput!) {
  agentActivityCreate(input: $input) { success }
}`
	input := map[string]any{
		"agentSessionId": sessionID,
		"content":        content,
	}
	if ephemeral {
		input["ephemeral"] = true
	}
	var data struct {
		AgentActivityCreate struct {
			Success bool `json:"success"`
		} `json:"agentActivityCreate"`
	}
	if err := c.do(ctx, mutation, map[string]any{"input": input}, &data); err != nil {
		return err
	}
	if !data.AgentActivityCreate.Success {
		return errors.New("linear: agentActivityCreate not successful")
	}
	return nil
}

// ExternalURL is a labelled link attached to an agent session —
// Hetchy's run page and the resulting pull request.
type ExternalURL struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// AddExternalURLs appends links to the session without replacing ones
// already attached.
func (c *Client) AddExternalURLs(ctx context.Context, sessionID string, urls []ExternalURL) error {
	if len(urls) == 0 {
		return nil
	}
	const mutation = `mutation HetchyAgentSessionUpdate($id: String!, $input: AgentSessionUpdateInput!) {
  agentSessionUpdate(id: $id, input: $input) { success }
}`
	var data struct {
		AgentSessionUpdate struct {
			Success bool `json:"success"`
		} `json:"agentSessionUpdate"`
	}
	vars := map[string]any{
		"id":    sessionID,
		"input": map[string]any{"addedExternalUrls": urls},
	}
	if err := c.do(ctx, mutation, vars, &data); err != nil {
		return err
	}
	if !data.AgentSessionUpdate.Success {
		return errors.New("linear: agentSessionUpdate not successful")
	}
	return nil
}

// MoveIssueToStarted moves the issue into its team's first "started"
// workflow state, per Linear's agent best practices. Issues already in
// a started/completed/canceled state are left alone.
func (c *Client) MoveIssueToStarted(ctx context.Context, issueID string) error {
	const query = `query HetchyIssueStates($id: String!) {
  issue(id: $id) {
    id
    state { id type }
    team {
      states(filter: { type: { eq: "started" } }) {
        nodes { id position }
      }
    }
  }
}`
	var data struct {
		Issue struct {
			ID    string `json:"id"`
			State struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"state"`
			Team struct {
				States struct {
					Nodes []struct {
						ID       string  `json:"id"`
						Position float64 `json:"position"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"team"`
		} `json:"issue"`
	}
	if err := c.do(ctx, query, map[string]any{"id": issueID}, &data); err != nil {
		return err
	}
	switch data.Issue.State.Type {
	case "started", "completed", "canceled":
		return nil
	}
	nodes := data.Issue.Team.States.Nodes
	if len(nodes) == 0 {
		return nil
	}
	target := nodes[0]
	for _, n := range nodes[1:] {
		if n.Position < target.Position {
			target = n
		}
	}
	const mutation = `mutation HetchyIssueUpdate($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) { success }
}`
	vars := map[string]any{
		"id":    issueID,
		"input": map[string]any{"stateId": target.ID},
	}
	var update struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.do(ctx, mutation, vars, &update); err != nil {
		return err
	}
	if !update.IssueUpdate.Success {
		return errors.New("linear: issueUpdate not successful")
	}
	return nil
}

// VerifyWebhookSignature checks the `linear-signature` header value (a
// lowercase hex HMAC-SHA256 of the raw body, keyed by the app's
// webhook signing secret) in constant time.
func VerifyWebhookSignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature)))
}

// AuthorizeURL builds the actor=app OAuth consent URL.
func AuthorizeURL(clientID, redirectURI, state string, scopes []string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, ","))
	q.Set("state", state)
	q.Set("actor", "app")
	q.Set("prompt", "consent")
	return AuthorizeEndpoint + "?" + q.Encode()
}

// ExchangeCode swaps an authorization code for an access token.
// tokenEndpoint is parameterised for tests; pass DefaultTokenEndpoint
// in production.
func ExchangeCode(ctx context.Context, httpClient *http.Client, tokenEndpoint, clientID, clientSecret, redirectURI, code string) (string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("redirect_uri", redirectURI)
	form.Set("code", code)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("linear: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("linear: token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("linear: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("linear: token exchange http %d: %s", resp.StatusCode, truncateForError(body))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("linear: decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("linear: token response missing access_token")
	}
	return tok.AccessToken, nil
}

// RevokeToken best-effort revokes an access token at Linear's side.
// revokeEndpoint is parameterised for tests; pass DefaultRevokeEndpoint
// in production.
func RevokeToken(ctx context.Context, httpClient *http.Client, revokeEndpoint, token string) error {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeEndpoint, nil)
	if err != nil {
		return fmt.Errorf("linear: build revoke request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linear: revoke: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: revoke http %d", resp.StatusCode)
	}
	return nil
}
