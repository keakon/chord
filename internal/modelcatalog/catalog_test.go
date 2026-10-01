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
	if gemini.AuthMethod != "x-goog-api-key" || !strings.HasSuffix(gemini.RequestURL, "/models") {
		t.Fatalf("gemini contract = %+v, want the generate-content endpoint shape", gemini)
	}
	codex, _ := EndpointContract("codex")
	if codex.AuthMethod != "oauth" || codex.EnvVar != "" {
		t.Fatalf("codex contract = %+v, want OAuth without an env default", codex)
	}
}

func TestModelFactsAndBindings(t *testing.T) {
	m, ok := Model("openai/gpt-6.1-sol")
	if !ok {
		t.Fatal("Model(openai/gpt-6.1-sol) missing")
	}
	if m.Context != 1050000 || m.Input != 922000 || m.Output != 128000 {
		t.Fatalf("gpt-6.1-sol facts = %+v", m)
	}
	if m.Cost != nil {
		t.Fatal("unverified pricing must stay absent, not zero")
	}
	for _, s := range m.Sources {
		if !strings.HasPrefix(s.URL, "https://") || s.Checked == "" {
			t.Fatalf("source %+v, want an https URL and a check date", s)
		}
	}

	b, ok := LookupBinding("codex", "gpt-6.1-sol")
	if !ok {
		t.Fatal("LookupBinding(codex, gpt-6.1-sol) missing")
	}
	if b.ModelID != "openai/gpt-6.1-sol" {
		t.Fatalf("binding = %+v", b)
	}
	if _, ok := b.Variants["high"]; !ok {
		t.Fatal("codex gpt-6.1-sol is missing the verified high tier")
	}
	if _, ok := LookupBinding("openai", "claude-sonnet-5-5"); ok {
		t.Fatal("cross-endpoint lookup must not resolve")
	}
	bindings := BindingsForEndpoint("codex")
	if len(bindings) != 7 {
		t.Fatalf("codex bindings = %d, want the seven wizard models", len(bindings))
	}
	for i := 1; i < len(bindings); i++ {
		if bindings[i-1].WireModelID > bindings[i].WireModelID {
			t.Fatal("BindingsForEndpoint is not sorted by wire model ID")
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
