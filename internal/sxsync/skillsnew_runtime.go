package sxsync

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

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

const skillsNewBotRuntimeTokenTTLSeconds = 6 * 60 * 60
const skillsNewDefaultURL = "https://app.skills.new"

type skillsNewBot struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

type skillsNewRuntimeToken struct {
	Token     string
	ExpiresAt time.Time
}

func (m *Manager) RuntimeSkillsNewEnv(ctx context.Context, orgID string, agent agents.Profile) (map[string]string, error) {
	if m == nil || m.orgs == nil {
		return nil, ErrNotConfigured
	}
	oc, err := m.orgs.Get(ctx, orgID)
	if err != nil {
		if errors.Is(err, orgcfg.ErrNotFound) {
			return nil, ErrNotConfigured
		}
		return nil, err
	}
	authToken := strings.TrimSpace(oc.SXKey)
	if authToken == "" {
		return nil, ErrNotConfigured
	}
	botName := strings.TrimSpace(agent.SXBot)
	if botName == "" {
		return nil, errors.New("sxsync: agent sx bot is required")
	}
	token, err := createSkillsNewBotRuntimeToken(ctx, skillsNewDefaultURL, authToken, botName, runtimeTokenLabel(agent), skillsNewBotRuntimeTokenTTLSeconds)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"HETCHY_AGENT_SX_BOT_KEY": token.Token,
	}, nil
}

func runtimeTokenLabel(agent agents.Profile) string {
	name := strings.TrimSpace(agent.DisplayName)
	if name == "" {
		name = strings.TrimSpace(agent.Slug)
	}
	if name == "" {
		name = strings.TrimSpace(agent.SXBot)
	}
	if name == "" {
		return "Hetchy runtime"
	}
	return "Hetchy runtime: " + name
}

func createSkillsNewBotRuntimeToken(ctx context.Context, serverURL, authToken, botName, label string, ttlSeconds int) (skillsNewRuntimeToken, error) {
	bots, err := listSkillsNewBots(ctx, serverURL, authToken)
	if err != nil {
		return skillsNewRuntimeToken{}, err
	}
	var botID string
	for _, bot := range bots {
		if bot.Name == botName || bot.Slug == botName || strings.EqualFold(bot.Name, botName) {
			botID = bot.ID
			break
		}
	}
	if botID == "" {
		return skillsNewRuntimeToken{}, fmt.Errorf("sxsync: skills.new bot %q not found", botName)
	}

	var out struct {
		CreateBotRuntimeToken struct {
			BotKey    string    `json:"botKey"`
			ExpiresAt time.Time `json:"expiresAt"`
		} `json:"createBotRuntimeToken"`
	}
	vars := map[string]any{
		"botId":      botID,
		"ttlSeconds": ttlSeconds,
	}
	if strings.TrimSpace(label) != "" {
		vars["label"] = strings.TrimSpace(label)
	}
	if err := doSkillsNewGraphQL(ctx, serverURL, authToken, "CreateBotRuntimeToken", createBotRuntimeTokenMutation, vars, &out); err != nil {
		return skillsNewRuntimeToken{}, err
	}
	if out.CreateBotRuntimeToken.BotKey == "" {
		return skillsNewRuntimeToken{}, errors.New("sxsync: createBotRuntimeToken returned an empty token")
	}
	return skillsNewRuntimeToken{
		Token:     out.CreateBotRuntimeToken.BotKey,
		ExpiresAt: out.CreateBotRuntimeToken.ExpiresAt,
	}, nil
}

func listSkillsNewBots(ctx context.Context, serverURL, authToken string) ([]skillsNewBot, error) {
	var out struct {
		Bots []skillsNewBot `json:"bots"`
	}
	if err := doSkillsNewGraphQL(ctx, serverURL, authToken, "ListBots", listBotsQuery, nil, &out); err != nil {
		return nil, err
	}
	return out.Bots, nil
}

func doSkillsNewGraphQL(ctx context.Context, serverURL, authToken, operationName, query string, variables map[string]any, out any) error {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	authToken = strings.TrimSpace(authToken)
	if serverURL == "" || authToken == "" {
		return errors.New("sxsync: skills.new server URL and auth token are required")
	}
	if variables == nil {
		variables = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"operationName": operationName,
		"query":         query,
		"variables":     variables,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sxsync: skills.new GraphQL %s returned HTTP %d: %s", operationName, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var gqlResp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &gqlResp); err != nil {
		return err
	}
	if len(gqlResp.Errors) > 0 {
		msgs := make([]string, 0, len(gqlResp.Errors))
		for _, gqlErr := range gqlResp.Errors {
			msgs = append(msgs, gqlErr.Message)
		}
		return fmt.Errorf("sxsync: skills.new GraphQL %s failed: %s", operationName, strings.Join(msgs, "; "))
	}
	if len(gqlResp.Data) == 0 || string(gqlResp.Data) == "null" {
		return fmt.Errorf("sxsync: skills.new GraphQL %s returned no data", operationName)
	}
	return json.Unmarshal(gqlResp.Data, out)
}

const listBotsQuery = `
query ListBots {
  bots {
    id
    name
    slug
    description
  }
}`

const createBotRuntimeTokenMutation = `
mutation CreateBotRuntimeToken($botId: ID!, $label: String, $ttlSeconds: Int) {
  createBotRuntimeToken(botId: $botId, label: $label, ttlSeconds: $ttlSeconds) {
    botKey
    expiresAt
  }
}`
