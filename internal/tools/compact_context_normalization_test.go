package tools

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestCompactContextDerivesEvidenceUnion(t *testing.T) {
	raw := json.RawMessage(`{"active_objective":"finish parser","next_step":"run tests","evidence_refs":["ev-a"],"claim_evidence":{"parser checked":["ev-b","ev-a"]},"claim_kinds":{"parser checked":"observed"},"retired_items":[" obsolete decision "]}`)
	args, err := testCompactValidator().ParseCompactContextArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args.EvidenceRefs, []string{"ev-a", "ev-b"}) || !slices.Equal(args.RetiredItems, []string{"obsolete decision"}) {
		t.Fatalf("unexpected normalization: %+v", args)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	again, err := testCompactValidator().ParseCompactContextArgs(encoded)
	if err != nil || !slices.Equal(again.EvidenceRefs, args.EvidenceRefs) {
		t.Fatalf("normalization not idempotent: %+v, %v", again, err)
	}
}

func TestCompactContextDeduplicatesBeforeBudgetCheck(t *testing.T) {
	validator := CompactContextValidator{ContinuationStateMaxTokens: 100}
	args := CompactContextArgs{ActiveObjective: "finish parser", NextStep: "run tests"}
	for range 12 {
		args.Completed = append(args.Completed, "parser syntax verified against the specification")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := validator.ParseCompactContextArgs(encoded)
	if err != nil || len(parsed.Completed) != 1 {
		t.Fatalf("duplicate entries wasted budget: %+v, %v", parsed, err)
	}
}
