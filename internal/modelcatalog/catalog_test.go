package modelcatalog

import (
	"strings"
	"testing"
)

func TestCatalogLoadsAndIndexes(t *testing.T) {
	if Version() == "" {
		t.Fatal("Version is empty")
	}
	endpoints := EndpointContracts()
	if len(endpoints) < 4 {
		t.Fatalf("Endpoints = %d, want the four first-batch contracts", len(endpoints))
	}
	for _, want := range []string{"anthropic", "codex", "gemini", "openai"} {
		if _, ok := EndpointContract(want); !ok {
			t.Fatalf("Endpoint(%q) missing", want)
		}
		if !IsManagedPreset(want) {
			t.Fatalf("IsManagedPreset(%q) = false", want)
		}
	}
	if _, ok := EndpointContract("azure"); ok {
		t.Fatal("Endpoint(azure) reported present; only verified endpoints may ship")
	}
	if IsManagedPreset("unknown-preset") {
		t.Fatal("IsManagedPreset(unknown-preset) = true")
	}
}

func TestEndpointContracts(t *testing.T) {
	openai, ok := EndpointContract("openai")
	if !ok {
		t.Fatal("Endpoint(openai) missing")
	}
	if openai.Protocol != "responses" || openai.RequestURL != "https://api.openai.com/v1/responses" ||
		openai.AuthMethod != "bearer" || openai.EnvVar != "OPENAI_API_KEY" {
		t.Fatalf("openai contract = %+v", openai)
	}
	gemini, _ := EndpointContract("gemini")
	if gemini.AuthMethod != "x-goog-api-key" || gemini.RequestURL != "https://generativelanguage.googleapis.com/v1beta/" {
		t.Fatalf("gemini contract = %+v, want the generate-content endpoint shape", gemini)
	}
	codex, _ := EndpointContract("codex")
	if codex.AuthMethod != "oauth" || codex.EnvVar != "" {
		t.Fatalf("codex contract = %+v, want OAuth without an env default", codex)
	}
}

func TestModelFactsAndBindings(t *testing.T) {
	c := effective()
	if len(c.Models) == 0 {
		t.Fatal("embedded catalog has no model facts")
	}
	modelIDs := make(map[string]bool, len(c.Models))
	for _, m := range c.Models {
		if m.ID == "" || modelIDs[m.ID] {
			t.Errorf("duplicate or empty model ID %q", m.ID)
			continue
		}
		modelIDs[m.ID] = true
		if m.Context <= 0 || m.Output <= 0 || m.Input < 0 || m.Input > m.Context {
			t.Errorf("invalid token budgets for %s: %+v", m.ID, m)
		}
		if len(m.Sources) == 0 {
			t.Errorf("%s has no verification sources", m.ID)
		}
		for _, s := range m.Sources {
			if !strings.HasPrefix(s.URL, "https://") || s.Checked == "" {
				t.Errorf("source %+v for %s, want an https URL and a check date", s, m.ID)
			}
		}
		if m.Cost != nil && (m.Cost.InputPerMillion <= 0 || m.Cost.OutputPerMillion <= 0) {
			t.Errorf("%s records non-positive pricing", m.ID)
		}
	}

	endpoints := EndpointContracts()
	if len(endpoints) == 0 {
		t.Fatal("embedded catalog has no endpoints")
	}
	wireByPreset := make(map[string]map[string]bool, len(endpoints))
	total := 0
	for _, e := range endpoints {
		bindings := BindingsForEndpoint(e.PresetID)
		wires := make(map[string]bool, len(bindings))
		for i, b := range bindings {
			if b.WireModelID == "" || b.ModelID == "" {
				t.Errorf("%s has an incomplete binding: %+v", e.PresetID, b)
				continue
			}
			if !modelIDs[b.ModelID] {
				t.Errorf("%s binding %s references unknown model %s", e.PresetID, b.WireModelID, b.ModelID)
			}
			if wires[b.WireModelID] {
				t.Errorf("%s has duplicate wire model %s", e.PresetID, b.WireModelID)
			}
			wires[b.WireModelID] = true
			if i > 0 && bindings[i-1].WireModelID > b.WireModelID {
				t.Errorf("BindingsForEndpoint(%s) is not sorted by wire model ID", e.PresetID)
			}
			got, ok := LookupBinding(e.PresetID, b.WireModelID)
			if !ok || got.ModelID != b.ModelID {
				t.Errorf("LookupBinding(%s, %s) = (%+v, %v)", e.PresetID, b.WireModelID, got, ok)
			}
			total++
		}
		wireByPreset[e.PresetID] = wires
	}
	if total == 0 {
		t.Fatal("embedded catalog has no bindings")
	}
	for a, aWires := range wireByPreset {
		for b, bWires := range wireByPreset {
			if a == b {
				continue
			}
			for wire := range aWires {
				if bWires[wire] {
					continue
				}
				if _, ok := LookupBinding(b, wire); ok {
					t.Errorf("LookupBinding(%s, %s) crossed endpoints from %s", b, wire, a)
				}
			}
		}
	}
}

func TestLoadCatalogRejectsInvalidData(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"duplicate endpoint", `{"version":"v","endpoints":[{"preset_id":"a","protocol":"responses","request_url":"https://x.invalid/v1","auth_method":"bearer","env_var":"A"},{"preset_id":"a","protocol":"responses","request_url":"https://y.invalid/v1","auth_method":"bearer","env_var":"A"}]}`},
		{"unknown field", `{"version":"v","bogus":true}`},
		{"bad protocol", `{"version":"v","endpoints":[{"preset_id":"a","protocol":"nope","request_url":"https://x.invalid/v1","auth_method":"bearer","env_var":"A"}]}`},
		{"Gemini resource instead of version root", `{"version":"v","endpoints":[{"preset_id":"gemini","protocol":"generate-content","request_url":"https://example.invalid/v1beta/models","auth_method":"x-goog-api-key","env_var":"GEMINI_API_KEY"}]}`},
		{"http url", `{"version":"v","endpoints":[{"preset_id":"a","protocol":"responses","request_url":"http://x.invalid/v1","auth_method":"bearer","env_var":"A"}]}`},
		{"model without source", `{"version":"v","models":[{"id":"m","context":100,"output":10}]}`},
		{"input exceeds context", `{"version":"v","models":[{"id":"m","context":100,"input":200,"output":10,"sources":[{"url":"https://x.invalid","checked":"2026-10-01"}]}]}`},
		{"binding to unknown model", `{"version":"v","endpoints":[{"preset_id":"a","protocol":"responses","request_url":"https://x.invalid/v1","auth_method":"bearer","env_var":"A"}],"models":[{"id":"m","context":100,"output":10,"sources":[{"url":"https://x.invalid","checked":"2026-10-01"}]}],"bindings":[{"endpoint":"a","wire_model_id":"w","model_id":"other"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadCatalog([]byte(tt.data)); err == nil {
				t.Fatalf("loadCatalog accepted invalid data: %s", tt.data)
			}
		})
	}
}
