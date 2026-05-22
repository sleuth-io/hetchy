package bot

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

func TestWorkOSWebhookHandlerMissingSecret(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want int
	}{
		{name: "dev noops", env: "dev", want: http.StatusOK},
		{name: "prod rejects", env: "prod", want: http.StatusServiceUnavailable},
		{name: "default rejects", env: "", want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bot{cfg: Config{Env: tc.env}, log: discardLogger()}
			req := httptest.NewRequest(http.MethodPost, "/workos/webhook", strings.NewReader(`{}`))
			rr := httptest.NewRecorder()

			b.workOSWebhookHandler(rr, req)

			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestWorkOSFeatureFlagEventOrgIDsUsesCurrentAndPreviousTargets(t *testing.T) {
	event := &workos.EventSchema{
		Context: map[string]any{
			"configured_targets": map[string]any{
				"organizations": []any{
					map[string]any{"id": "org_b"},
					map[string]any{"id": "org_a"},
				},
			},
			"previous_attributes": map[string]any{
				"context": map[string]any{
					"configured_targets": map[string]any{
						"organizations": []any{
							map[string]any{"id": "org_c"},
							map[string]any{"id": "org_a"},
						},
					},
				},
			},
		},
	}

	got := workOSFeatureFlagEventOrgIDs(event)
	want := []string{"org_a", "org_b", "org_c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workOSFeatureFlagEventOrgIDs = %#v, want %#v", got, want)
	}
}

func TestWorkOSFeatureFlagEventOrgIDsIgnoresMalformedTargets(t *testing.T) {
	event := &workos.EventSchema{
		Context: map[string]any{
			"configured_targets": map[string]any{
				"organizations": []any{
					map[string]any{"id": ""},
					map[string]any{"name": "Missing ID"},
					"bad",
				},
			},
		},
	}

	if got := workOSFeatureFlagEventOrgIDs(event); len(got) != 0 {
		t.Fatalf("workOSFeatureFlagEventOrgIDs = %#v, want empty", got)
	}
}
