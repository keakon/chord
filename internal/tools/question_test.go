package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionArgumentModesAndPolicies(t *testing.T) {
	tests := []struct {
		name, raw string
		valid     bool
	}{
		{"sync", `{"questions":[{"question":"Which?","header":"Choice"}]}`, true},
		{"async", `{"questions":[{"question":"Which?","header":"Choice"}],"wait":false}`, true},
		{"existing", `{"wait_for":["q-1"]}`, true},
		{"padded_wait_id", `{"wait_for":["q-1",""]}`, true},
		{"blank_wait_for_with_questions", `{"questions":[{"question":"Which?","header":"Choice"}],"wait":false,"wait_for":[""]}`, true},
		{"blank_wait_for_only", `{"wait_for":[" "]}`, false},
		{"empty", `{}`, false},
		{"both", `{"questions":[{"question":"Which?","header":"Choice"}],"wait_for":["q-1"]}`, false},
		{"wait_with_existing", `{"wait_for":["q-1"],"wait":true}`, false},
		{"duplicate_wait", `{"wait_for":["q-1","q-1"]}`, false},
		{"required_default", `{"questions":[{"question":"Which?","header":"Choice","default_option_id":"a"}]}`, false},
		{"valid_default", `{"questions":[{"question":"Which?","header":"Choice","response_policy":"default_allowed","default_option_id":"a","options":[{"id":"a","label":"First"}]}]}`, true},
		{"missing_default", `{"questions":[{"question":"Which?","header":"Choice","response_policy":"default_allowed"}]}`, false},
		{"multi_default", `{"questions":[{"question":"Which?","header":"Choice","multiple":true,"response_policy":"default_allowed","default_option_id":"a","options":[{"id":"a","label":"First"}]}]}`, false},
		{"duplicate_id", `{"questions":[{"question":"Which?","header":"Choice","options":[{"id":"a","label":"First"},{"id":"a","label":"Second"}]}]}`, false},
		{"missing_id", `{"questions":[{"question":"Which?","header":"Choice","options":[{"label":"First"}]}]}`, false},
		{"unknown_policy", `{"questions":[{"question":"Which?","header":"Choice","response_policy":"other"}]}`, false},
		{"single_object", `{"questions":{"question":"Which?","header":"Choice"}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeQuestionArgs(json.RawMessage(tt.raw))
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}

// TestDecodeQuestionArgsBlankWaitFor pins the tolerance for the shape models
// kept producing in practice: a valid questions payload padded with an empty
// wait_for placeholder (the call never reached the dialog because the old XOR
// check rejected it). Both error messages must also name the fields involved so
// the model can fix a genuinely ambiguous call.
func TestDecodeQuestionArgsBlankWaitFor(t *testing.T) {
	args, err := DecodeQuestionArgs(json.RawMessage(`{"questions":[{"question":"Which?","header":"Choice"}],"wait":false,"wait_for":[""]}`))
	if err != nil {
		t.Fatalf("blank wait_for placeholder must decode: %v", err)
	}
	if len(args.Questions) != 1 || len(args.WaitFor) != 0 || args.Waits() {
		t.Fatalf("args = %#v, want questions kept, placeholder dropped, async", args)
	}

	args, err = DecodeQuestionArgs(json.RawMessage(`{"wait_for":[" q-1 ",""]}`))
	if err != nil {
		t.Fatalf("padded wait_for must decode: %v", err)
	}
	if len(args.WaitFor) != 1 || args.WaitFor[0] != "q-1" {
		t.Fatalf("WaitFor = %#v, want [q-1]", args.WaitFor)
	}

	_, err = DecodeQuestionArgs(json.RawMessage(`{"questions":[{"question":"Which?","header":"Choice"}],"wait_for":["q-1"]}`))
	if err == nil || !strings.Contains(err.Error(), "questions") || !strings.Contains(err.Error(), "wait_for") {
		t.Fatalf("mutual exclusion error = %v, want both field names", err)
	}

	if _, err = DecodeQuestionArgs(json.RawMessage(`{"wait_for":[""]}`)); err == nil || !strings.Contains(err.Error(), "provide") {
		t.Fatalf("empty error = %v, want provide-message", err)
	}
}

func TestQuestionExecuteAcceptsBlankWaitForPlaceholder(t *testing.T) {
	raw := json.RawMessage(`{"questions":[{"question":"Which?","header":"Choice"}],"wait":false,"wait_for":[""]}`)
	called := false
	tool := NewQuestionTool(func(_ context.Context, args QuestionArgs) (QuestionResult, error) {
		called = true
		if len(args.WaitFor) != 0 || args.Waits() {
			t.Fatalf("args = %#v, want async questions without wait_for", args)
		}
		return QuestionResult{Status: QuestionStatusAccepted, QuestionIDs: []string{"q-1"}}, nil
	})
	// The schema must let the placeholder through: rejecting it in validation
	// would put the dialog back out of reach for the same model shape.
	if err := ValidateToolArgs(tool, raw); err != nil {
		t.Fatalf("schema rejected blank wait_for placeholder: %v", err)
	}
	result, err := tool.Execute(context.Background(), raw)
	if err != nil || !called {
		t.Fatalf("called=%v result=%q err=%v", called, result, err)
	}
}

func TestQuestionExecuteReportsRuntimeResults(t *testing.T) {
	tool := NewQuestionTool(func(_ context.Context, args QuestionArgs) (QuestionResult, error) {
		if args.Waits() {
			t.Fatal("expected async")
		}
		return QuestionResult{Status: QuestionStatusAccepted, QuestionIDs: []string{"q-1"}}, nil
	})
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"questions":[{"question":"Which?","header":"Choice"}],"wait":false}`))
	if err != nil || !strings.Contains(result, QuestionStatusAccepted) || strings.Contains(result, "Which?") {
		t.Fatalf("result=%s err=%v", result, err)
	}
	if _, err = tool.Synchronous().Execute(context.Background(), json.RawMessage(`{"wait_for":["q-1"]}`)); err == nil {
		t.Fatal("worker accepted wait_for")
	}
	if _, err = tool.Synchronous().Execute(context.Background(), json.RawMessage(`{"questions":[{"question":"Which?","header":"Choice"}],"wait":false}`)); err == nil {
		t.Fatal("worker accepted async")
	}
	if _, err = NewQuestionTool(nil).Execute(context.Background(), json.RawMessage(`{"questions":[{"question":"Which?","header":"Choice"}]}`)); err == nil {
		t.Fatal("unavailable callback accepted")
	}
}
func TestQuestionSchemaUsesOneQuestionDefinition(t *testing.T) {
	tool := NewQuestionTool(nil)
	props := tool.Parameters()["properties"].(map[string]any)
	if props["wait"] == nil || props["wait_for"] == nil {
		t.Fatal("missing wait modes")
	}
	sync := tool.Synchronous().Parameters()["properties"].(map[string]any)
	if sync["wait"] != nil || sync["wait_for"] != nil {
		t.Fatal("worker exposes unsupported modes")
	}
	raw := json.RawMessage(`{"questions":[{"question":"Which?","header":"Choice","options":[{"id":"a","label":"First"}]}]}`)
	if err := ValidateToolArgs(tool, raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tool.Description(), "never times out") || !strings.Contains(tool.Description(), "not user consent") {
		t.Fatal("missing required/default boundary")
	}
}
func TestQuestionSizeLimits(t *testing.T) {
	item := QuestionItem{Question: strings.Repeat("x", MaxQuestionTextBytes+1), Header: "Choice"}
	if ValidateQuestionItems([]QuestionItem{item}) == nil {
		t.Fatal("accepted oversized question")
	}
	item.Question = "Which?"
	if ValidateQuestionItems([]QuestionItem{item, item, item, item}) == nil {
		t.Fatal("accepted oversized batch")
	}
}
