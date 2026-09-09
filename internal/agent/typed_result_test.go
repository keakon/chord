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

func TestValidateCompleteTypedResultRejectsMismatchedRef(t *testing.T) {
	dir := t.TempDir()
	ref, _, err := tools.SaveImmutableResult(dir, "type/test", json.RawMessage(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := validateCompleteTypedResult(dir, "type/test", json.RawMessage(`{"value":2}`), &ref); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
	if _, _, _, err := validateCompleteTypedResult(dir, "type/other", nil, &ref); err == nil || !strings.Contains(err.Error(), "result_type") {
		t.Fatalf("error = %v", err)
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

// The following two tests migrated back from subagent_completion_validation_test.go
// (deleted with the verification gate in 289a1cd0): the pairing schema and the
// pairing-rejection text are live typed-result behavior.

// TestCompleteParametersSchemaEncodesResultPairing pins the structural form of
// the Complete argument schema: the result fields must form two anyOf groups —
// a summary-only completion that carries no result field, and a typed-result
// completion that pairs result_type with exactly one of result/result_ref — so
// the pairing constraint is visible to the model while it constructs
// arguments. The runtime validator in validateCompleteTypedResult stays as the
// fallback.
func TestCompleteParametersSchemaEncodesResultPairing(t *testing.T) {
	params := (tools.CompleteTool{}).Parameters()
	if got, ok := params["required"].([]string); !ok || !slices.Equal(got, []string{"summary"}) {
		t.Fatalf(`Parameters()["required"] = %#v, want ["summary"]`, params["required"])
	}
	groups, ok := params["anyOf"].([]map[string]any)
	if !ok || len(groups) != 2 {
		t.Fatalf(`Parameters()["anyOf"] = %#v, want the summary-only and typed-result groups`, params["anyOf"])
	}

	summaryGroup := groups[0]
	if got, ok := summaryGroup["required"].([]string); !ok || !slices.Equal(got, []string{"summary"}) {
		t.Fatalf("summary-only group required = %#v, want [summary]", summaryGroup["required"])
	}
	not, ok := summaryGroup["not"].(map[string]any)
	if !ok {
		t.Fatalf("summary-only group = %#v, want not to exclude every result field", summaryGroup)
	}
	forbidden, ok := not["anyOf"].([]map[string]any)
	if !ok || len(forbidden) != 3 {
		t.Fatalf(`summary-only group not["anyOf"] = %#v, want result_type/result/result_ref`, not["anyOf"])
	}
	for i, want := range [][]string{{"result_type"}, {"result"}, {"result_ref"}} {
		if got, ok := forbidden[i]["required"].([]string); !ok || !slices.Equal(got, want) {
			t.Fatalf("summary-only group not.anyOf[%d] required = %#v, want %v", i, forbidden[i]["required"], want)
		}
	}

	typedGroup := groups[1]
	if got, ok := typedGroup["required"].([]string); !ok || !slices.Equal(got, []string{"summary", "result_type"}) {
		t.Fatalf("typed-result group required = %#v, want [summary result_type]", typedGroup["required"])
	}
	pair, ok := typedGroup["anyOf"].([]map[string]any)
	if !ok || len(pair) != 2 {
		t.Fatalf(`typed-result group anyOf = %#v, want the result and result_ref alternatives`, typedGroup["anyOf"])
	}
	for i, want := range [][]string{{"result"}, {"result_ref"}} {
		if got, ok := pair[i]["required"].([]string); !ok || !slices.Equal(got, want) {
			t.Fatalf("typed-result group anyOf[%d] required = %#v, want %v", i, pair[i]["required"], want)
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
