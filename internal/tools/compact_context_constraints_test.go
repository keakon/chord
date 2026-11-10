package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/jsonx"
)

func TestCompactContextConstraintsLimitsAndBudget(t *testing.T) {
	tool := NewCompactContextTool(CompactContextValidator{})
	property := tool.Parameters()["properties"].(map[string]any)["constraints"].(map[string]any)
	if property["maxItems"] != 8 {
		t.Fatalf("constraints schema limit = %v", property["maxItems"])
	}
	for _, count := range []int{0, 8, 9} {
		args := CompactContextArgs{ActiveObjective: "Inspect the parser.", NextStep: "Read the parser."}
		for i := range count {
			args.Constraints = append(args.Constraints, fmt.Sprintf("Use library%d.", i))
		}
		raw, err := jsonx.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := (CompactContextValidator{}).ParseCompactContextArgs(raw)
		if (err != nil) != (count > 8) || err == nil && len(parsed.Constraints) != count {
			t.Fatalf("count=%d: parsed=%+v err=%v", count, parsed, err)
		}
	}
	raw, err := jsonx.Marshal(CompactContextArgs{ActiveObjective: "a", NextStep: "b", Constraints: []string{strings.Repeat("x", 100)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (CompactContextValidator{ContinuationStateMaxTokens: 50, EstimateTokens: func(text string) int { return len(text) }}).ParseCompactContextArgs(raw)
	if err == nil || !strings.Contains(err.Error(), "constraints") {
		t.Fatalf("constraints missing from budget error: %v", err)
	}
	worker := NewCompactContextTool(CompactContextValidator{WorkerSurface: true})
	if _, _, _, err := SanitizeUnknownArgsWithDiagnostics(worker, raw); err == nil || !strings.Contains(err.Error(), "args.constraints") {
		t.Fatalf("worker accepted constraints: %v", err)
	}
}
