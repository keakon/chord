package gen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

const (
	validCatalog = `version: "2026-10-01.1"
`

	validEndpoints = `endpoints:
  - preset_id: sample
    protocol: responses
    request_url: https://example.invalid/v1/responses
    auth_method: bearer
    env_var: SAMPLE_API_KEY
    docs:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`

	validModels = `models:
  - id: sample/test-model
    context: 128000
    output: 32768
    reasoning_options: []
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`

	validBindings = `bindings:
  - endpoint: sample
    wire_model_id: test-model
    model_id: sample/test-model
`
)

func writeSources(t *testing.T, catalog, endpoints, models, bindings string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		fileNameCatalog:   catalog,
		fileNameEndpoints: endpoints,
		fileNameModels:    models,
		fileNameBindings:  bindings,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func validSourceSet(t *testing.T) string {
	t.Helper()
	return writeSources(t, validCatalog, validEndpoints, validModels, validBindings)
}

func TestGenerateValidSources(t *testing.T) {
	data, err := Generate(validSourceSet(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("generated artifact is not valid JSON: %v", err)
	}
	if got := decoded["version"]; got != "2026-10-01.1" {
		t.Errorf("version = %v, want 2026-10-01.1", got)
	}
	endpoints, ok := decoded["endpoints"].([]any)
	if !ok || len(endpoints) != 1 {
		t.Fatalf("endpoints = %#v, want one entry", decoded["endpoints"])
	}
}

func TestGenerateRejectsGlobalCompactionFields(t *testing.T) {
	for key, value := range map[string]string{
		"profile":              "auto",
		"reserved":             "1000",
		"retain_recent_tokens": "1000",
		"model_driven":         "true",
	} {
		t.Run(key, func(t *testing.T) {
			models := validModels + "    config_profile:\n      compaction:\n        " + key + ": " + value + "\n"
			_, err := Generate(writeSources(t, validCatalog, validEndpoints, models, validBindings))
			if err == nil || !strings.Contains(err.Error(), "field "+key+" not found") {
				t.Fatalf("expected unsupported compaction field error, got %v", err)
			}
		})
	}
}

func TestGenerateSortsEntriesCanonically(t *testing.T) {
	models := `models:
  - id: sample/zeta
    context: 128000
    output: 32768
    reasoning_options: []
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
  - id: sample/alpha
    context: 128000
    output: 32768
    reasoning_options: []
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`
	bindings := `bindings:
  - endpoint: sample
    wire_model_id: alpha
    model_id: sample/alpha
`
	data, err := Generate(writeSources(t, validCatalog, validEndpoints, models, bindings))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var decoded struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Models) != 2 || decoded.Models[0].ID != "sample/alpha" || decoded.Models[1].ID != "sample/zeta" {
		t.Fatalf("models not sorted by id: %#v", decoded.Models)
	}
}

func TestGenerateRejectsInvalidSources(t *testing.T) {
	tests := []struct {
		name     string
		catalog  string
		endpoint string
		models   string
		bindings string
		wantErr  string
	}{
		{
			name: "duplicate model id",
			models: `models:
  - id: sample/test-model
    context: 128000
    output: 32768
    reasoning_options: []
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
  - id: sample/test-model
    context: 64000
    output: 16384
    reasoning_options: []
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`,
			wantErr: "duplicate id",
		},
		{
			name: "binding references unknown model",
			bindings: `bindings:
  - endpoint: sample
    wire_model_id: test-model
    model_id: sample/missing
`,
			wantErr: "unknown model",
		},
		{
			name: "binding references unknown endpoint",
			bindings: `bindings:
  - endpoint: other
    wire_model_id: test-model
    model_id: sample/test-model
`,
			wantErr: "unknown endpoint",
		},
		{
			name: "model source URL not https",
			models: `models:
  - id: sample/test-model
    context: 128000
    output: 32768
    reasoning_options: []
    sources:
      - url: http://example.invalid/docs
        checked: "2026-10-01"
`,
			wantErr: "must be https",
		},
		{
			name: "endpoint doc date not a calendar day",
			endpoint: `endpoints:
  - preset_id: sample
    protocol: responses
    request_url: https://example.invalid/v1/responses
    auth_method: bearer
    env_var: SAMPLE_API_KEY
    docs:
      - url: https://example.invalid/docs
        checked: "2026-13-45"
`,
			wantErr: "YYYY-MM-DD",
		},
		{
			name: "zero cost encoded",
			models: `models:
  - id: sample/test-model
    context: 128000
    output: 32768
    reasoning_options: []
    cost:
      input_per_million: 0
      output_per_million: 5
      currency: USD
      checked: "2026-10-01"
      source:
        url: https://example.invalid/pricing
        checked: "2026-10-01"
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`,
			wantErr: "must be positive",
		},
		{
			name: "cost without currency",
			models: `models:
  - id: sample/test-model
    context: 128000
    output: 32768
    reasoning_options: []
    cost:
      input_per_million: 1
      output_per_million: 5
      currency: ""
      checked: "2026-10-01"
      source:
        url: https://example.invalid/pricing
        checked: "2026-10-01"
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`,
			wantErr: "currency is required",
		},
		{
			name: "unknown field in models source",
			models: `models:
  - id: sample/test-model
    context: 128000
    output: 32768
    reasoning_options: []
    tier: flagship
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`,
			wantErr: "field tier not found",
		},
		{
			name: "input exceeds context",
			models: `models:
  - id: sample/test-model
    context: 128000
    input: 200000
    output: 32768
    reasoning_options: []
    sources:
      - url: https://example.invalid/docs
        checked: "2026-10-01"
`,
			wantErr: "exceeds context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog, endpoints, models, bindings := tt.catalog, tt.endpoint, tt.models, tt.bindings
			if catalog == "" {
				catalog = validCatalog
			}
			if endpoints == "" {
				endpoints = validEndpoints
			}
			if models == "" {
				models = validModels
			}
			if bindings == "" {
				bindings = validBindings
			}
			_, err := Generate(writeSources(t, catalog, endpoints, models, bindings))
			if err == nil {
				t.Fatalf("Generate succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestGenerateMetadataAndBindingOverrides(t *testing.T) {
	models := strings.Replace(validModels, "    context: 128000", `    released: "2026-04-28"
    coding_sources:
      - url: https://example.invalid/coding
        checked: "2026-10-04"
    connection:
      request_url: https://example.invalid/v1/chat/completions
      wire_model_id: test-model
      env_var: SAMPLE_API_KEY
      sources:
        - url: https://example.invalid/connect
          checked: "2026-10-04"
    context: 128000`, 1)
	bindings := validBindings + "    limit:\n      context: 64000\n      input: 0\n      output: 16000\n    input_modalities: [text]\n"
	dir := writeSources(t, validCatalog, validEndpoints, models, bindings)
	data, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	var catalog modelcatalog.Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	m := catalog.Models[0]
	got := catalog.Bindings[0].ApplyTo(m)
	if m.Connection == nil || m.Connection.EnvVar != "SAMPLE_API_KEY" || m.Released != "2026-04-28" || len(m.CodingSources) != 1 || got.Context != 64000 || got.Output != 16000 || got.Input != 0 {
		t.Fatalf("round trip lost metadata or limits: %+v, %+v", m, got)
	}
	for _, bad := range []string{"released: 2026-02-30", "released: yesterday"} {
		invalid := strings.Replace(models, `released: "2026-04-28"`, bad, 1)
		if _, err := Generate(writeSources(t, validCatalog, validEndpoints, invalid, bindings)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestGenerateConfigProfileRoundTrip(t *testing.T) {
	models := strings.Replace(validModels, "    context: 128000", `    config_profile:
      model:
        prompt_cache:
          mode: auto
      compat:
        reasoning_continuity:
          reasoning_replay: all
      compaction:
        threshold: 0.4
        reminder: 0.3
        notes: keep the recent task boundary
      sources:
        - url: https://example.invalid/profile
          checked: "2026-10-04"
    context: 128000`, 1)
	data, err := Generate(writeSources(t, validCatalog, validEndpoints, models, validBindings))
	if err != nil {
		t.Fatal(err)
	}
	var c modelcatalog.Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	profile := c.Models[0].Profile
	if profile == nil || profile.Model["prompt_cache"] == nil || profile.Compaction == nil || profile.Compaction.Threshold == nil || *profile.Compaction.Threshold != 0.4 {
		t.Fatalf("profile lost: %#v", profile)
	}
}

func TestGenerateServerToolBindingEvidence(t *testing.T) {
	bindings := validBindings + `    server_tools:
      web_search:
        state: unknown
        contract: openai.responses.web_search
        evidence: documentation
        sources:
          - url: https://example.invalid/tools
            checked: "2026-10-09"
`
	data, err := Generate(writeSources(t, validCatalog, validEndpoints, validModels, bindings))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"server_tools"`) || !strings.Contains(string(data), `"unknown"`) {
		t.Fatalf("capability omitted: %s", data)
	}
	bindings = strings.Replace(bindings, "state: unknown", "state: supported", 1)
	if _, err = Generate(writeSources(t, validCatalog, validEndpoints, validModels, bindings)); err == nil {
		t.Fatal("documentation promoted to supported")
	}
	bindings = strings.Replace(bindings, "evidence: documentation", "evidence: api", 1)
	if _, err = Generate(writeSources(t, validCatalog, validEndpoints, validModels, bindings)); err != nil {
		t.Fatal(err)
	}
}
