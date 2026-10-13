package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestExplainOriginalLocationsAndPaths(t *testing.T) {
	global := strings.ReplaceAll(explainFixtureModel("        reasoning: {effort: high}\n"), "sample:", "sample.v1:")
	global = strings.ReplaceAll(global, "model-1:", "model.v1/part:")
	project := strings.ReplaceAll(global, "{effort: high}", "\n          effort: [invalid]\n          summary: auto\n")
	project = "# heading\n\n" + project + "unknown_top_level: true\n"
	rc := loadExplainFixture(t, global, project)
	r, err := ExplainModelField(rc, "sample.v1", "model.v1/part", ExplainReasoningEffort)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"providers", "sample.v1", "models", "model.v1/part", "reasoning", "effort"}
	if r.SourceStatus != ExplainSourceComplete || r.Value != "high" || len(r.Diagnostics) != 1 || !slices.Equal(r.Diagnostics[0].PathSegments, want) || r.Diagnostics[0].Line != 9 {
		t.Fatalf("%+v", r)
	}
	if r.Declarations[1].Source.Line != 9 || r.Source.Line != 6 {
		t.Fatalf("locations: %+v", r)
	}
	// An unrelated error before the target forces YAML reserialization, but must
	// not move the target's reported original position.
	project = "# heading\nunknown_top_level: true\n\n" + global
	rc = loadExplainFixture(t, global, project)
	r, err = ExplainModelField(rc, "sample.v1", "model.v1/part", ExplainReasoningEffort)
	if err != nil || r.Source.Line != 9 || r.Source.Kind != ExplainSourceProject || len(r.Diagnostics) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestExplainSharedYAML(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		inherited   bool
		value       string
	}{
		{"alias", "*shared", true, "high"},
		{"merge", "{<<: *shared}", true, "high"},
		{"explicit", "{<<: *shared, reasoning: {effort: low}}", false, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := "model_templates:\n  shared: &shared\n    reasoning: {effort: high}\nproviders:\n  sample:\n    type: responses\n    models:\n      model-1: " + tc.model + "\n"
			rc := loadExplainFixture(t, data, "")
			r := mustExplain(t, rc, ExplainReasoningEffort)
			if r.Value != tc.value || r.SourceStatus != ExplainSourceComplete || r.Source == nil || r.Source.Inherited != tc.inherited {
				t.Fatalf("%+v", r)
			}
			want := ExplainEditReplace
			if tc.inherited {
				want = ExplainEditAddOverride
			}
			if r.EditTarget.Action != want || (tc.inherited && r.EditTarget.Line != 0) {
				t.Fatalf("%+v", r.EditTarget)
			}
		})
	}
}
func TestExplainIgnoredAncestorsAndGlobalDiagnostics(t *testing.T) {
	for _, project := range []string{
		"providers: [invalid]\n",
		"providers:\n  sample: [invalid]\n",
		"providers:\n  sample:\n    models: [invalid]\n",
		"providers:\n  sample:\n    models:\n      model-1: [invalid]\n",
	} {
		rc := loadExplainFixture(t, explainFixtureModel("        reasoning: {effort: high}\n"), project)
		r := mustExplain(t, rc, ExplainReasoningEffort)
		if r.Value != "high" || len(r.Diagnostics) != 1 || len(r.Declarations) != 2 || r.Declarations[1].Effect != ExplainEffectIgnored {
			t.Fatalf("%+v", r)
		}
	}
	rc := loadExplainFixture(t, explainFixtureModel("        reasoning: {effort: [invalid]}\n"), "")
	r := mustExplain(t, rc, ExplainReasoningEffort)
	if r.ValueState != ExplainValueUnset || len(r.Diagnostics) != 1 || len(r.Diagnostics[0].PathSegments) != 6 || r.Declarations[0].Effect != ExplainEffectIgnored {
		t.Fatalf("%+v", r)
	}
}
func TestExplainCollectionFailureAndTypeLocator(t *testing.T) {
	e := newConfigExplainEvidence("global.yaml", "")
	e.collect(ConfigLayer{File: "global.yaml", Layer: OriginLayerGlobal, Data: []byte("providers:\n  sample: {}\n  sample: {}\n")}, []byte("providers: {}"), nil)
	if len(e.layers) != 1 || e.layers[0].complete {
		t.Fatal("duplicate keys should make evidence partial")
	}
	e.collect(ConfigLayer{File: "bad.yaml", Data: []byte("[")}, nil, nil)
	if e.layers[1].complete {
		t.Fatal("malformed YAML should make evidence partial")
	}
	l := newTypeIssueLocator([]byte("providers:\n  sample:\n    models:\n      model-1:\n        limit: {input: [invalid]}\n"), true)
	if got := l.path("line 5: cannot unmarshal !!seq into int"); !slices.Equal(got, []string{"providers", "sample", "models", "model-1", "limit", "input"}) {
		t.Fatalf("path: %v", got)
	}
	if got := l.path("unrecognized failure"); got != nil {
		t.Fatalf("ambiguous path: %v", got)
	}
	l = newTypeIssueLocator([]byte("a: [invalid]\nb: [invalid]\n"), true)
	if got := l.path("line 1: cannot unmarshal !!seq into int"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("path: %v", got)
	}
}
func TestExplainEvidenceDoesNotRetainInvalidValues(t *testing.T) {
	rc := loadExplainFixture(t, explainFixtureModel("        limit: {input: secret-invalid-number}\n"), "")
	m := rc.ExplainEvidence.Providers["sample"]["model-1"]
	for _, raw := range m.Fields[ExplainLimitInput] {
		if len(raw.source.PathSegments) == 6 && (!raw.ignored || raw.scalar != nil) {
			t.Fatalf("invalid scalar retained: %+v", raw)
		}
	}
	r := mustExplain(t, rc, ExplainLimitInput)
	// Existing loader messages may quote a bad scalar; the evidence itself must
	// not include that scalar as an accepted declaration value.
	for _, decl := range r.Declarations {
		data, _ := json.Marshal(decl.Value)
		if string(data) != "null" {
			t.Fatalf("invalid declaration value: %s", data)
		}
	}
}
