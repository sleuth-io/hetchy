package sxsync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

const skillsNewBotRuntimeTokenTTLSeconds = 6 * 60 * 60

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
	serverURL, _ := m.skillsNewHTTPConfig()
	token, err := createSkillsNewBotRuntimeToken(ctx, serverURL, authToken, botName, runtimeTokenLabel(agent), skillsNewBotRuntimeTokenTTLSeconds)
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

func createSkillsNewBotRuntimeToken(ctx context.Context, serverURL, authToken, botName, label string, ttlSeconds int) (sxlib.BotRuntimeTokenResult, error) {
	client, err := sxlib.OpenSkillsNewWithOptions(serverURL, sxlib.SkillsNewOptions{
		AuthToken: authToken,
	})
	if err != nil {
		return sxlib.BotRuntimeTokenResult{}, err
	}
	botName = strings.TrimSpace(botName)
	bots, err := client.ListBots(ctx)
	if err != nil {
		return sxlib.BotRuntimeTokenResult{}, err
	}
	var canonicalBotName string
	for _, bot := range bots {
		if bot.Name == botName || bot.Slug == botName || strings.EqualFold(bot.Name, botName) {
			canonicalBotName = bot.Name
			break
		}
	}
	if canonicalBotName == "" {
		return sxlib.BotRuntimeTokenResult{}, fmt.Errorf("sxsync: skills.new bot %q not found", botName)
	}
	return client.CreateBotRuntimeToken(ctx, sxlib.BotRuntimeTokenSpec{
		BotName:    canonicalBotName,
		Label:      label,
		TTLSeconds: ttlSeconds,
	})
}
