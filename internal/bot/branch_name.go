package bot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// branchNameModel is the Anthropic model used to suggest a slug for
// the user's request. Haiku is fast and cheap; a bad suggestion just
// means a less-pretty branch name, never a failure of the run, so we
// don't need a stronger model here.
const branchNameModel = "claude-haiku-4-5-20251001"

// branchNameTimeout caps the slug-generation HTTP call. Haiku slug
// requests typically return in under a second; we set a tight cap so
// a slow/down Anthropic API doesn't translate into a visible UI pause
// before the sandbox-creation progress messages. On any failure we
// silently fall back to "sf".
const branchNameTimeout = 2 * time.Second

// branchSlugFallback is the slug we use when slug generation fails or
// produces nothing usable. Matches the historical "feature/sf-…"
// prefix so log greps and dashboards keyed on `sf-` still find these
// branches.
const branchSlugFallback = "sf"

// branchSlugMaxLen caps the LLM-suggested slug. Git ref names can be
// hundreds of characters, but a short branch is easier to read in PR
// URLs, CI logs, and the chat sidebar. With a 6-char suffix this keeps
// branches under ~50 characters end-to-end.
const branchSlugMaxLen = 40

// branchSuffixBytes is the entropy used for the random branch suffix.
// 3 bytes hex-encode to 6 lowercase chars — matching the example in
// the user request (`my-useful-branchname-3e2df2`).
const branchSuffixBytes = 3

// branchSlugCollapse collapses runs of hyphens left behind by
// sanitisation so we never produce `foo--bar` from `foo  bar`.
var branchSlugCollapse = regexp.MustCompile(`-+`)

// branchNameFor produces the full branch name (with `feature/` prefix)
// to use for a fresh-agent run. It asks the LLM to suggest a short
// kebab-case slug based on userRequest, sanitises it, and appends a
// random 6-char hex suffix. Any failure path — empty creds, network
// error, timeout, malformed response, or slug that sanitises to
// nothing — falls back to the "sf" slug so the branch is still unique.
//
// Tests can install branchNameFn on the Bot to bypass the LLM call
// (which would otherwise hit a real Anthropic endpoint per test).
func (b *Bot) branchNameFor(ctx context.Context, oc orgcfg.Config, userRequest string) string {
	if b != nil && b.branchNameFn != nil {
		return b.branchNameFn(ctx, oc, userRequest)
	}
	slug := b.generateBranchSlug(ctx, oc, userRequest)
	return "feature/" + slug + "-" + randomBranchSuffix()
}

// generateBranchSlug returns a sanitised kebab-case slug describing
// userRequest. It is the LLM-touching half of branchNameFor; broken
// out for testability and so the fallback path stays explicit.
func (b *Bot) generateBranchSlug(ctx context.Context, oc orgcfg.Config, userRequest string) string {
	userRequest = strings.TrimSpace(userRequest)
	if userRequest == "" {
		return branchSlugFallback
	}
	raw, err := requestBranchSlug(ctx, oc, userRequest)
	if err != nil {
		// Best-effort: warn and fall back. We deliberately don't
		// expose the failure to the user — the branch suffix is
		// cosmetic and shouldn't gate the run.
		if b != nil && b.log != nil {
			b.log.Warn("branch slug generation failed; falling back to sf",
				"org", oc.OrgID, "error", err)
		}
		return branchSlugFallback
	}
	slug := sanitizeBranchSlug(raw)
	if slug == "" {
		return branchSlugFallback
	}
	return slug
}

// requestBranchSlug calls the Anthropic Messages API and returns the
// raw model output (which sanitizeBranchSlug then cleans up). Returns
// an error when the org has no Anthropic credential configured, the
// request fails, or the response is unparseable.
func requestBranchSlug(ctx context.Context, oc orgcfg.Config, userRequest string) (string, error) {
	kind, value, ok := resolveAnthropicCred(oc)
	if !ok {
		return "", errors.New("anthropic: no credential")
	}

	body, err := json.Marshal(map[string]any{
		"model":      branchNameModel,
		"max_tokens": 60,
		"system":     branchNameSystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userRequest},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal body: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, branchNameTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, anthropicAPIBase+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	applyAnthropicAuth(req, kind, value)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

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

// branchNameSystemPrompt keeps the LLM tightly scoped to a single-line
// slug. Repeated wording is intentional: the model otherwise loves to
// add a leading "Branch name:" or wrap the result in backticks.
const branchNameSystemPrompt = `You name git branches.

Given a user's feature request, respond with a single short kebab-case branch slug describing the change.

Rules:
- Lowercase a-z, digits 0-9, and hyphens only. No slashes, spaces, underscores, dots, quotes, or punctuation.
- 2 to 5 words, joined by hyphens. 40 characters maximum.
- Start with a letter.
- Describe the change, not the verb tense. Examples: "add-dark-mode-toggle", "fix-login-redirect", "upgrade-node-20".
- Do not include a prefix like "feature/" — emit only the slug body.
- Do not wrap the slug in quotes, backticks, or markdown. Do not add commentary, explanation, or trailing punctuation.

Respond with the slug only, nothing else.`

// resolveAnthropicCred picks the credential to use for an outbound
// Anthropic API call from an orgcfg.Config. Mirrors the precedence in
// claudeAuthEnv: a subscription OAuth token wins over an API key when
// both are set. Returns ok=false when neither is configured. The
// returned (kind, value) feeds applyAnthropicAuth so the actual
// header-setting code lives in one place.
func resolveAnthropicCred(oc orgcfg.Config) (kind anthropicCredKind, value string, ok bool) {
	if tok := strings.TrimSpace(oc.ClaudeCodeOAuthToken); tok != "" {
		return anthropicCredOAuthToken, tok, true
	}
	if key := strings.TrimSpace(oc.AnthropicAPIKey); key != "" {
		return anthropicCredAPIKey, key, true
	}
	return 0, "", false
}

// applyAnthropicAuth sets the credential-specific headers on req for
// the given (kind, value). Shared by validateAnthropicCredential and
// requestBranchSlug so the OAuth-beta header and API-key header rules
// live in one place — if Anthropic rolls a new oauth beta value or
// adds a third credential kind, only this function changes. Returns
// false when kind doesn't match a known credential so callers can
// raise an "unknown credential kind" error.
func applyAnthropicAuth(req *http.Request, kind anthropicCredKind, value string) bool {
	switch kind {
	case anthropicCredAPIKey:
		req.Header.Set("x-api-key", value)
		return true
	case anthropicCredOAuthToken:
		req.Header.Set("Authorization", "Bearer "+value)
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		return true
	default:
		return false
	}
}

// sanitizeBranchSlug normalises raw model output to the safe slug
// character set. Returns an empty string when nothing usable remains
// (the caller falls back to "sf"). Steps:
//   - trim whitespace, strip a leading "feature/" if the model added it
//   - lowercase
//   - replace whitespace and underscores with hyphens
//   - drop any character outside [a-z0-9-]
//   - collapse runs of hyphens
//   - trim hyphens from both ends
//   - drop a leading digit so the slug always starts with a letter
//   - truncate to branchSlugMaxLen and trim hyphens again
func sanitizeBranchSlug(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`'\"")
	s = strings.TrimPrefix(s, "feature/")
	s = strings.TrimPrefix(s, "Branch name:")
	s = strings.TrimPrefix(s, "branch name:")
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)

	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '\t' || r == '\n' || r == '-':
			b.WriteByte('-')
		default:
			// drop
		}
	}
	s = b.String()
	s = branchSlugCollapse.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	// Drop any leading digits or hyphens so branch slugs always start
	// with a letter. This avoids ambiguity with abbreviated SHAs and
	// ticket numbers that humans often pattern-match on at a glance.
	// We strip the two character classes together in a single loop so
	// "2025-rewrite" → "rewrite" (not the intermediate "rewrite" that
	// a non-recursive pass would leave at "-rewrite", or the worse
	// "2-fix" left by stripping only the top digit of "12-fix").
	for len(s) > 0 && (s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) {
		s = s[1:]
	}

	if len(s) > branchSlugMaxLen {
		s = s[:branchSlugMaxLen]
	}
	s = strings.Trim(s, "-")
	return s
}

// randomBranchSuffix returns a 6-char lowercase hex string for use as
// the trailing collision-breaker on a branch name. Errors from
// crypto/rand.Read are vanishingly rare on every platform we deploy
// to, but we still degrade gracefully by encoding the partial buffer
// rather than panicking inside a chat handler.
func randomBranchSuffix() string {
	var buf [branchSuffixBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Time-based fallback: a UnixNano-derived 6-char suffix is
		// still unique-enough for collision-breaking on the same
		// branch slug, and only triggers on a broken kernel CSPRNG.
		ts := time.Now().UnixNano()
		return fmt.Sprintf("%06x", ts&0xffffff)
	}
	return hex.EncodeToString(buf[:])
}
