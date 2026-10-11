package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionNestedWaitRejectsCreationParameters(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"question", `"Which option?"`},
		{"header", `"Choice"`},
		{"options", `[{"id":"a","label":"First"}]`},
		{"multiple", `true`},
		{"response_policy", `"default_allowed"`},
		{"default_option_id", `"a"`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			called := false
			tool := NewQuestionTool(func(context.Context, QuestionArgs) (QuestionResult, error) {
				called = true
				return QuestionResult{}, nil
			})
			item := `{"wait_for":["q-1"],"` + tc.field + `":` + tc.value + `}`
			for _, questions := range []string{item, "[" + item + "]"} {
				raw := json.RawMessage(`{"questions":` + questions + `}`)
				if err := ValidateToolArgs(tool, raw); err != nil {
					t.Fatalf("schema rejected conflict before runtime validation: %v", err)
				}
				_, err := tool.Execute(context.Background(), raw)
				if err == nil || !strings.Contains(err.Error(), tc.field) || !strings.Contains(err.Error(), "wait_for") || called {
					t.Fatalf("raw=%s called=%v error=%v", raw, called, err)
				}
			}
		})
	}
}

func TestQuestionNestedWaitNormalizesOnlyEmptyCreationFields(t *testing.T) {
	raw := json.RawMessage(`{"questions":[{"wait_for":["q-1"],"question":" ","header":"","options":[],"multiple":false,"response_policy":"","default_option_id":""}]}`)
	args, err := DecodeQuestionArgs(raw)
	if err != nil || len(args.Questions) != 0 || len(args.WaitFor) != 1 || args.WaitFor[0] != "q-1" {
		t.Fatalf("args=%+v error=%v", args, err)
	}
}
