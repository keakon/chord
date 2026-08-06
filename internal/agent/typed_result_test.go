package agent

import (
	"encoding/json"
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
	if _, _, _, err := validateCompleteTypedResult(dir, "type/test", large, nil); err == nil || !strings.Contains(err.Error(), "use save_result") {
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

func TestTypedResultSettlementCollectReturnsRefWithoutInlinePayload(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ref, inline, err := tools.SaveImmutableResult(a.sessionDir, "type/test", json.RawMessage(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	settlement := testSettlement("task-a", 1, 2)
	settlement.Completion = &CompletionEnvelope{Summary: "done", ResultType: "type/test", Result: inline, ResultRef: &ref}
	settlement.ResultRef = &ref
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateCompleted)}, settlement, true)
	result, err := a.CollectTasks(t.Context(), tools.TaskCollectRequest{TaskIDs: []string{"task-a"}})
	if err != nil {
		t.Fatal(err)
	}
	item := result.Tasks[0]
	if item.ResultType != "type/test" || item.ResultRef == nil || item.ResultRef.ID != ref.ID {
		t.Fatalf("collect item = %#v", item)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"result":`) || strings.Contains(string(encoded), `"value":1`) {
		t.Fatalf("collect expanded inline result: %s", encoded)
	}
}
