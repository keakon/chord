package config

import (
	"slices"
	"testing"
)

func TestExplainAncestorNullMasksEqualAndDifferentScalars(t *testing.T) {
	for _, tc := range []struct {
		name, project string
		depth         int
		missing       bool
	}{
		{"providers", "providers: null\n", 1, true},
		{"provider", "providers:\n  sample: null\n", 2, true},
		{"models", "providers:\n  sample:\n    models: null\n", 3, true},
		{"model", "providers:\n  sample:\n    models:\n      model-1: null\n", 4, false},
		{"limit", explainFixtureModel("        limit: null\n"), 5, false},
	} {
		for _, value := range []struct{ name, body string }{
			{"zero", "        limit: {context: 0, input: 0, output: 0}\n        reasoning: {effort: ''}\n        thinking: {type: ''}\n"},
			{"nonzero", "        limit: {context: 120, input: 100, output: 20}\n        reasoning: {effort: high}\n        thinking: {type: enabled}\n"},
		} {
			t.Run(tc.name+"/"+value.name, func(t *testing.T) {
				rc := loadExplainFixture(t, explainFixtureModel(value.body), tc.project)
				fields := []ExplainField{ExplainLimitInput, ExplainLimitOutput, ExplainLimitContext}
				if tc.depth < 5 {
					fields = append(fields, ExplainReasoningEffort, ExplainThinkingType)
				}
				for _, field := range fields {
					r, err := ExplainModelField(rc, "sample", "model-1", field)
					if tc.missing {
						if err == nil {
							t.Fatalf("expected missing model, got %+v", r)
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					if r.Source != nil || r.SourceStatus != ExplainSourcePartial || r.ValueState != ExplainValueUnknown {
						t.Fatalf("stale source: %+v", r)
					}
					d, _ := explainDescriptor(field)
					value, _ := d.get(rc.Config.Providers["sample"].Models["model-1"])
					if r.Value != value.value() {
						t.Fatalf("value=%v, resolved=%v", r.Value, value.value())
					}
					if r.EditTarget == nil || r.EditTarget.Action != ExplainEditReplace || r.EditTarget.File != rc.ExplainEvidence.projectPath || len(r.EditTarget.PathSegments) != tc.depth || r.EditTarget.Line == 0 {
						t.Fatalf("edit: %+v", r.EditTarget)
					}
					if len(r.Declarations) != 2 || r.Declarations[0].Effect != ExplainEffectOverridden || r.Declarations[1].Effect != ExplainEffectClearing {
						t.Fatalf("declarations: %+v", r.Declarations)
					}
				}
			})
		}
	}
}

func TestExplainModelNullSupersedesCapabilityNull(t *testing.T) {
	rc := loadExplainFixture(t, explainFixtureModel("        reasoning: null\n"), "providers:\n  sample:\n    models:\n      model-1: null\n")
	r := mustExplain(t, rc, ExplainReasoningEffort)
	if r.Source != nil || r.SourceStatus != ExplainSourcePartial || r.ValueState != ExplainValueUnknown || len(r.EditTarget.PathSegments) != 4 {
		t.Fatalf("%+v", r)
	}
}

func TestExplainRebuiltAncestorAndUnrelatedProject(t *testing.T) {
	for _, tc := range []struct {
		global, project string
		state           ExplainValueState
		source          ExplainSourceKind
	}{
		{"        limit: null\n", explainFixtureModel("        limit: {input: 50}\n"), ExplainValueSet, ExplainSourceProject},
		{"        reasoning: null\n", "providers:\n  sample:\n    models:\n      model-2: {}\n", ExplainValueCleared, ExplainSourceGlobal},
		{"        reasoning: null\n", explainFixtureModel("        reasoning: {summary: auto}\n"), ExplainValueUnset, ""},
	} {
		rc := loadExplainFixture(t, explainFixtureModel(tc.global), tc.project)
		field := ExplainReasoningEffort
		if tc.source == ExplainSourceProject {
			field = ExplainLimitInput
		}
		r := mustExplain(t, rc, field)
		if r.ValueState != tc.state || r.SourceStatus != ExplainSourceComplete || tc.source != "" && (r.Source == nil || r.Source.Kind != tc.source) {
			t.Fatalf("%+v", r)
		}
	}
}

func TestExplainInheritedNullSuggestsLocalOverride(t *testing.T) {
	rc := loadExplainFixture(t, explainFixtureModel("        limit: {input: 0}\n"), "model_templates:\n  shared: &shared {limit: null}\nproviders:\n  sample:\n    models:\n      model-1: {<<: *shared}\n")
	r := mustExplain(t, rc, ExplainLimitInput)
	if r.Source != nil || r.EditTarget.Action != ExplainEditAddOverride || r.EditTarget.Line != 0 || len(r.EditTarget.PathSegments) != 6 || r.EditTarget.File != rc.ExplainEvidence.projectPath {
		t.Fatalf("%+v", r)
	}
}

func TestExplainCatalogBlockNoteOnlyForMissingEnabledLeaf(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"        reasoning: {effort: high}\n", false},
		{"        reasoning: {effort: ''}\n", false},
		{"        reasoning: {summary: auto}\n", true},
		{"        catalog: false\n        reasoning: {summary: auto}\n", false},
	} {
		rc := loadExplainFixture(t, explainFixtureModel(tc.body), "")
		r := mustExplain(t, rc, ExplainReasoningEffort)
		got := slices.ContainsFunc(r.Notes, func(n ExplainNote) bool { return n.Code == ExplainNoteCatalogBlock })
		if got != tc.want {
			t.Fatalf("body=%s notes=%+v", tc.body, r.Notes)
		}
	}
}
