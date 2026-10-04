package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
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

func TestCatalogProfileAppliesToSameProtocolGateway(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
  gateway:
    type: chat-completions
    api_url: https://gateway.example/v1/chat/completions
    models:
      glm:
        catalog: zhipuai/glm-5.3
        compat:
          forced_tool_choice: {auto_only: true}
model_pools:
  default: [openai/gpt-6.1-sol, gateway/glm]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatal(err)
	}
	exact := rc.Config.Providers["openai"].Models["gpt-6.1-sol"]
	if exact.Reasoning == nil || exact.Reasoning.Summary != "auto" {
		t.Fatalf("exact preset profile = %+v, want documented reasoning defaults", exact.Reasoning)
	}
	custom := rc.Config.Providers["gateway"].Models["glm"]
	if custom.Compat == nil || custom.Compat.ReasoningContinuity == nil || custom.Compat.ReasoningContinuity.ReasoningReplay != ReasoningReplayAll || custom.Compaction == nil || custom.Compat.ForcedToolChoice == nil || custom.Compat.ForcedToolChoice.AutoOnly == nil || !*custom.Compat.ForcedToolChoice.AutoOnly {
		t.Fatalf("gateway did not inherit model recipe: %+v", custom)
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

func TestCustomEndpointCatalogInheritsSameProtocolRecipe(t *testing.T) {
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
	if len(m.Variants) == 0 || m.Reasoning == nil || m.Compat == nil || m.Compat.Responses == nil || m.Compat.Responses.SendParallelToolCalls == nil || !*m.Compat.Responses.SendParallelToolCalls {
		t.Fatalf("custom endpoint missing recipe: %+v", m)
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

func TestCatalogRespectsGlobalCompactionAndNullBlocks(t *testing.T) {
	global := writeCatalogTestConfig(t, "config.yaml", `context:
  compaction:
    threshold: 0
    reminder: -1
providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning: null
        compaction: null
  anthropic:
    preset: anthropic
    models:
      claude-opus-5-5:
        thinking: null
        prompt_cache: null
model_pools:
  default: [openai/gpt-6.1-sol, anthropic/claude-opus-5-5]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatal(err)
	}
	gpt := rc.Config.Providers["openai"].Models["gpt-6.1-sol"]
	if gpt.Reasoning != nil || gpt.Compaction != nil {
		t.Fatalf("null blocks were refilled: %+v", gpt)
	}
	claude := rc.Config.Providers["anthropic"].Models["claude-opus-5-5"]
	if claude.Thinking != nil || claude.PromptCache != nil || claude.Compaction != nil {
		t.Fatalf("explicit settings were replaced: %+v", claude)
	}
	// A pool-only model must also preserve the global disable settings.
	rc, err = LoadResolvedConfig(writeCatalogTestConfig(t, "pool.yaml", `context:
  compaction: {threshold: 0, reminder: -1}
providers:
  openai: {preset: openai}
model_pools:
  default: [openai/gpt-6.1-sol]
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := rc.Config.Providers["openai"].Models["gpt-6.1-sol"].Compaction; got != nil {
		t.Fatalf("global disable overridden: %+v", got)
	}
}

func TestCatalogProfileOriginsAndSchema(t *testing.T) {
	rc, err := LoadResolvedConfig(writeCatalogTestConfig(t, "config.yaml", `providers:
  openai: {preset: openai}
model_pools:
  default: [openai/gpt-6.1-sol]
`), "")
	if err != nil {
		t.Fatal(err)
	}
	origins, ok := rc.Index.ModelBlock("openai", "gpt-6.1-sol", "reasoning")
	if !ok || len(origins) != 1 || origins[0].Layer != OriginLayerCatalog {
		t.Fatalf("missing profile origin: %+v", origins)
	}
	for _, compat := range []map[string]any{
		{"thinking_toolcall": map[string]any{"mode": "on"}},
		{"thinking_toolcall": map[string]any{"enabled": "true"}},
		{"reasoning_continuity": map[string]any{"reasoning_replay": "invalid"}},
		{"request_overrides": map[string]any{"headers": map[string]any{"X-Sample": 1}}},
	} {
		if _, err := DecodeCatalogProfile(&modelcatalog.ConfigProfile{Compat: compat}); err == nil {
			t.Fatalf("invalid profile accepted: %+v", compat)
		}
	}
	profile := &modelcatalog.ConfigProfile{Compat: map[string]any{
		"thinking_toolcall": map[string]any{"enabled": true},
		"request_overrides": map[string]any{"headers": map[string]any{"X-Sample": "value", "X-Remove": nil}, "body": map[string]any{"nested": map[string]any{"value": true}}},
	}}
	if _, err := DecodeCatalogProfile(profile); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogModelThresholdKeepsReminderDerivation(t *testing.T) {
	for _, threshold := range []float64{0, 0.4, 0.8} {
		global := writeCatalogTestConfig(t, "config.yaml", fmt.Sprintf(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        compaction: {threshold: %v}
model_pools:
  default: [openai/gpt-6.1-sol]
`, threshold))
		rc, err := LoadResolvedConfig(global, "")
		if err != nil {
			t.Fatal(err)
		}
		compaction := rc.Config.Providers["openai"].Models["gpt-6.1-sol"].Compaction
		if compaction == nil || compaction.Threshold == nil || *compaction.Threshold != threshold || compaction.Reminder != nil {
			t.Fatalf("threshold %v: compaction = %+v, want explicit threshold and derived reminder", threshold, compaction)
		}
	}
}

func TestGatewayCatalogKeepsOverridesAndNullBlocks(t *testing.T) {
	rc, err := LoadResolvedConfig(writeCatalogTestConfig(t, "config.yaml", `context:
  compaction: {threshold: 0, reminder: -1}
providers:
  sample:
    type: chat-completions
    api_url: https://example.invalid/v1/chat/completions
    compress: gzip
    compat:
      reasoning_continuity: {reasoning_replay: current_turn}
    models:
      inherited:
        catalog: moonshotai/kimi-k3
      overridden:
        catalog: moonshotai/kimi-k3
        compat:
          reasoning_continuity: {reasoning_replay: none}
      cleared:
        catalog: moonshotai/kimi-k3
        compat: null
        thinking: null
        compaction: null
model_pools:
  default: [sample/inherited, sample/overridden, sample/cleared]
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Diagnostics) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", rc.Diagnostics)
	}
	p := rc.Config.Providers["sample"]
	if p.Compress != "gzip" {
		t.Fatal("explicit compression changed")
	}
	inherited := p.Models["inherited"]
	if inherited.Compat == nil || inherited.Compat.ChatCompletions == nil || inherited.Compat.ReasoningContinuity != nil {
		t.Fatalf("catalog must keep native thinking but defer replay to provider: %+v", inherited.Compat)
	}
	if overridden := p.Models["overridden"]; overridden.Compat.ReasoningContinuity.ReasoningReplay != ReasoningReplayNone {
		t.Fatal("model override lost")
	}
	if cleared := p.Models["cleared"]; cleared.Compat != nil || cleared.Thinking != nil || cleared.Compaction != nil {
		t.Fatalf("explicit null was refilled: %+v", cleared)
	}
	origins, ok := rc.Index.ModelBlock("sample", "inherited", "compat")
	if !ok || len(origins) == 0 || origins[0].Layer != OriginLayerCatalog {
		t.Fatalf("missing catalog origin: %+v", origins)
	}
}

func TestGatewayCatalogDifferentProtocolKeepsOnlyIndependentGuidance(t *testing.T) {
	rc, err := LoadResolvedConfig(writeCatalogTestConfig(t, "config.yaml", `providers:
  sample:
    type: chat-completions
    api_url: https://example.invalid/v1/chat/completions
    models:
      claude:
        catalog: anthropic/claude-opus-5-5
model_pools:
  default: [sample/claude]
`), "")
	if err != nil {
		t.Fatal(err)
	}
	m := rc.Config.Providers["sample"].Models["claude"]
	if m.Thinking != nil || m.PromptCache != nil || m.Compat != nil || len(m.Variants) > 0 {
		t.Fatalf("different protocol inherited native wire recipe: %+v", m)
	}
	if m.Compaction == nil || m.Compaction.Threshold == nil || *m.Compaction.Threshold != 0.7 {
		t.Fatalf("independent compaction strategy missing: %+v", m.Compaction)
	}
}

func TestGatewayResponsesDefaultsRespectExplicitFalse(t *testing.T) {
	rc, err := LoadResolvedConfig(writeCatalogTestConfig(t, "config.yaml", `providers:
  sample:
    type: responses
    api_url: https://example.invalid/v1/responses
    compat:
      responses: {send_parallel_tool_calls: false}
    models:
      inherited:
        catalog: openai/gpt-6.1-sol
      overridden:
        catalog: openai/gpt-6.1-sol
        compat:
          responses: {send_store: false}
model_pools:
  default: [sample/inherited, sample/overridden]
`), "")
	if err != nil {
		t.Fatal(err)
	}
	p := rc.Config.Providers["sample"]
	for _, name := range []string{"inherited", "overridden"} {
		m := p.Models[name]
		effective, _ := ResolveResponsesCompat("", name, m, p.Compat)
		if effective == nil || effective.SendParallelToolCalls == nil || *effective.SendParallelToolCalls {
			t.Fatalf("provider false replaced: %+v", effective)
		}
		if effective.SendStore == nil || *effective.SendStore != (name == "inherited") {
			t.Fatalf("store override/default lost for %s: %+v", name, effective)
		}
	}
}

func TestInvalidCompactionDeclarationsDoNotSuppressCatalog(t *testing.T) {
	base := "providers:\n  openai: {preset: openai}\nmodel_pools:\n  default: [openai/gpt-6.1-sol]\n"
	baseline, err := LoadResolvedConfig(writeCatalogTestConfig(t, "config.yaml", base), "")
	if err != nil {
		t.Fatal(err)
	}
	want := baseline.Config.Providers["openai"].Models["gpt-6.1-sol"].Compaction
	if want == nil || want.Threshold == nil || want.Reminder == nil {
		t.Fatal("catalog fixture has no compaction recommendation")
	}
	for _, layer := range []string{"global", "project"} {
		for _, value := range []string{"2", ".nan", ".inf", "-.inf"} {
			t.Run(layer+"/"+value, func(t *testing.T) {
				invalid := fmt.Sprintf("context:\n  compaction: {threshold: %s, reminder: %s}\n", value, value)
				global, project := base, ""
				if layer == "global" {
					global += invalid
				} else {
					project = writeCatalogTestConfig(t, "project.yaml", invalid)
				}
				rc, err := LoadResolvedConfig(writeCatalogTestConfig(t, "config.yaml", global), project)
				if err != nil {
					t.Fatal(err)
				}
				got := rc.Config.Providers["openai"].Models["gpt-6.1-sol"].Compaction
				if got == nil || got.Threshold == nil || got.Reminder == nil || *got.Threshold != *want.Threshold || *got.Reminder != *want.Reminder {
					t.Fatalf("compaction=%+v, want catalog recommendation %+v", got, want)
				}
				if len(rc.Index.Compaction["threshold"]) != 0 || len(rc.Index.Compaction["reminder"]) != 0 {
					t.Fatalf("invalid values retain origins: %+v", rc.Index.Compaction)
				}
			})
		}
	}
	// An invalid project override must preserve a valid global declaration.
	global := writeCatalogTestConfig(t, "config.yaml", base+"context:\n  compaction: {threshold: 0, reminder: -1}\n")
	project := writeCatalogTestConfig(t, "project.yaml", "context:\n  compaction: {threshold: 2, reminder: 2}\n")
	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatal(err)
	}
	if got := rc.Config.Providers["openai"].Models["gpt-6.1-sol"].Compaction; got != nil {
		t.Fatalf("explicit disable replaced: %+v", got)
	}
	for _, key := range []string{"threshold", "reminder"} {
		if origins := rc.Index.Compaction[key]; len(origins) != 1 || origins[0].Layer != OriginLayerGlobal {
			t.Fatalf("%s origins=%+v", key, origins)
		}
	}
}
