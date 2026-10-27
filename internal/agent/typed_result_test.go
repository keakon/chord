package agent

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestValidateCompleteTypedResultPersistsArbitraryObject(t *testing.T) {
	dir := t.TempDir()
	resultType, inline, ref, err := validateCompleteTypedResult(dir, "application/vnd.example.analysis+json", json.RawMessage(`{"confidence":0.86,"candidates":[]}`), nil)
	if err != nil {
		t.Fatalf("validateCompleteTypedResult: %v", err)
	}
	if resultType != "application/vnd.example.analysis+json" || string(inline) != `{"candidates":[],"confidence":0.86}` || ref == nil || ref.ResultType != resultType {
		t.Fatalf("typed result = (%q, %s, %#v)", resultType, inline, ref)
	}
	if _, err := tools.ValidateResultRef(dir, *ref, resultType); err != nil {
		t.Fatalf("ValidateResultRef: %v", err)
	}
}

func TestValidateCompleteTypedResultAcceptsRefOnly(t *testing.T) {
	dir := t.TempDir()
	ref, _, err := tools.SaveImmutableResult(dir, "type/test", json.RawMessage(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resultType, inline, got, err := validateCompleteTypedResult(dir, "type/test", nil, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if resultType != "type/test" || len(inline) != 0 || got == nil || got.ID != ref.ID {
		t.Fatalf("typed result = (%q, %s, %#v)", resultType, inline, got)
	}
}

func TestValidateCompleteTypedResultRejectsInvalidShapesAndSize(t *testing.T) {
	dir := t.TempDir()
	for _, raw := range []string{`[1]`, `"text"`, `null`} {
		if _, _, _, err := validateCompleteTypedResult(dir, "type/test", json.RawMessage(raw), nil); err == nil || !strings.Contains(err.Error(), "JSON object") {
			t.Fatalf("input %s error = %v", raw, err)
		}
	}
	large := json.RawMessage(`{"value":"` + strings.Repeat("x", tools.MaxInlineResultBytes) + `"}`)
	if _, _, _, err := validateCompleteTypedResult(dir, "type/test", large, nil); err == nil || !strings.Contains(err.Error(), "use save_artifact") {
		t.Fatalf("large result error = %v", err)
	}
}

func TestValidateCompleteTypedResultRejectsBothInlineAndRef(t *testing.T) {
	dir := t.TempDir()
	ref, _, err := tools.SaveImmutableResult(dir, "type/test", json.RawMessage(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, inline := range map[string]json.RawMessage{
		"same":      json.RawMessage(`{"value":1}`),
		"different": json.RawMessage(`{"value":2}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := validateCompleteTypedResult(dir, "type/test", inline, &ref); err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Fatalf("error = %v, want exactly-one rejection", err)
			}
		})
	}
	if _, _, _, err := validateCompleteTypedResult(dir, "type/other", nil, &ref); err == nil || !strings.Contains(err.Error(), "result_type") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompleteToolExecuteRejectsBothInlineAndRef(t *testing.T) {
	raw := json.RawMessage(`{"summary":"done","result_type":"type/test","result":{"value":1},"result_ref":{"id":"ref-1"}}`)
	if _, err := (tools.CompleteTool{}).Execute(t.Context(), raw); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("Execute error = %v, want exactly-one rejection", err)
	}
}

func TestCompletionEnvelopeLegacyJSONStillRestores(t *testing.T) {
	var env CompletionEnvelope
	if err := json.Unmarshal([]byte(`{"summary":"legacy","files_changed":["a.go"]}`), &env); err != nil {
		t.Fatal(err)
	}
	got := normalizeCompletionEnvelope(&env)
	if got == nil || got.Summary != "legacy" || got.ResultType != "" || got.ResultRef != nil || len(got.Result) != 0 {
		t.Fatalf("envelope = %#v", got)
	}
}

// The following two tests cover the live typed-result contract: the pairing
// schema and its rejection text.

// TestCompleteParametersSchemaStatesResultPairing pins how the Complete
// argument schema exposes the pairing: no top-level combinator, because strict
// Anthropic endpoints reject one, with the rule carried by the field
// descriptions. The runtime validator in validateCompleteTypedResult stays as
// the enforcement.
func TestCompleteParametersSchemaStatesResultPairing(t *testing.T) {
	params := (tools.CompleteTool{}).Parameters()
	if got, ok := params["required"].([]string); !ok || !slices.Equal(got, []string{"summary"}) {
		t.Fatalf(`Parameters()["required"] = %#v, want ["summary"]`, params["required"])
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "not"} {
		if value, ok := params[key]; ok {
			t.Fatalf(`Parameters()[%q] = %#v, want no top-level combinator`, key, value)
		}
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf(`Parameters()["properties"] = %#v`, params["properties"])
	}
	description := func(field string) string {
		schema, ok := properties[field].(map[string]any)
		if !ok {
			t.Fatalf("Parameters() property %q = %#v", field, properties[field])
		}
		desc, _ := schema["description"].(string)
		return desc
	}
	if got := description("result_type"); !strings.Contains(got, "exactly one of result or result_ref") {
		t.Fatalf("result_type description = %q", got)
	}
	for _, field := range []string{"result", "result_ref"} {
		if got := description(field); !strings.Contains(got, "result_type is also provided") {
			t.Fatalf("%s description = %q", field, got)
		}
	}
}

// A rejection the model cannot place is a rejection it repeats: the observed
// failure mode is a worker answering "result_type is required" by nesting
// result_type inside result and failing the identical check twice. The message
// therefore has to name the level the fields live at and the way out for
// completions that carry no machine-readable result.
func TestTypedResultPairingRejectionNamesTopLevelShapeAndOptOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "result without result_type", args: map[string]any{"summary": "done", "result": map[string]any{"value": 1}}},
		{name: "result_type without result", args: map[string]any{"summary": "done", "result_type": "type/test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var args struct {
				ResultType string           `json:"result_type"`
				Result     json.RawMessage  `json:"result"`
				ResultRef  *tools.ResultRef `json:"result_ref"`
			}
			raw, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				t.Fatalf("unmarshal args: %v", err)
			}
			_, _, _, err = validateCompleteTypedResult(t.TempDir(), args.ResultType, args.Result, args.ResultRef)
			if err == nil {
				t.Fatal("validateCompleteTypedResult() = nil, want a pairing rejection")
			}
			if _, ok := errors.AsType[typedResultPairingError](err); !ok {
				t.Fatalf("error %#v is not a typedResultPairingError; the degraded delivery path keys off that type", err)
			}
			for _, want := range []string{"top-level Complete arguments", "not fields inside result", "omit result_type, result and result_ref entirely"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("rejection %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}
