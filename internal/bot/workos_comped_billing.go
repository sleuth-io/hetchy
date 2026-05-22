package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	workos "github.com/workos/workos-go/v7"
)

const workOSCompedBillingFlagSlug = "hetchy-billing-comped"

func (b *Bot) syncWorkOSCompedBillingForOrg(ctx context.Context, orgID string) (bool, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" || !b.workOSCompedSyncConfigured() {
		return false, nil
	}
	enabled, err := b.workOSOrgHasFeatureFlag(ctx, orgID, workOSCompedBillingFlagSlug)
	if err != nil {
		return false, err
	}
	if _, err := b.billing.SetBillingExempt(ctx, orgID, enabled); err != nil {
		return false, err
	}
	return enabled, nil
}

func (b *Bot) workOSCompedSyncConfigured() bool {
	if b == nil || b.billing == nil || !b.billing.Enabled() {
		return false
	}
	if b.workOSOrgHasFeatureFlagFn != nil {
		return true
	}
	return b.auth != nil && !b.cfg.AuthBypass && strings.TrimSpace(b.cfg.WorkOSAPIKey) != ""
}

func (b *Bot) workOSOrgHasFeatureFlag(ctx context.Context, orgID, slug string) (bool, error) {
	if b.workOSOrgHasFeatureFlagFn != nil {
		return b.workOSOrgHasFeatureFlagFn(ctx, orgID, slug)
	}
	if b.auth == nil {
		return false, errors.New("workos feature flag sync: auth service is not configured")
	}
	return b.auth.OrganizationHasFeatureFlag(ctx, orgID, slug)
}

func (b *Bot) workOSWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	webhookSecret := strings.TrimSpace(b.cfg.WorkOSWebhookSecret)
	if webhookSecret == "" {
		if b.cfg.Env == "dev" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "workos webhook is not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	verifier := workos.NewWebhookVerifier(webhookSecret)
	event, err := verifier.ConstructEvent(r.Header.Get("WorkOS-Signature"), string(body))
	if err != nil {
		b.log.Warn("workos webhook signature verification failed", "error", err)
		http.Error(w, "invalid workos signature", http.StatusBadRequest)
		return
	}
	if err := b.handleWorkOSEvent(r.Context(), event); err != nil {
		b.log.Error("handle workos event", "event_id", event.ID, "event_type", event.Event, "error", err)
		http.Error(w, "workos webhook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *Bot) handleWorkOSEvent(ctx context.Context, event *workos.EventSchema) error {
	if event == nil || workOSEventDataString(event.Data, "slug") != workOSCompedBillingFlagSlug {
		return nil
	}
	if b.billing == nil || !b.billing.Enabled() {
		return nil
	}
	switch event.Event {
	case "flag.created", "flag.rule_updated", "flag.updated", "flag.deleted":
	default:
		return nil
	}
	orgIDs := workOSFeatureFlagEventOrgIDs(event)
	if len(orgIDs) == 0 {
		var err error
		orgIDs, err = b.workOSFallbackBillingOrgIDs(ctx, event.Event)
		if err != nil {
			return err
		}
	}
	if event.Event == "flag.deleted" {
		for _, orgID := range orgIDs {
			if _, err := b.billing.SetBillingExempt(ctx, orgID, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, orgID := range orgIDs {
		if _, err := b.syncWorkOSCompedBillingForOrg(ctx, orgID); err != nil {
			return fmt.Errorf("sync comped billing for %s: %w", orgID, err)
		}
	}
	return nil
}

func (b *Bot) workOSFallbackBillingOrgIDs(ctx context.Context, eventType string) ([]string, error) {
	if b.billing == nil || !b.billing.Enabled() {
		return nil, nil
	}
	switch eventType {
	case "flag.deleted":
		return b.billing.ListBillingExemptOrgIDs(ctx)
	case "flag.rule_updated":
		return b.billing.ListAccountOrgIDs(ctx)
	default:
		return nil, nil
	}
}

func workOSEventDataString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	v, _ := data[key].(string)
	return strings.TrimSpace(v)
}

func workOSFeatureFlagEventOrgIDs(event *workos.EventSchema) []string {
	ids := map[string]struct{}{}
	addWorkOSConfiguredTargetOrgIDs(ids, event.Context)
	if prev, ok := nestedMap(event.Context, "previous_attributes", "context"); ok {
		addWorkOSConfiguredTargetOrgIDs(ids, prev)
	}
	return sortedMapKeys(ids)
}

func addWorkOSConfiguredTargetOrgIDs(ids map[string]struct{}, ctx map[string]any) {
	targets, ok := nestedMap(ctx, "configured_targets")
	if !ok {
		return
	}
	rawOrgs, ok := targets["organizations"].([]any)
	if !ok {
		return
	}
	for _, raw := range rawOrgs {
		org, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := org["id"].(string); strings.TrimSpace(id) != "" {
			ids[strings.TrimSpace(id)] = struct{}{}
		}
	}
}

func nestedMap(root map[string]any, keys ...string) (map[string]any, bool) {
	current := root
	for _, key := range keys {
		if current == nil {
			return nil, false
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func sortedMapKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
