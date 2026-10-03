package gen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
