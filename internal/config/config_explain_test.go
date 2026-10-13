package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func loadExplainFixture(t *testing.T, global, project string) *ResolvedConfig {
	t.Helper()
	dir := t.TempDir()
	g, p := filepath.Join(dir, "global.yaml"), filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(g, []byte(global), 0600); err != nil {
		t.Fatal(err)
	}
	if project != "" {
		if err := os.WriteFile(p, []byte(project), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := LoadResolvedConfig(g, p)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}
func explainFixtureModel(body string) string {
	return "providers:\n  sample:\n    type: responses\n    models:\n      model-1:\n" + body
}
func mustExplain(t *testing.T, rc *ResolvedConfig, field ExplainField) ModelFieldExplanation {
	t.Helper()
	result, err := ExplainModelField(rc, "sample", "model-1", field)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestExplainModelFieldLayers(t *testing.T) {
	for _, tc := range []struct {
		name, global, project string
		state                 ExplainValueState
		value                 any
		effects               []ExplainEffect
		source                ExplainSourceKind
	}{
		{"same", "        reasoning: {effort: high}\n", "        reasoning: {effort: high}\n", ExplainValueSet, "high", []ExplainEffect{ExplainEffectOverridden, ExplainEffectSelected}, ExplainSourceProject},
		{"different", "        reasoning: {effort: high}\n", "        reasoning: {effort: low}\n", ExplainValueSet, "low", []ExplainEffect{ExplainEffectOverridden, ExplainEffectSelected}, ExplainSourceProject},
		{"sibling", "        reasoning: {effort: high}\n", "        reasoning: {summary: auto}\n", ExplainValueSet, "high", []ExplainEffect{ExplainEffectSelected, ExplainEffectObserved}, ExplainSourceGlobal},
		{"clear", "        reasoning: {effort: high}\n", "        reasoning: null\n", ExplainValueCleared, nil, []ExplainEffect{ExplainEffectOverridden, ExplainEffectClearing}, ExplainSourceProject},
		{"rebuild", "        reasoning: null\n", "        reasoning: {summary: auto}\n", ExplainValueUnset, nil, []ExplainEffect{ExplainEffectClearing, ExplainEffectObserved}, ""},
		{"empty", "        reasoning: {effort: ''}\n", "", ExplainValueSet, "", []ExplainEffect{ExplainEffectSelected}, ExplainSourceGlobal},
		{"absent", "        name: Example\n", "", ExplainValueUnset, nil, nil, ""},
		{"empty_block", "        reasoning: {}\n", "", ExplainValueUnset, nil, []ExplainEffect{ExplainEffectObserved}, ""},
		{"invalid_leaf", "        reasoning: {effort: high}\n", "        reasoning: {effort: [private-invalid-value]}\n", ExplainValueSet, "high", []ExplainEffect{ExplainEffectSelected, ExplainEffectIgnored}, ExplainSourceGlobal},
		{"invalid_block", "        reasoning: {effort: high}\n", "        reasoning: [private-invalid-value]\n", ExplainValueSet, "high", []ExplainEffect{ExplainEffectSelected, ExplainEffectIgnored}, ExplainSourceGlobal},
		{"invalid_model", "        reasoning: {effort: high}\n", "        - private-invalid-value\n", ExplainValueSet, "high", []ExplainEffect{ExplainEffectSelected, ExplainEffectIgnored}, ExplainSourceGlobal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := ""
			if tc.project != "" {
				project = explainFixtureModel(tc.project)
			}
			rc := loadExplainFixture(t, explainFixtureModel(tc.global), project)
			before, _ := json.Marshal(rc.Config)
			r := mustExplain(t, rc, ExplainReasoningEffort)
			if r.ValueState != tc.state || !reflect.DeepEqual(r.Value, tc.value) || r.SourceStatus != ExplainSourceComplete {
				t.Fatalf("result: %+v", r)
			}
			var effects []ExplainEffect
			for _, d := range r.Declarations {
				effects = append(effects, d.Effect)
			}
			if !reflect.DeepEqual(effects, tc.effects) {
				t.Fatalf("effects: %v want %v (%+v)", effects, tc.effects, r)
			}
			if tc.source != "" && (r.Source == nil || r.Source.Kind != tc.source || r.Source.Line == 0) {
				t.Fatalf("source: %+v", r.Source)
			}
			if tc.state == ExplainValueCleared && (r.EditTarget.Action != ExplainEditReplace || len(r.EditTarget.PathSegments) != 5) {
				t.Fatalf("clear edit: %+v", r.EditTarget)
			}
			encoded, _ := json.Marshal(r)
			if strings.Contains(string(encoded), "private-invalid-value") {
				t.Fatalf("leaked invalid content: %s", encoded)
			}
			after, _ := json.Marshal(rc.Config)
			if string(before) != string(after) {
				t.Fatal("explanation mutated config")
			}
			data, err := os.ReadFile(rc.ExplainEvidence.globalPath)
			if err != nil || string(data) != explainFixtureModel(tc.global) {
				t.Fatal("explanation changed file")
			}
		})
	}
}
func TestExplainLimitsAndDerivation(t *testing.T) {
	for _, tc := range []struct {
		name, global, project string
		field                 ExplainField
		state                 ExplainValueState
		value                 any
		kind                  ExplainSourceKind
	}{
		{"zero", "        limit: {input: 0}\n", "", ExplainLimitInput, ExplainValueSet, 0, ExplainSourceGlobal},
		{"absent", "        name: Example\n", "", ExplainLimitInput, ExplainValueUnset, nil, ""},
		{"derived", "        limit: {input: 100, output: 20}\n", "", ExplainLimitContext, ExplainValueSet, 120, ExplainSourceDerived},
		{"rederived", "        limit: {input: 100, output: 20}\n", "        limit: {input: 150}\n", ExplainLimitContext, ExplainValueSet, 170, ExplainSourceDerived},
		{"explicit", "        limit: {context: 120, input: 100, output: 20}\n", "", ExplainLimitContext, ExplainValueSet, 120, ExplainSourceGlobal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := ""
			if tc.project != "" {
				project = explainFixtureModel(tc.project)
			}
			rc := loadExplainFixture(t, explainFixtureModel(tc.global), project)
			r := mustExplain(t, rc, tc.field)
			if r.ValueState != tc.state || r.Value != tc.value || r.SourceStatus != ExplainSourceComplete {
				t.Fatalf("%+v", r)
			}
			if tc.kind != "" && (r.Source == nil || r.Source.Kind != tc.kind) {
				t.Fatalf("source: %+v", r.Source)
			}
			if tc.kind == ExplainSourceDerived {
				if r.Source.Rule != "input_plus_output" || len(r.Source.Inputs) != 2 || r.Source.Inputs[0].Source == nil || r.Source.Inputs[1].SourceStatus != ExplainSourceComplete {
					t.Fatalf("inputs: %+v", r.Source)
				}
			}
			if tc.name == "zero" {
				data, _ := json.Marshal(r)
				if !strings.Contains(string(data), "\"value\":0") || !slices.ContainsFunc(r.Notes, func(n ExplainNote) bool { return n.Code == ExplainNoteInput }) {
					t.Fatalf("%s", data)
				}
			}
		})
	}
}
func TestExplainUntrackedPartialAndLeafNull(t *testing.T) {
	rc := loadExplainFixture(t, explainFixtureModel("        reasoning: {effort: high}\n"), explainFixtureModel("        reasoning: {effort: null}\n"))
	r := mustExplain(t, rc, ExplainReasoningEffort)
	if r.SourceStatus != ExplainSourcePartial || r.ValueState == ExplainValueCleared || r.Value != rc.Config.Providers["sample"].Models["model-1"].Reasoning.Effort {
		t.Fatalf("%+v", r)
	}
	rc.ExplainEvidence = nil
	r = mustExplain(t, rc, ExplainLimitInput)
	if r.SourceStatus != ExplainSourceUntracked || r.ValueState != ExplainValueUnknown || r.Value != 0 {
		t.Fatalf("%+v", r)
	}
	rc = loadExplainFixture(t, explainFixtureModel("        thinking: {type: ''}\n"), "")
	r = mustExplain(t, rc, ExplainThinkingType)
	if r.Value != "" || r.ValueState != ExplainValueSet {
		t.Fatalf("%+v", r)
	}
	p := rc.Config.Providers["sample"]
	m := p.Models["model-1"]
	m.Thinking.Type = "enabled"
	p.Models["model-1"] = m
	rc.Config.Providers["sample"] = p
	r = mustExplain(t, rc, ExplainThinkingType)
	if r.SourceStatus != ExplainSourcePartial || r.Value != "enabled" || r.Source != nil || r.Declarations[0].Effect != ExplainEffectObserved {
		t.Fatalf("%+v", r)
	}
	rc.ExplainEvidence.layers[0].complete = false
	r = mustExplain(t, rc, ExplainLimitOutput)
	if r.SourceStatus != ExplainSourcePartial || r.ValueState != ExplainValueUnknown {
		t.Fatalf("%+v", r)
	}
	if _, err := ExplainModelField(rc, "sample", "missing", ExplainLimitInput); err == nil {
		t.Fatal("missing model accepted")
	}
	if _, err := ExplainModelField(rc, "sample", "model-1", "limit"); err == nil {
		t.Fatal("invalid field accepted")
	}
	if _, err := ExplainModelField(nil, "sample", "model-1", ExplainLimitInput); err == nil {
		t.Fatal("nil accepted")
	}
}

func TestExplainHigherLayerSupersedesLeafNull(t *testing.T) {
	for _, tc := range []struct {
		project string
		state   ExplainValueState
		value   any
	}{
		{"        reasoning: null\n", ExplainValueCleared, nil},
		{"        reasoning: {effort: high}\n", ExplainValueSet, "high"},
	} {
		rc := loadExplainFixture(t, explainFixtureModel("        reasoning: {effort: null}\n"), explainFixtureModel(tc.project))
		r := mustExplain(t, rc, ExplainReasoningEffort)
		if r.ValueState != tc.state || r.Value != tc.value || r.SourceStatus != ExplainSourceComplete || r.Source == nil || r.Source.Kind != ExplainSourceProject {
			t.Fatalf("%+v", r)
		}
	}
}
func TestExplainNoIndexDoesNotInventUnset(t *testing.T) {
	rc := loadExplainFixture(t, explainFixtureModel("        limit: {input: 0}\n"), "")
	rc.Index = nil
	r := mustExplain(t, rc, ExplainLimitInput)
	if r.ValueState != ExplainValueUnknown || r.Value != 0 || r.SourceStatus != ExplainSourcePartial || r.Declarations[0].Effect != ExplainEffectObserved || r.EditTarget.Action != ExplainEditAddOverride {
		t.Fatalf("%+v", r)
	}
}
