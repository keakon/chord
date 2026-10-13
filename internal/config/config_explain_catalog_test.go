package config

import (
	"slices"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

func TestExplainCatalogAssignments(t *testing.T) {
	facts := modelcatalog.ModelFacts{ID: "sample/catalog-model", Context: 1000, Input: 800, Output: 200}
	for _, tc := range []struct {
		name, body  string
		input       int
		inputKind   ExplainSourceKind
		effortState ExplainValueState
	}{
		{"absent", "        name: Example\n", 800, ExplainSourceCatalog, ExplainValueSet},
		{"zero", "        limit: {input: 0}\n", 0, ExplainSourceGlobal, ExplainValueSet},
		{"partial_block", "        reasoning: {summary: auto}\n", 800, ExplainSourceCatalog, ExplainValueUnset},
		{"cleared", "        reasoning: null\n", 800, ExplainSourceCatalog, ExplainValueCleared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := loadExplainFixture(t, explainFixtureModel(tc.body), "")
			rc.CatalogVersion = "fixture-1"
			provider := rc.Config.Providers["sample"]
			model := provider.Models["model-1"]
			identity := ExplainSource{Kind: ExplainSourceCatalog, CatalogModelID: facts.ID, CatalogVersion: rc.CatalogVersion, Preset: "sample-preset", WireModel: "wire-model"}
			if !model.fillFromCatalog(facts, nil, &modelcatalog.ConfigProfile{}, ModelConfig{Reasoning: &ReasoningConfig{Effort: "high"}, Thinking: &ThinkingConfig{Type: "enabled"}}, rc.Index, "sample", "model-1", true, rc.ExplainEvidence, identity) {
				t.Fatal("nothing filled")
			}
			provider.Models["model-1"] = model
			rc.Config.Providers["sample"] = provider
			r := mustExplain(t, rc, ExplainLimitInput)
			if r.Value != tc.input || r.ValueState != ExplainValueSet || r.SourceStatus != ExplainSourceComplete || r.Source.Kind != tc.inputKind {
				t.Fatalf("%+v", r)
			}
			if tc.inputKind == ExplainSourceCatalog && (r.Source.CatalogVersion != "fixture-1" || r.Source.CatalogModelID != facts.ID || r.Source.WireModel != "wire-model" || r.EditTarget.Action != ExplainEditAddOverride) {
				t.Fatalf("%+v", r)
			}
			r = mustExplain(t, rc, ExplainReasoningEffort)
			if r.ValueState != tc.effortState || r.SourceStatus != ExplainSourceComplete {
				t.Fatalf("%+v", r)
			}
			if tc.name == "partial_block" && !slices.ContainsFunc(r.Notes, func(n ExplainNote) bool { return n.Code == ExplainNoteCatalogBlock }) {
				t.Fatalf("%+v", r)
			}
			r = mustExplain(t, rc, ExplainThinkingType)
			if r.Value != "enabled" || r.Source.Kind != ExplainSourceCatalog || r.Declarations[0].Effect != ExplainEffectSelected {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestExplainCatalogMaterialization(t *testing.T) {
	// This is the repository's embedded, offline catalog, never a home-directory
	// refresh cache. Synthetic assignment behavior is covered above.
	global := writeCatalogTestConfig(t, "global.yaml", "providers:\n  sample:\n    preset: openai\n")
	rc, err := LoadResolvedConfig(global, "", "sample/gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ExplainModelField(rc, "sample", "gpt-6.1-sol", ExplainLimitContext)
	if err != nil || r.Source == nil || r.Source.Kind != ExplainSourceCatalog || r.Source.CatalogModelID != "openai/gpt-6.1-sol" || r.Source.WireModel != "gpt-6.1-sol" || r.Source.CatalogVersion != rc.CatalogVersion {
		t.Fatalf("%+v %v", r, err)
	}
	rc = loadExplainFixture(t, explainFixtureModel("        catalog: openai/gpt-6.1-sol\n"), "")
	r = mustExplain(t, rc, ExplainLimitContext)
	if r.Source == nil || r.Source.Kind != ExplainSourceCatalog || r.Source.CatalogModelID != "openai/gpt-6.1-sol" || r.Source.Preset != "" {
		t.Fatalf("%+v", r)
	}
	rc = loadExplainFixture(t, explainFixtureModel("        catalog: false\n"), "")
	r = mustExplain(t, rc, ExplainLimitContext)
	if r.Source != nil || r.ValueState != ExplainValueUnset {
		t.Fatalf("%+v", r)
	}
	rc = loadExplainFixture(t, explainFixtureModel("        catalog: sample/missing\n"), "")
	r = mustExplain(t, rc, ExplainLimitInput)
	if len(r.Diagnostics) != 1 || r.Source != nil {
		t.Fatalf("%+v", r)
	}
}

func TestExplainEmptyCatalogBlockAndDerivedExplicitZero(t *testing.T) {
	rc := loadExplainFixture(t, explainFixtureModel("        limit: {context: 0, input: 100, output: 20}\n"), "")
	r := mustExplain(t, rc, ExplainLimitContext)
	if r.SourceStatus != ExplainSourceComplete || r.Source.Kind != ExplainSourceDerived || r.Value != 120 || r.Declarations[0].Value != 0 || r.Declarations[0].Effect != ExplainEffectObserved {
		t.Fatalf("%+v", r)
	}
	p := rc.Config.Providers["sample"]
	m := p.Models["model-1"]
	m.fillFromCatalog(modelcatalog.ModelFacts{ID: "sample/empty"}, nil, &modelcatalog.ConfigProfile{}, ModelConfig{Reasoning: &ReasoningConfig{}}, rc.Index, "sample", "model-1", true, rc.ExplainEvidence, ExplainSource{Kind: ExplainSourceCatalog, CatalogModelID: "sample/empty", CatalogVersion: rc.CatalogVersion})
	p.Models["model-1"] = m
	rc.Config.Providers["sample"] = p
	r = mustExplain(t, rc, ExplainReasoningEffort)
	if r.ValueState != ExplainValueUnset || r.Value != nil || r.Source != nil || r.SourceStatus != ExplainSourceComplete || len(r.Declarations) != 1 || r.Declarations[0].Value != "" || r.Declarations[0].Effect != ExplainEffectObserved {
		t.Fatalf("%+v", r)
	}
}
