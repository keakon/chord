package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const originGlobalConfig = `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    preset: sample
    models:
      model-1:
        limit:
          context: 100000
          input: 80000
          output: 20000
        reasoning:
          effort: low
          summary: auto
        thinking:
          type: enabled
          budget: 1024
        variants:
          fast:
            reasoning:
              effort: high
model_pools:
  default:
    - sample-provider/model-1
    - sample-provider/model-1@fast
`

const originProjectConfig = `providers:
  sample-provider:
    models:
      model-1:
        reasoning: null
        limit:
          output: 24000
model_pools:
  secondary:
    - sample-provider/model-2
`

func writeOriginLayer(t *testing.T, name, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func buildTestIndex(t *testing.T) *SourceIndex {
	t.Helper()
	idx, err := BuildSourceIndex(
		ConfigLayer{Layer: OriginLayerGlobal, File: "global.yaml", Data: []byte(originGlobalConfig)},
		ConfigLayer{Layer: OriginLayerProject, File: "project.yaml", Data: []byte(originProjectConfig)},
	)
	if err != nil {
		t.Fatalf("BuildSourceIndex: %v", err)
	}
	return idx
}

func TestBuildSourceIndexTracksSelectionsBlocksAndPools(t *testing.T) {
	idx := buildTestIndex(t)

	preset, ok := idx.PresetOrigin("sample-provider")
	if !ok {
		t.Fatal("PresetOrigin: sample-provider preset not tracked")
	}
	if preset.Layer != OriginLayerGlobal || preset.File != "global.yaml" || preset.Line == 0 {
		t.Fatalf("PresetOrigin = %+v, want the global declaration", preset)
	}

	mo, ok := idx.Model("sample-provider", "model-1")
	if !ok {
		t.Fatal("Model: model-1 not tracked")
	}
	// Both layers redeclare the model key: the project layer narrows fields of
	// the same model.
	if len(mo.Defined) != 2 || mo.Defined[0].Layer != OriginLayerGlobal || mo.Defined[1].Layer != OriginLayerProject {
		t.Fatalf("Defined = %+v, want the global then the project declaration", mo.Defined)
	}

	reasoning, ok := idx.ModelBlock("sample-provider", "model-1", "reasoning")
	if !ok {
		t.Fatal("ModelBlock: reasoning not tracked")
	}
	if len(reasoning) != 2 {
		t.Fatalf("reasoning declarations = %d, want 2 (global map, project null)", len(reasoning))
	}
	if reasoning[0].Null || reasoning[0].Layer != OriginLayerGlobal {
		t.Fatalf("reasoning[0] = %+v, want the global mapping", reasoning[0])
	}
	if !reasoning[1].Null || reasoning[1].Layer != OriginLayerProject {
		t.Fatalf("reasoning[1] = %+v, want the project null", reasoning[1])
	}
	if state := BlockClearing(reasoning); !state.Cleared || state.Active {
		t.Fatalf("BlockClearing(reasoning) = %+v, want cleared by the project null", state)
	}

	thinking, ok := idx.ModelBlock("sample-provider", "model-1", "thinking")
	if !ok || len(thinking) != 1 || thinking[0].Null {
		t.Fatalf("thinking declarations = %+v, want the global mapping only", thinking)
	}
	if state := BlockClearing(thinking); !state.Active || state.Cleared {
		t.Fatalf("BlockClearing(thinking) = %+v, want active", state)
	}

	variants, ok := idx.ModelBlock("sample-provider", "model-1", "variants")
	if !ok || len(variants) != 1 || variants[0].Layer != OriginLayerGlobal {
		t.Fatalf("variants declarations = %+v, want the global mapping only", variants)
	}

	if _, ok := idx.ModelBlock("sample-provider", "model-1", "prompt_cache"); ok {
		t.Fatal("ModelBlock: prompt_cache reported present without any declaration")
	}

	if refs, ok := idx.PoolRefs("default"); !ok || len(refs) != 2 {
		t.Fatalf("PoolRefs(default) = %+v, want 2 refs", refs)
	} else if refs[0].Ref != "sample-provider/model-1" || refs[1].Ref != "sample-provider/model-1@fast" {
		t.Fatalf("PoolRefs(default) refs = %q, %q", refs[0].Ref, refs[1].Ref)
	} else if refs[0].Line == 0 || refs[0].Layer != OriginLayerGlobal {
		t.Fatalf("PoolRefs(default)[0] = %+v, want a positioned global ref", refs[0])
	}
	if _, ok := idx.PoolRefs("secondary"); !ok || len(idx.ModelPools["secondary"]) != 1 {
		t.Fatalf("PoolRefs(secondary) not tracked: %+v", idx.ModelPools["secondary"])
	}
}

func TestBuildSourceIndexUntrackedProvider(t *testing.T) {
	idx := buildTestIndex(t)
	if _, ok := idx.PresetOrigin("other-provider"); ok {
		t.Fatal("PresetOrigin: untracked provider reported")
	}
	if _, ok := idx.Model("sample-provider", "model-9"); ok {
		t.Fatal("Model: untracked model reported")
	}
}

func TestBuildSourceIndexResolvesAnchors(t *testing.T) {
	data := `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1: &base
        limit:
          context: 100000
          input: 80000
      model-2:
        <<: *base
      model-3:
        <<: *base
        limit:
          output: 20000
model_pools:
  default:
    - &ref sample-provider/model-1
  other:
    - *ref
`
	idx, err := BuildSourceIndex(ConfigLayer{Layer: OriginLayerGlobal, File: "global.yaml", Data: []byte(data)})
	if err != nil {
		t.Fatalf("BuildSourceIndex: %v", err)
	}

	// Merge keys pull the anchor's leaves in when the current entry does not
	// declare the same key: model-2 takes its whole limit block from the
	// anchor, so its explicit input contract is tracked.
	if _, ok := idx.ExplicitInputContract("sample-provider", "model-2"); !ok {
		t.Fatal("ExplicitInputContract: model-2 input merged through the anchor not tracked")
	}
	// An explicit key shadows the merged key as a whole block, matching the
	// decoder: model-3 declares limit.output only, so no input contract exists
	// even though the anchor carries one.
	if _, ok := idx.ExplicitInputContract("sample-provider", "model-3"); ok {
		t.Fatal("ExplicitInputContract: model-3 explicit limit must shadow the merged anchor")
	}
	mo, ok := idx.Model("sample-provider", "model-3")
	if !ok || len(mo.Limit.Output) != 1 || len(mo.Limit.Context) != 0 {
		t.Fatalf("model-3 limit origins = %+v, want only the explicit output", mo.Limit)
	}

	refs, ok := idx.PoolRefs("other")
	if !ok || len(refs) != 1 || refs[0].Ref != "sample-provider/model-1" {
		t.Fatalf("PoolRefs(other) = %+v, want the aliased ref resolved", refs)
	}
}

func TestBlockClearing(t *testing.T) {
	globalOrigin := Origin{Layer: OriginLayerGlobal, File: "g.yaml", Line: 3}
	projectOrigin := Origin{Layer: OriginLayerProject, File: "p.yaml", Line: 5}
	globalMap := BlockOrigin{Origin: globalOrigin}
	globalNull := BlockOrigin{Origin: globalOrigin, Null: true}
	projectMap := BlockOrigin{Origin: projectOrigin}
	projectNull := BlockOrigin{Origin: projectOrigin, Null: true}

	tests := []struct {
		name         string
		decls        []BlockOrigin
		wantPresent  bool
		wantActive   bool
		wantCleared  bool
		wantTopLayer OriginLayer
	}{
		{name: "absent"},
		{name: "global mapping only", decls: []BlockOrigin{globalMap}, wantPresent: true, wantActive: true, wantTopLayer: OriginLayerGlobal},
		{name: "global null only", decls: []BlockOrigin{globalNull}, wantPresent: true, wantCleared: true, wantTopLayer: OriginLayerGlobal},
		// A project null clears the global mapping it overlays.
		{name: "project null clears global", decls: []BlockOrigin{globalMap, projectNull}, wantPresent: true, wantCleared: true, wantTopLayer: OriginLayerProject},
		// A project mapping re-activates a block the global layer cleared;
		// fields the project leaves unfilled stay unfilled.
		{name: "project mapping over global null", decls: []BlockOrigin{globalNull, projectMap}, wantPresent: true, wantActive: true, wantTopLayer: OriginLayerProject},
		{name: "both mappings", decls: []BlockOrigin{globalMap, projectMap}, wantPresent: true, wantActive: true, wantTopLayer: OriginLayerProject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := BlockClearing(tt.decls)
			if state.Present != tt.wantPresent || state.Active != tt.wantActive || state.Cleared != tt.wantCleared {
				t.Fatalf("BlockClearing = %+v, want present=%t active=%t cleared=%t", state, tt.wantPresent, tt.wantActive, tt.wantCleared)
			}
			if tt.wantPresent && state.Origin.Layer != tt.wantTopLayer {
				t.Fatalf("BlockClearing origin layer = %q, want %q", state.Origin.Layer, tt.wantTopLayer)
			}
		})
	}
}

func TestSourceIndexExplicitInputContract(t *testing.T) {
	idx := buildTestIndex(t)
	origin, ok := idx.ExplicitInputContract("sample-provider", "model-1")
	if !ok {
		t.Fatal("ExplicitInputContract: explicit input not tracked")
	}
	if origin.Layer != OriginLayerGlobal {
		t.Fatalf("ExplicitInputContract layer = %q, want global (project declared none)", origin.Layer)
	}
	if _, ok := idx.ExplicitInputContract("sample-provider", "model-2"); ok {
		t.Fatal("ExplicitInputContract: undeclared model reported")
	}
}

// TestMergeCrossLayerBlockClearing pins the map-level project merge semantics
// for nullable model blocks: an explicit null clears the block's inherited
// values, an empty mapping keeps them, and a higher layer that redeclares the
// block only contributes the fields it writes.
func TestMergeCrossLayerBlockClearing(t *testing.T) {
	global := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        limit:
          context: 100000
          input: 80000
          output: 20000
        reasoning:
          effort: low
          summary: auto
        thinking:
          type: enabled
          budget: 1024
        variants:
          slow:
            reasoning:
              effort: low
          fast:
            reasoning:
              effort: high
`)
	project := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    models:
      model-1:
        reasoning: null
        thinking: {}
        variants: null
        limit:
          output: 24000
`)
	base, err := LoadConfigFromPath(global)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	_, merged, err := MergeProjectConfig(base, project)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}

	model := merged.Providers["sample-provider"].Models["model-1"]
	if model.Reasoning != nil {
		t.Fatalf("Reasoning = %+v, want cleared by the project null", model.Reasoning)
	}
	if model.Thinking == nil || model.Thinking.Type != ThinkingTypeEnabled || model.Thinking.Budget != 1024 {
		t.Fatalf("Thinking = %+v, want inherited from global (empty mapping does not clear)", model.Thinking)
	}
	if len(model.Variants) != 0 {
		t.Fatalf("Variants = %+v, want cleared by the project null", model.Variants)
	}
	if model.Limit.Output != 24000 || model.Limit.Context != 100000 || model.Limit.Input != 80000 {
		t.Fatalf("Limit = %+v, want project output merged over the global leaves", model.Limit)
	}

	// A higher layer redeclaring a block the lower layer nulled contributes
	// only what it writes.
	globalCleared := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        reasoning: null
`)
	projectRebuilt := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    models:
      model-1:
        reasoning:
          effort: high
`)
	base, err = LoadConfigFromPath(globalCleared)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	_, merged, err = MergeProjectConfig(base, projectRebuilt)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	model = merged.Providers["sample-provider"].Models["model-1"]
	if model.Reasoning == nil || model.Reasoning.Effort != "high" || model.Reasoning.Summary != "" {
		t.Fatalf("Reasoning = %+v, want only the project effort after the global null", model.Reasoning)
	}
}

func TestLoadResolvedConfig(t *testing.T) {
	global := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        limit:
          context: 100000
          input: 80000
          output: 20000
        reasoning:
          effort: low
model_pools:
  default:
    - sample-provider/model-1
`)
	project := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    models:
      model-1:
        reasoning: null
        limit:
          output: 24000
      model-2:
        limit:
          context: 5000
model_pools:
  secondary:
    - sample-provider/model-2
    - sample-provider/gone
`)

	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	if rc.Global == nil || rc.Project == nil || rc.Config == nil {
		t.Fatalf("layers = %v/%v/%v, want all loaded", rc.Global, rc.Project, rc.Config)
	}
	model := rc.Config.Providers["sample-provider"].Models["model-1"]
	if model.Reasoning != nil {
		t.Fatalf("Reasoning = %+v, want cleared by the project null", model.Reasoning)
	}
	if model.Limit.Output != 24000 || model.Limit.Context != 100000 {
		t.Fatalf("Limit = %+v, want project output over global context", model.Limit)
	}
	if _, ok := rc.Config.Providers["sample-provider"].Models["model-2"]; !ok {
		t.Fatal("model-2 from the project layer missing in the merged config")
	}

	if _, ok := rc.Index.ExplicitInputContract("sample-provider", "model-1"); !ok {
		t.Fatal("Index: global explicit input contract not tracked")
	}
	reasoning, ok := rc.Index.ModelBlock("sample-provider", "model-1", "reasoning")
	if !ok || len(reasoning) != 2 || !reasoning[1].Null {
		t.Fatalf("Index reasoning = %+v, want global mapping + project null", reasoning)
	}

	// model-1 is pool-referenced and fully declared; model-2 exists in the
	// provider but only the secondary pool references it with one broken ref.
	var broken []Diagnostic
	for _, d := range rc.Diagnostics {
		if d.Severity == DiagnosticSeverityError {
			broken = append(broken, d)
		}
	}
	if len(broken) != 1 {
		t.Fatalf("error diagnostics = %+v, want exactly the broken secondary-pool ref", rc.Diagnostics)
	}
	if broken[0].Scope != "model pool secondary" || !broken[0].Continues {
		t.Fatalf("broken ref diagnostic = %+v, want secondary pool scope and continuing load", broken[0])
	}
}

func TestLoadResolvedConfigMissingGlobal(t *testing.T) {
	project := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        limit:
          context: 100000
          output: 20000
`)
	rc, err := LoadResolvedConfig(filepath.Join(t.TempDir(), "missing.yaml"), project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	if rc.Global != nil {
		t.Fatalf("Global = %+v, want nil for a missing file", rc.Global)
	}
	if rc.Config == nil || len(rc.Config.Providers) != 1 {
		t.Fatalf("Config = %+v, want the project-only config", rc.Config)
	}
	var missing []Diagnostic
	for _, d := range rc.Diagnostics {
		if d.Severity == DiagnosticSeverityWarning && d.Message == "global config file not found; no providers are configured" {
			missing = append(missing, d)
		}
	}
	if len(missing) != 1 {
		t.Fatalf("missing-config diagnostics = %+v, want exactly one", rc.Diagnostics)
	}
}

func TestLoadResolvedConfigInvalidOverrideDiagnosed(t *testing.T) {
	global := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        limit:
          context: 100000
          output: 20000
`)
	project := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    models:
      model-1:
        limit:
          context: 100000
          output: 20000
confirm_timeout:
  not: a number
`)
	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	var dropped bool
	for _, d := range rc.Diagnostics {
		if d.Severity == DiagnosticSeverityWarning && d.Path == "confirm_timeout" && d.Fallback != "" {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("diagnostics = %+v, want the dropped invalid override", rc.Diagnostics)
	}
	if got := rc.Config.ConfirmTimeout; got != 0 {
		t.Fatalf("ConfirmTimeout = %d, want the default 0 after the drop", got)
	}
}

func TestResolvedConfigMatchesManualLoadPath(t *testing.T) {
	global := writeOriginLayer(t, "config.yaml", strings.ReplaceAll(originGlobalConfig, "    preset: sample\n", ""))
	project := writeOriginLayer(t, "config.yaml", originProjectConfig)

	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	base, err := LoadConfigFromPath(global)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	_, manual, err := MergeProjectConfig(base, project)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if !reflect.DeepEqual(rc.Config, manual) {
		t.Fatal("LoadResolvedConfig effective config differs from the LoadConfig + MergeProjectConfig result")
	}
}
