package bot

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

const sandboxEnvFileThreshold = 8 * 1024

var sandboxEnvFileKeys = map[string]string{
	"HETCHY_AGENT_PROMPT_B64": "HETCHY_AGENT_PROMPT_B64_FILE",
	"SF_PROMPT_B64":           "SF_PROMPT_B64_FILE",
	"SF_SPEC_HEALTH_B64":      "SF_SPEC_HEALTH_B64_FILE",
	"SF_SPEC_SETUP_B64":       "SF_SPEC_SETUP_B64_FILE",
	"SF_SPEC_START_B64":       "SF_SPEC_START_B64_FILE",
}

func (b *Bot) materializeLargeRunEnv(ctx context.Context, sandboxID string, proc sandboxProcess, sessionID, label string, env map[string]string) error {
	var keys []string
	for key := range sandboxEnvFileKeys {
		if val, ok := env[key]; ok && len(val) > sandboxEnvFileThreshold {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return nil
	}

	dir := "/tmp/hetchy-env/" + label
	var cmd strings.Builder
	cmd.WriteString("rm -rf -- ")
	cmd.WriteString(shellQuote(dir))
	cmd.WriteByte('\n')
	cmd.WriteString("mkdir -p -- ")
	cmd.WriteString(shellQuote(dir))

	fileVars := make(map[string]string, len(keys))
	for _, key := range keys {
		val := env[key]
		path := dir + "/" + strings.ToLower(key) + ".b64"
		fileVars[key] = path
		cmd.WriteByte('\n')
		cmd.WriteString(heredocWriteCmd(path, val, false))
		b.log.Info("sandbox env file write",
			"sandbox", sandboxID,
			"session", sessionID,
			"label", label,
			"key", key,
			"bytes", len(val),
		)
	}
	if _, err := b.shLines(ctx, sandboxID, proc, sessionID, "write-env", cmd.String(), 60*time.Second, 0, true, func(string) {}); err != nil {
		return fmt.Errorf("write env files: %w", err)
	}
	for _, key := range keys {
		fileKey := sandboxEnvFileKeys[key]
		delete(env, key)
		env[fileKey] = fileVars[key]
	}
	return nil
}
