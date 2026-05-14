package bot

import (
	"testing"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestDaytonaSandboxLabelsIncludesOrgID(t *testing.T) {
	labels := daytonaSandboxLabels(
		Config{Env: "production"},
		orgcfg.Config{OrgID: "org_abc"},
		"vol-1",
	)
	if got := labels[daytonaSandboxLabelOrgID]; got != "org_abc" {
		t.Fatalf("org id label = %q, want %q", got, "org_abc")
	}
	if got := labels[daytonaSandboxLabelEnv]; got != "prod" {
		t.Fatalf("env label = %q, want %q", got, "prod")
	}
	if got := labels[daytonaSandboxLabelCacheVolumeID]; got != "vol-1" {
		t.Fatalf("cache volume label = %q, want %q", got, "vol-1")
	}
}

func TestDaytonaSandboxLabelsTrimsOrgID(t *testing.T) {
	labels := daytonaSandboxLabels(
		Config{Env: "dev"},
		orgcfg.Config{OrgID: "  org_trim  "},
		"",
	)
	if got := labels[daytonaSandboxLabelOrgID]; got != "org_trim" {
		t.Fatalf("org id label = %q, want %q", got, "org_trim")
	}
}

func TestDaytonaSandboxLabelsOmitsBlankFields(t *testing.T) {
	labels := daytonaSandboxLabels(
		Config{Env: "dev"},
		orgcfg.Config{OrgID: "   "},
		"   ",
	)
	if _, ok := labels[daytonaSandboxLabelOrgID]; ok {
		t.Fatalf("blank org id should not produce a label, got %+v", labels)
	}
	if _, ok := labels[daytonaSandboxLabelCacheVolumeID]; ok {
		t.Fatalf("blank cache volume id should not produce a label, got %+v", labels)
	}
	if got := labels[daytonaSandboxLabelEnv]; got != "dev" {
		t.Fatalf("env label = %q, want dev", got)
	}
}

func TestDaytonaSandboxLabelsAlwaysSetsEnv(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"", "prod"},
		{"dev", "dev"},
		{"development", "dev"},
		{"staging", "stg"},
		{"prod", "prod"},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			labels := daytonaSandboxLabels(Config{Env: tc.raw}, orgcfg.Config{}, "")
			if got := labels[daytonaSandboxLabelEnv]; got != tc.want {
				t.Fatalf("env label for %q = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
