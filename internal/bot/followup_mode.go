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

	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

type followUpMode string

const (
	followUpModeChange     followUpMode = "change"
	followUpModeInspect    followUpMode = "inspect"
	followUpModeAnswerOnly followUpMode = "answer_only"
)

const (
	followUpModeModel          = "claude-haiku-4-5-20251001"
	followUpModeTimeout        = 3 * time.Second
	minNonChangeModeConfidence = 0.6
)

type followUpModeDecision struct {
	Mode       followUpMode `json:"mode"`
	Confidence float64      `json:"confidence"`
	Reason     string       `json:"reason"`
}

type followUpModeFunc func(context.Context, orgcfg.Config, convstore.Record, string) followUpModeDecision

func (b *Bot) decideFollowUpMode(ctx context.Context, oc orgcfg.Config, rec convstore.Record, userRequest string) followUpModeDecision {
	if b != nil && b.followUpModeFn != nil {
		return enforceFollowUpModeConfidence(normalizeFollowUpModeDecision(b.followUpModeFn(ctx, oc, rec, userRequest)))
	}
	decision, err := requestFollowUpMode(ctx, oc, rec, userRequest)
	if err != nil {
		if b != nil && b.log != nil {
			b.log.Warn("follow-up mode classification failed; defaulting to change",
				"org", oc.OrgID, "thread", rec.ThreadID, "error", err)
		}
		return followUpModeDecision{Mode: followUpModeChange, Confidence: 0, Reason: "classifier failed; defaulted to change"}
	}
	decision = enforceFollowUpModeConfidence(normalizeFollowUpModeDecision(decision))
	return decision
}

func enforceFollowUpModeConfidence(decision followUpModeDecision) followUpModeDecision {
	if decision.Mode != followUpModeChange && decision.Confidence < minNonChangeModeConfidence {
		return followUpModeDecision{
			Mode:       followUpModeChange,
			Confidence: decision.Confidence,
			Reason:     "non-change classification below confidence threshold; defaulted to change",
		}
	}
	return decision
}

func normalizeFollowUpModeDecision(decision followUpModeDecision) followUpModeDecision {
	switch decision.Mode {
	case followUpModeChange, followUpModeInspect, followUpModeAnswerOnly:
	default:
		decision.Mode = followUpModeChange
	}
	if decision.Confidence < 0 {
		decision.Confidence = 0
	}
	if decision.Confidence > 1 {
		decision.Confidence = 1
	}
	decision.Reason = strings.TrimSpace(decision.Reason)
	return decision
}

func requestFollowUpMode(ctx context.Context, oc orgcfg.Config, rec convstore.Record, userRequest string) (followUpModeDecision, error) {
	kind, value, ok := resolveAnthropicCred(oc)
	if !ok {
		return followUpModeDecision{}, errors.New("anthropic: no credential")
	}
	prompt := buildFollowUpModePrompt(rec, userRequest)
	body, err := json.Marshal(map[string]any{
		"model":      followUpModeModel,
		"max_tokens": 200,
		"system":     followUpModeSystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return followUpModeDecision{}, fmt.Errorf("marshal body: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, followUpModeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, anthropicAPIBase+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return followUpModeDecision{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if !applyAnthropicAuth(req, kind, value) {
		return followUpModeDecision{}, fmt.Errorf("anthropic: unknown credential kind %d", kind)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return followUpModeDecision{}, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return followUpModeDecision{}, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	text, err := anthropicTextContent(payload)
	if err != nil {
		return followUpModeDecision{}, err
	}
	decision, err := parseFollowUpModeDecision(text)
	if err != nil {
		return followUpModeDecision{}, err
	}
	return decision, nil
}

func anthropicTextContent(payload []byte) (string, error) {
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	var out strings.Builder
	for _, c := range parsed.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	return out.String(), nil
}

func parseFollowUpModeDecision(raw string) (followUpModeDecision, error) {
	raw = strings.TrimSpace(raw)
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end < start {
		return followUpModeDecision{}, fmt.Errorf("mode classifier returned no JSON object: %q", truncate(raw, 200))
	}
	var decision followUpModeDecision
	if err := json.Unmarshal([]byte(raw[start:end+1]), &decision); err != nil {
		return followUpModeDecision{}, fmt.Errorf("parse mode classifier JSON: %w", err)
	}
	return decision, nil
}

func buildFollowUpModePrompt(rec convstore.Record, userRequest string) string {
	history := strings.Join(rec.History, "\n---\n")
	if len(history) > 4000 {
		history = history[len(history)-4000:]
	}
	return fmt.Sprintf(`Classify the latest follow-up turn.

Open PR: %s
Branch: %s

Conversation so far:
%s

Latest user request:
%s
`, rec.PRURL, rec.Branch, history, userRequest)
}

const followUpModeSystemPrompt = `You classify follow-up chat turns for a coding agent.

Return only a strict JSON object with this schema:
{"mode":"change|inspect|answer_only","confidence":0.0,"reason":"short explanation"}

Modes:
- change: the user wants code, files, commits, pushes, PR updates, validation, or fixes. Use this for any ambiguous request.
- inspect: the user wants investigation, logs, status, review, explanation grounded in repo/runtime state, or anomaly analysis, but did not ask to modify code or PR state.
- answer_only: the user wants a simple conversational answer and does not need repo inspection, validation, git, or PR work.

Default to change unless the latest request is clearly inspect or answer_only.`
