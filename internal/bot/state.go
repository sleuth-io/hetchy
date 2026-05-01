package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// persistedConversation is the on-disk representation of a conversation.
// The sandbox is referenced by ID and rehydrated on load.
type persistedConversation struct {
	SandboxID string   `json:"sandbox_id"`
	Branch    string   `json:"branch"`
	PRURL     string   `json:"pr_url"`
	History   []string `json:"history"`
}

type persistedState struct {
	Conversations map[string]*persistedConversation `json:"conversations"`
}

func (b *Bot) loadState(ctx context.Context) error {
	data, err := os.ReadFile(b.cfg.StateFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return fmt.Errorf("parse state file: %w", err)
	}
	for threadID, pc := range ps.Conversations {
		var sb *daytona.Sandbox
		err := b.retryWithBackoff(ctx, "get sandbox", func() error {
			var err error
			sb, err = b.daytona.Get(ctx, pc.SandboxID)
			return err
		})
		if err != nil {
			b.log.Warn("state: sandbox not found, dropping conversation",
				"thread_id", threadID, "sandbox_id", pc.SandboxID, "error", err)
			continue
		}
		b.convos[threadID] = &conversation{
			sandbox: sb,
			branch:  pc.Branch,
			prURL:   pc.PRURL,
			history: pc.History,
		}
		b.log.Info("state: restored conversation",
			"thread_id", threadID, "sandbox_id", pc.SandboxID, "branch", pc.Branch)
	}
	return nil
}

func (b *Bot) saveState() {
	ps := persistedState{Conversations: make(map[string]*persistedConversation, len(b.convos))}
	for threadID, conv := range b.convos {
		ps.Conversations[threadID] = &persistedConversation{
			SandboxID: conv.sandbox.ID,
			Branch:    conv.branch,
			PRURL:     conv.prURL,
			History:   conv.history,
		}
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		b.log.Error("state: marshal failed", "error", err)
		return
	}
	if err := os.WriteFile(b.cfg.StateFile, data, 0600); err != nil {
		b.log.Error("state: write failed", "path", b.cfg.StateFile, "error", err)
	}
}
