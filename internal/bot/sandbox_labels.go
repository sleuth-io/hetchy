package bot

import (
	"strings"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// Daytona sandbox label keys. Daytona surfaces these in the sandbox
// list/detail views and accepts them as filters, so we use them to
// scope ops queries to a single Hetchy env or organization (e.g.
// "show every sandbox owned by org X" or "stop all dev sandboxes").
//
// Keys are prefixed with "hetchy_" so they don't collide with labels
// that other tenants of a shared Daytona organization might set.
const (
	daytonaSandboxLabelEnv           = "hetchy_env"
	daytonaSandboxLabelOrgID         = "hetchy_org_id"
	daytonaSandboxLabelCacheVolumeID = "hetchy_cache_volume_id"
)

// daytonaSandboxLabels builds the label map attached to a sandbox at
// create time. The Hetchy organization id is included as a label so
// every sandbox can be traced back to the org that requested it —
// without this, multi-tenant audit/cleanup queries against the Daytona
// control plane would have nothing to filter on.
//
// Empty values are omitted: Daytona accepts empty label values, but
// they're useless as a filter and only add noise in the UI.
func daytonaSandboxLabels(cfg Config, oc orgcfg.Config, cacheVolumeID string) map[string]string {
	labels := map[string]string{
		daytonaSandboxLabelEnv: daytonaSandboxEnv(cfg.Env),
	}
	if orgID := strings.TrimSpace(oc.OrgID); orgID != "" {
		labels[daytonaSandboxLabelOrgID] = orgID
	}
	if volID := strings.TrimSpace(cacheVolumeID); volID != "" {
		labels[daytonaSandboxLabelCacheVolumeID] = volID
	}
	return labels
}
