package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

const (
	codexAuthJSONPath         = "/home/daytona/.hetchy-codex-home/auth.json"
	codexAuthWritebackTimeout = 30 * time.Second
	maxCodexAuthJSONBytes     = 256 * 1024
)

// writeBackOpenAICodexAuthJSON persists the auth.json that Codex may
// refresh during a ChatGPT-backed run. This intentionally does not
// serialize all Codex runs for the org: concurrent runs are allowed, and
// a stale sandbox only writes back if the org still stores the same seed
// auth JSON it started with.
func (b *Bot) writeBackOpenAICodexAuthJSON(parent context.Context, sb *daytona.Sandbox, oc orgcfg.Config, requestID string) {
	if err := b.persistOpenAICodexAuthJSON(parent, sb, oc, requestID); err != nil && b.log != nil {
		b.log.Warn("codex auth writeback failed",
			"org", oc.OrgID,
			"request_id", requestID,
			"error", err,
		)
	}
}

func (b *Bot) persistOpenAICodexAuthJSON(parent context.Context, sb *daytona.Sandbox, oc orgcfg.Config, requestID string) error {
	kind, original := openAICodexAuth(oc)
	if kind != "auth_json" || strings.TrimSpace(original) == "" || strings.TrimSpace(oc.OrgID) == "" || b.orgs == nil {
		return nil
	}

	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexAuthWritebackTimeout)
	defer cancel()

	raw, err := b.downloadSandboxFile(ctx, sb, codexAuthJSONPath)
	if err != nil {
		return fmt.Errorf("download codex auth json: %w", err)
	}
	updated, err := canonicalOpenAICodexAuthJSON(raw)
	if err != nil {
		return fmt.Errorf("normalize refreshed codex auth json: %w", err)
	}
	originalCanonical, err := canonicalOpenAICodexAuthJSON([]byte(original))
	if err != nil {
		return fmt.Errorf("normalize stored codex auth json: %w", err)
	}
	if updated == originalCanonical {
		return nil
	}

	latest, err := b.orgs.Get(ctx, oc.OrgID)
	if err != nil {
		return fmt.Errorf("get latest org config: %w", err)
	}
	if strings.TrimSpace(latest.OpenAIAPIKey) != "" {
		if b.log != nil {
			b.log.Info("codex auth writeback skipped; org switched to API key",
				"org", oc.OrgID,
				"request_id", requestID,
			)
		}
		return nil
	}
	latestCanonical, err := canonicalOpenAICodexAuthJSON([]byte(latest.OpenAICodexOAuthToken))
	if err != nil {
		if b.log != nil {
			b.log.Info("codex auth writeback skipped; org credential changed",
				"org", oc.OrgID,
				"request_id", requestID,
			)
		}
		return nil
	}
	if latestCanonical == updated {
		return nil
	}
	if latestCanonical != originalCanonical {
		if b.log != nil {
			b.log.Info("codex auth writeback skipped; newer auth already stored",
				"org", oc.OrgID,
				"request_id", requestID,
			)
		}
		return nil
	}

	latest.OpenAICodexOAuthToken = updated
	if _, err := b.orgs.Upsert(ctx, latest); err != nil {
		return fmt.Errorf("store refreshed codex auth json: %w", err)
	}
	if b.log != nil {
		b.log.Info("codex auth json refreshed",
			"org", oc.OrgID,
			"request_id", requestID,
			"bytes", len(updated),
		)
	}
	return nil
}

func (b *Bot) downloadSandboxFile(ctx context.Context, sb *daytona.Sandbox, remotePath string) ([]byte, error) {
	if b.downloadSandboxFileFn != nil {
		return b.downloadSandboxFileFn(ctx, sb, remotePath)
	}
	if sb == nil || sb.FileSystem == nil {
		return nil, errors.New("sandbox filesystem not configured")
	}
	return sb.FileSystem.DownloadFile(ctx, remotePath, nil)
}

func canonicalOpenAICodexAuthJSON(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", errOpenAIInvalidCredential
	}
	if len(raw) > maxCodexAuthJSONBytes {
		return "", fmt.Errorf("codex auth json too large: %d bytes", len(raw))
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", errOpenAIInvalidCredential
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errOpenAIInvalidCredential
	}

	compact, err := json.Marshal(v)
	if err != nil {
		return "", errOpenAIInvalidCredential
	}
	s := string(compact)
	if err := validateOpenAICodexAuthJSON(s); err != nil {
		return "", err
	}
	return s, nil
}
