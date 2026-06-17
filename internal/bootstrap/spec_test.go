package bootstrap

import (
	"encoding/json"
	"testing"
)

func TestParseManifest(t *testing.T) {
	t.Run("empty input is rejected", func(t *testing.T) {
		_, err := ParseManifest(nil)
		if err == nil {
			t.Fatal("expected error for nil input")
		}
		_, err = ParseManifest([]byte(""))
		if err == nil {
			t.Fatal("expected error for empty input")
		}
	})

	t.Run("malformed JSON wraps error", func(t *testing.T) {
		_, err := ParseManifest([]byte("{not json"))
		if err == nil {
			t.Fatal("expected parse error")
		}
	})

	t.Run("valid manifest decodes fully", func(t *testing.T) {
		raw := []byte(`{
			"kind": "go-web+postgres",
			"services": [
				{"name": "web", "port": 8080, "url": "http://localhost:8080", "kind": "ui"}
			],
			"required_secrets": [
				{"name": "GITHUB_APP_ID", "user_supplied": true, "hint": "App ID from GitHub"}
			],
			"deferred_capabilities": ["Real authentication (AUTH_BYPASS=1)"],
			"suggested_repo_changes": ["Add a make bootstrap target"],
			"validation_capability": {
				"can_run_ui": true,
				"default_url": "http://localhost:8080",
				"test_commands": ["go test ./..."],
				"evidence_required": ["screenshot"]
			}
		}`)
		m, err := ParseManifest(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if m.Kind != "go-web+postgres" {
			t.Errorf("kind: got %q", m.Kind)
		}
		if len(m.Services) != 1 || m.Services[0].Port != 8080 {
			t.Errorf("services: %+v", m.Services)
		}
		if !m.HasDeferred() {
			t.Error("expected HasDeferred=true")
		}
		if !m.ValidationCapability.CanRunUI || m.ValidationCapability.DefaultURL != "http://localhost:8080" || len(m.ValidationCapability.TestCommands) != 1 {
			t.Errorf("validation capability: %+v", m.ValidationCapability)
		}
	})

	t.Run("HasDeferred is false when empty", func(t *testing.T) {
		m, err := ParseManifest([]byte(`{"kind": "cli", "services": [], "required_secrets": []}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if m.HasDeferred() {
			t.Error("expected HasDeferred=false for empty list")
		}
	})
}

// TestSpecJSONShape locks down the wire format the Manifest exposes —
// the agent writes JSON matching this shape into /tmp/hetchy-spec/
// and the host parses it back. If anything here drifts, the bootstrap
// loop will silently misread the agent's output.
func TestSpecJSONShape(t *testing.T) {
	m := &Manifest{
		Kind:            "rails+pg",
		Services:        []Service{{Name: "web", Port: 3000, URL: "http://localhost:3000", Kind: "ui"}},
		RequiredSecrets: []Secret{{Name: "STRIPE_SECRET_KEY", UserSupplied: true, Hint: "Stripe test key"}},
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Round-trip and check the field names landed where the agent
	// expects them. We don't compare full strings because Go's JSON
	// output is deterministic but verbose; a key check is enough.
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"kind", "services", "required_secrets"} {
		if _, ok := back[key]; !ok {
			t.Errorf("missing key %q in encoded manifest: %s", key, string(encoded))
		}
	}
	// deferred_capabilities and suggested_repo_changes have omitempty —
	// they should NOT appear when empty.
	if _, ok := back["deferred_capabilities"]; ok {
		t.Errorf("expected deferred_capabilities to be omitted when empty: %s", string(encoded))
	}
}

func TestNeedsGenerationUpgrade(t *testing.T) {
	t.Run("nil spec returns false", func(t *testing.T) {
		if NeedsGenerationUpgrade(nil) {
			t.Error("NeedsGenerationUpgrade(nil) should return false")
		}
	})
	t.Run("zero generation treated as old", func(t *testing.T) {
		spec := &Spec{BootstrapGeneration: 0}
		if !NeedsGenerationUpgrade(spec) {
			t.Error("NeedsGenerationUpgrade(gen=0) should return true (zero is treated as old)")
		}
	})
	t.Run("generation below current needs upgrade", func(t *testing.T) {
		spec := &Spec{BootstrapGeneration: CurrentBootstrapGeneration - 1}
		if !NeedsGenerationUpgrade(spec) {
			t.Errorf("NeedsGenerationUpgrade(gen=%d) should return true when current=%d",
				spec.BootstrapGeneration, CurrentBootstrapGeneration)
		}
	})
	t.Run("current generation does not need upgrade", func(t *testing.T) {
		spec := &Spec{BootstrapGeneration: CurrentBootstrapGeneration}
		if NeedsGenerationUpgrade(spec) {
			t.Errorf("NeedsGenerationUpgrade(gen=%d) should return false when at current=%d",
				spec.BootstrapGeneration, CurrentBootstrapGeneration)
		}
	})
}
