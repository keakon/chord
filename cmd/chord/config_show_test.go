package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestSensitiveShowKey(t *testing.T) {
	sensitive := []string{"api_key", "apiKey", "client_secret", "password", "authorization", "token", "refresh", "access", "Key"}
	for _, key := range sensitive {
		if !sensitiveShowKey(key) {
			t.Fatalf("sensitiveShowKey(%q) = false, want true", key)
		}
	}
	plain := []string{"token_url", "key_rotation", "key_order", "api_url", "type", "preset"}
	for _, key := range plain {
		if sensitiveShowKey(key) {
			t.Fatalf("sensitiveShowKey(%q) = true, want false", key)
		}
	}
}

func TestRedactShowConfigValue(t *testing.T) {
	m := map[string]any{
		"providers": map[string]any{
			"sample": map[string]any{
				"api_url":   "https://example.invalid/v1/responses?api-key=abc123&x=1",
				"token_url": "https://example.invalid/token",
				"api_key":   "sk-value",
				"models": map[string]any{
					"m": map[string]any{
						"compat": map[string]any{
							"request_overrides": map[string]any{
								"headers": map[string]any{
									"Authorization": "Bearer abc",
									"X-Plain":       "keep-me-shape",
								},
							},
						},
					},
				},
			},
		},
	}
	redactShowConfigValue(m, nil)

	provider := m["providers"].(map[string]any)["sample"].(map[string]any)
	if got := provider["api_key"]; got != "[redacted]" {
		t.Fatalf("api_key = %v, want redacted", got)
	}
	if got := provider["api_url"].(string); strings.Contains(got, "abc123") || !strings.Contains(got, "x=1") {
		t.Fatalf("api_url = %q, want the credential query param redacted and the rest intact", got)
	}
	if got := provider["token_url"].(string); got != "https://example.invalid/token" {
		t.Fatalf("token_url = %q, want unchanged", got)
	}
	headers := provider["models"].(map[string]any)["m"].(map[string]any)["compat"].(map[string]any)["request_overrides"].(map[string]any)["headers"].(map[string]any)
	if got := headers["Authorization"]; got != "[redacted]" {
		t.Fatalf("header Authorization = %v, want redacted", got)
	}
	if got := headers["X-Plain"]; got != "[redacted]" {
		t.Fatalf("header X-Plain = %v, want conservatively redacted", got)
	}
}

func TestFilterShowMap(t *testing.T) {
	m := map[string]any{
		"providers": map[string]any{
			"sample": map[string]any{
				"preset": "sample",
			},
		},
	}
	sub, err := filterShowMap(m, "providers.sample")
	if err != nil {
		t.Fatalf("filterShowMap: %v", err)
	}
	if _, ok := sub["preset"]; !ok {
		t.Fatalf("filterShowMap = %+v, want the sample subtree", sub)
	}
	leaf, err := filterShowMap(m, "providers.sample.preset")
	if err != nil {
		t.Fatalf("filterShowMap: %v", err)
	}
	if leaf["preset"] != "sample" {
		t.Fatalf("filterShowMap leaf = %+v, want the wrapped value", leaf)
	}
	if _, err := filterShowMap(m, "providers.absent"); err == nil {
		t.Fatal("filterShowMap: want an error for a missing path")
	}
}

func TestFilterShowBudgetsProviderPath(t *testing.T) {
	budgets := []configShowBudget{
		{Provider: "sample", Model: "model-1", Input: 10},
		{Provider: "other", Model: "model-2", Input: 20},
	}
	for _, path := range []string{"providers.sample", "sample", "providers.sample.models"} {
		got := filterShowBudgets(budgets, path)
		if len(got) != 1 || got[0].Provider != "sample" {
			t.Fatalf("filterShowBudgets(%q) = %+v, want sample budget", path, got)
		}
	}
}

func TestConfigShowOriginsSorted(t *testing.T) {
	idx, err := config.BuildSourceIndex(
		config.ConfigLayer{Layer: config.OriginLayerGlobal, File: "global.yaml", Data: []byte(`providers:
  sample:
    preset: sample
    models:
      m1:
        reasoning:
          effort: low
model_pools:
  default:
    - sample/m1
`)},
	)
	if err != nil {
		t.Fatalf("BuildSourceIndex: %v", err)
	}
	origins := configShowOrigins(idx)
	if len(origins) == 0 {
		t.Fatal("configShowOrigins = empty, want tracked entries")
	}
	for i := 1; i < len(origins); i++ {
		if origins[i-1].Path > origins[i].Path {
			t.Fatalf("origins not sorted: %q after %q", origins[i].Path, origins[i-1].Path)
		}
	}
	var nullNote string
	for _, o := range origins {
		if strings.HasSuffix(o.Path, ".reasoning") && o.Note != "" {
			nullNote = o.Note
		}
	}
	if nullNote != "" {
		t.Fatalf("mapping block got a null note %q", nullNote)
	}
	var poolNote string
	for _, o := range origins {
		if o.Path == "model_pools.default" {
			poolNote = o.Note
		}
	}
	if poolNote != "[0] sample/m1" {
		t.Fatalf("pool note = %q, want the indexed ref", poolNote)
	}
}

func TestRenderConfigShowResult(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.yaml")
	project := filepath.Join(dir, "project.yaml")
	writeFile := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	writeFile(global, `providers:
  sample:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      m1:
        limit:
          context: 100000
          output: 20000
model_pools:
  default:
    - sample/m1
    - sample/gone
`)
	writeFile(project, `providers:
  sample:
    models:
      m1:
        limit:
          output: 24000
`)

	rc, err := config.LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}

	var buf bytes.Buffer
	if err := renderConfigShowResult(&buf, configShowOptions{}, rc); err != nil {
		t.Fatalf("renderConfigShowResult: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"Effective config (project merged over global):",
		"output: 24000",
		"Origins (tracked declarations",
		"model_pools.default",
		"[1] sample/gone",
		"Model budgets:",
		"input=0 (derived from context/output)",
		// The project layer overrides output to 24000, so the derived input
		// budget follows the final context/output pair: 100000 - 24000.
		"request input budget=76000",
		"Diagnostics:",
		"[error]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered text missing %q:\n%s", want, text)
		}
	}

	buf.Reset()
	if err := renderConfigShowResult(&buf, configShowOptions{Path: "providers.sample.models.m1"}, rc); err != nil {
		t.Fatalf("renderConfigShowResult(path): %v", err)
	}
	if !strings.Contains(buf.String(), "output: 24000") || strings.Contains(buf.String(), "model_pools") {
		t.Fatalf("path-filtered render leaked other sections:\n%s", buf.String())
	}

	buf.Reset()
	if err := renderConfigShowResult(&buf, configShowOptions{JSON: true}, rc); err != nil {
		t.Fatalf("renderConfigShowResult(json): %v", err)
	}
	jsonText := buf.String()
	for _, want := range []string{`"config"`, `"origins"`, `"model_budgets"`, `"diagnostics"`, `"input_budget": 76000`} {
		if !strings.Contains(jsonText, want) {
			t.Fatalf("JSON report missing %q:\n%s", want, jsonText)
		}
	}
}

func TestConfigShowExplainsResponsesValuesAndEmission(t *testing.T) {
	rc := writeCatalogShowConfig(t, `providers:
  sample:
    preset: openai
    compat:
      responses:
        send_parallel_tool_calls: false
    models:
      alias:
        catalog: openai/gpt-6.1-sol
        parallel_tool_calls: false
        store: true
model_pools:
  default: [sample/alias]
  secondary: [sample/missing]
`, "")
	var buf bytes.Buffer
	if err := renderConfigShowResult(&buf, configShowOptions{JSON: true}, rc); err != nil {
		t.Fatal(err)
	}
	var report configShowReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("show ok should reflect error diagnostics")
	}
	if len(report.RequestSettings) != 1 {
		t.Fatalf("request settings = %+v", report.RequestSettings)
	}
	settings := report.RequestSettings[0]
	if !settings.Store || !settings.SendFields["send_store"] || settings.ParallelToolCalls || settings.SendFields["send_parallel_tool_calls"] {
		t.Fatalf("request settings = %+v", settings)
	}
	if settings.Sources["send_store"] != "catalog" || settings.Sources["send_parallel_tool_calls"] != "provider" || settings.Sources["store"] != "model" {
		t.Fatalf("sources = %+v", settings.Sources)
	}
}

func TestConfigShowRedactsProxyUserinfoAndDiagnosticURLs(t *testing.T) {
	raw := "http://sample-user:sample-password@proxy.example.invalid:8080?api_key=sample-key&region=test"
	redacted := redactShowURL(raw)
	for _, secret := range []string{"sample-user", "sample-password", "sample-key"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("URL leaked %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "proxy.example.invalid:8080") || !strings.Contains(redacted, "region=test") {
		t.Fatalf("endpoint lost: %s", redacted)
	}
	values := map[string]any{"proxy": raw}
	redactShowConfigValue(values, nil)
	if values["proxy"] != redacted {
		t.Fatalf("proxy not redacted: %+v", values)
	}
	diagnostic := redactShowDiagnostic(config.Diagnostic{Message: "invalid proxy " + raw})
	if strings.Contains(diagnostic.Message, "sample-password") {
		t.Fatalf("diagnostic leaked: %+v", diagnostic)
	}
	if strings.Contains(redactShowURL("https://example.invalid?api_key=sample-key;invalid"), "sample-key") {
		t.Fatal("malformed query leaked")
	}
}
