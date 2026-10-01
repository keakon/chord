package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestModelCatalogRefJSONShape(t *testing.T) {
	data, err := json.Marshal(ModelCatalogRef{ID: "openai/gpt-6.1-sol"})
	if err != nil || string(data) != `"openai/gpt-6.1-sol"` {
		t.Fatalf("marshal catalog ref = %s, err=%v", data, err)
	}
	var disabled ModelCatalogRef
	if err := json.Unmarshal([]byte("false"), &disabled); err != nil || !disabled.Disabled {
		t.Fatalf("unmarshal false = %+v, err=%v", disabled, err)
	}
	if err := json.Unmarshal([]byte("true"), &disabled); err == nil {
		t.Fatal("unmarshal true succeeded, want a union-type error")
	}
}

func writeCatalogTestConfig(t *testing.T, name, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadResolvedConfigMaterializesPoolOnlyModelAndOrigins(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
model_pools:
  default:
    - openai/gpt-6.1-sol
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	model, ok := rc.Config.Providers["openai"].Models["gpt-6.1-sol"]
	if !ok || model.Limit.Context != 1050000 || model.Limit.Output != 128000 {
		t.Fatalf("materialized model = %+v, want catalog limits", model)
	}
	mo, ok := rc.Index.Model("openai", "gpt-6.1-sol")
	if !ok || len(mo.Limit.Context) == 0 || mo.Limit.Context[0].Layer != OriginLayerCatalog {
		t.Fatalf("model origins = %+v, want catalog origin", mo)
	}
	if diags := rc.Diagnostics; len(diags) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diags)
	}
}

func TestCatalogDoesNotCrossLowerNullClear(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        variants: null
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	project := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    models:
      gpt-6.1-sol:
        variants:
          high:
            reasoning:
              effort: high
`)
	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	variants := rc.Config.Providers["openai"].Models["gpt-6.1-sol"].Variants
	if len(variants) != 1 || variants["high"].Reasoning == nil || variants["high"].Reasoning.Effort != "high" {
		t.Fatalf("variants = %+v, want only project-defined high variant", variants)
	}
}

func TestCatalogResponsesCompatUsesBindingAndHonorsDisabled(t *testing.T) {
	compat := CatalogResponsesCompat("openai", "gpt-6.1-sol", ModelConfig{})
	if compat == nil || compat.SendStore == nil || !*compat.SendStore || compat.SendParallelToolCalls == nil || !*compat.SendParallelToolCalls {
		t.Fatalf("catalog Responses compat = %+v, want verified send defaults", compat)
	}
	compat = CatalogResponsesCompat("openai", "internal", ModelConfig{Catalog: &ModelCatalogRef{ID: "openai/gpt-6.1-sol"}})
	if compat == nil || compat.SendStore == nil {
		t.Fatalf("catalog alias Responses compat = %+v, want binding defaults", compat)
	}
	if got := CatalogResponsesCompat("openai", "gpt-6.1-sol", ModelConfig{Catalog: &ModelCatalogRef{Disabled: true}}); got != nil {
		t.Fatalf("disabled catalog Responses compat = %+v, want nil", got)
	}
}

func TestCatalogAliasAndDisabledModel(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      alias:
        catalog: openai/gpt-6.1-sol
      disabled:
        catalog: false
        limit:
          context: 100
          output: 10
model_pools:
  default: [openai/alias, openai/disabled]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	alias := rc.Config.Providers["openai"].Models["alias"]
	if alias.Limit.Context != 1050000 || alias.Limit.Output != 128000 {
		t.Fatalf("alias = %+v, want catalog facts", alias)
	}
	disabled := rc.Config.Providers["openai"].Models["disabled"]
	if disabled.Limit.Context != 100 || disabled.Limit.Output != 10 {
		t.Fatalf("disabled = %+v, want explicit limits only", disabled)
	}
}

func TestLoadResolvedConfigReportsPresetContractError(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    api_url: https://example.invalid/responses
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	if len(rc.Diagnostics) == 0 || rc.Diagnostics[0].Severity != DiagnosticSeverityError {
		t.Fatalf("diagnostics = %+v, want preset contract error", rc.Diagnostics)
	}
}

func TestLoadResolvedConfigExtraRefMaterializesUnconfiguredModel(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
`)
	rc, err := LoadResolvedConfig(global, "", "openai/gpt-6.1-sol")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	if _, ok := rc.Config.Providers["openai"].Models["gpt-6.1-sol"]; !ok {
		t.Fatal("extra model ref was not materialized")
	}
}

func TestCustomEndpointCatalogBorrowsOnlyModelFacts(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  sample:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      alias:
        catalog: openai/gpt-6.1-sol
        limit:
          output: 2048
      cleared:
        catalog: openai/gpt-6.1-sol
        modalities: null
model_pools:
  default: [sample/alias]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", rc.Diagnostics)
	}
	m := rc.Config.Providers["sample"].Models["alias"]
	if m.Limit.Context != 1050000 || m.Limit.Output != 2048 {
		t.Fatalf("model facts = %+v", m)
	}
	if len(m.Variants) != 0 || CatalogResponsesCompat("", "alias", m) != nil {
		t.Fatal("custom endpoint inherited route-specific settings")
	}
	if rc.Config.Providers["sample"].Models["cleared"].Modalities != nil {
		t.Fatal("catalog refilled cleared modalities")
	}
	if _, ok := rc.Index.Model("sample", "alias"); !ok {
		t.Fatal("catalog source missing")
	}
}

func TestCustomEndpointCatalogRejectsUnknownID(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  sample:
    type: responses
    models:
      alias:
        catalog: unknown/model
model_pools:
  default: [sample/alias]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Diagnostics) < 1 || rc.Diagnostics[0].Severity != DiagnosticSeverityError {
		t.Fatalf("diagnostics = %+v", rc.Diagnostics)
	}
	if err := ValidateConfiguredModelRefs(rc.Config.Providers, []string{"sample/alias"}, ""); err == nil {
		t.Fatal("unknown catalog ID remained usable")
	}
}

func TestCatalogSendOverrideDiagnostics(t *testing.T) {
	rejecting := func(_, _ string, _ ModelConfig) *ResponsesCompatConfig {
		return &ResponsesCompatConfig{SendParallelToolCalls: new(false)}
	}
	accepting := func(_, _ string, _ ModelConfig) *ResponsesCompatConfig {
		return &ResponsesCompatConfig{SendParallelToolCalls: new(true)}
	}
	noClaims := func(_, _ string, _ ModelConfig) *ResponsesCompatConfig { return nil }

	build := func(providerSend, modelSend *bool) *Config {
		provider := ProviderConfig{
			Preset: "openai",
			Models: map[string]ModelConfig{"model-1": {Name: "model-1"}},
		}
		if providerSend != nil {
			provider.Compat = &ProviderCompatConfig{Responses: &ResponsesCompatConfig{SendParallelToolCalls: providerSend}}
		}
		if modelSend != nil {
			model := provider.Models["model-1"]
			model.Compat = &ModelCompatConfig{Responses: &ResponsesCompatConfig{SendParallelToolCalls: modelSend}}
			provider.Models["model-1"] = model
		}
		return &Config{Providers: map[string]ProviderConfig{"sample": provider}}
	}

	for _, tc := range []struct {
		name   string
		claims func(string, string, ModelConfig) *ResponsesCompatConfig
		p, m   *bool
		want   string
	}{
		{name: "model override on rejecting binding", claims: rejecting, m: new(true), want: "providers.sample.models.model-1.compat.responses.send_parallel_tool_calls"},
		{name: "provider override on rejecting binding", claims: rejecting, p: new(true), want: "providers.sample.compat.responses.send_parallel_tool_calls"},
		{name: "model false keeps provider true silent", claims: rejecting, p: new(true), m: new(false)},
		{name: "rejecting binding without override", claims: rejecting},
		{name: "accepting binding keeps override", claims: accepting, m: new(true)},
		{name: "binding without claims keeps override", claims: noClaims, m: new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := catalogSendOverrideDiagnostics(build(tc.p, tc.m), tc.claims)
			if tc.want == "" {
				if len(diags) != 0 {
					t.Fatalf("diagnostics = %+v, want none", diags)
				}
				return
			}
			if len(diags) != 1 || diags[0].Path != tc.want || diags[0].Severity != DiagnosticSeverityError || !diags[0].Continues {
				t.Fatalf("diagnostics = %+v, want one continuing error at %s", diags, tc.want)
			}
			if diags[0].Scope != "model sample/model-1" {
				t.Fatalf("scope = %q, want model sample/model-1", diags[0].Scope)
			}
		})
	}
}

func TestLoadResolvedConfigSendOverridesMatchRealBindings(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    compat:
      responses:
        send_store: true
    models:
      disabled:
        catalog: false
        compat:
          responses:
            send_parallel_tool_calls: true
model_pools:
  default: [openai/disabled]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none: verified bindings accept their claimed fields and catalog-disabled models carry no binding claims", rc.Diagnostics)
	}
}
