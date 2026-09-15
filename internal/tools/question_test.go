package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The user-language rule lives in the shared Values prompt and the per-field
// parameter descriptions (where the text is actually written); the tool
// description no longer repeats it.
func TestQuestionToolDescriptionOmitsDuplicatedLanguageRule(t *testing.T) {
	desc := NewQuestionTool(nil).Description()
	if strings.Contains(desc, "user's current language") {
		t.Fatalf("Description() should not duplicate the parameter-level language guidance: %q", desc)
	}
}

// The Guidelines and dynamic capability/confirmation prompt blocks own the
// decision threshold and routing for questions. Keep the tool description
// focused on the callable tool contract so those policies have one source.
func TestQuestionToolDescriptionKeepsPolicyInPromptBlocks(t *testing.T) {
	desc := NewQuestionTool(nil).Description()
	for _, unwanted := range []string{
		"When to use:",
		"When NOT to use:",
		"scope, permissions, risk, or implementation choice",
		"plain assistant text",
		"easy for a non-implementer to answer",
		"tradeoffs/risks",
	} {
		if strings.Contains(desc, unwanted) {
			t.Fatalf("Description() should not own policy %q: %q", unwanted, desc)
		}
	}
}

func TestQuestionToolParametersMentionUserLanguage(t *testing.T) {
	params := NewQuestionTool(nil).Parameters()

	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has unexpected type %T", params["properties"])
	}
	questions, ok := properties["questions"].(map[string]any)
	if !ok {
		t.Fatalf("questions has unexpected type %T", properties["questions"])
	}
	if desc, _ := questions["description"].(string); !strings.Contains(desc, "user's current language") {
		t.Fatalf("questions.description missing user language guidance: %q", desc)
	}

	items, ok := questions["items"].(map[string]any)
	if !ok {
		t.Fatalf("questions.items has unexpected type %T", questions["items"])
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("items.properties has unexpected type %T", items["properties"])
	}

	for _, key := range []string{"question", "header"} {
		prop, ok := itemProps[key].(map[string]any)
		if !ok {
			t.Fatalf("%s has unexpected type %T", key, itemProps[key])
		}
		if desc, _ := prop["description"].(string); !strings.Contains(desc, "user's current language") {
			t.Fatalf("%s.description missing user language guidance: %q", key, desc)
		}
	}

	options, ok := itemProps["options"].(map[string]any)
	if !ok {
		t.Fatalf("options has unexpected type %T", itemProps["options"])
	}
	if desc, _ := options["description"].(string); !strings.Contains(desc, "user's current language") {
		t.Fatalf("options.description missing user language guidance: %q", desc)
	}

	optionItems, ok := options["items"].(map[string]any)
	if !ok {
		t.Fatalf("options.items has unexpected type %T", options["items"])
	}
	optionProps, ok := optionItems["properties"].(map[string]any)
	if !ok {
		t.Fatalf("options.items.properties has unexpected type %T", optionItems["properties"])
	}
	for _, key := range []string{"label", "description"} {
		prop, ok := optionProps[key].(map[string]any)
		if !ok {
			t.Fatalf("option %s has unexpected type %T", key, optionProps[key])
		}
		if desc, _ := prop["description"].(string); !strings.Contains(desc, "user's current language") {
			t.Fatalf("option %s.description missing user language guidance: %q", key, desc)
		}
	}
}

func TestQuestionToolParametersOptInToObjectCoercion(t *testing.T) {
	params := NewQuestionTool(nil).Parameters()
	properties, _ := params["properties"].(map[string]any)
	questions, _ := properties["questions"].(map[string]any)
	if coerce, _ := questions["coerceFromObject"].(bool); !coerce {
		t.Fatalf("questions schema should opt in to coerceFromObject, got %v", questions["coerceFromObject"])
	}
}

func TestQuestionToolLabelLengthsAreGuidance(t *testing.T) {
	tool := NewQuestionTool(func(_ context.Context, questions []QuestionItem) ([]QuestionAnswer, error) {
		if len(questions) != 1 || len(questions[0].Header) <= 30 || len(strings.Fields(questions[0].Options[0].Label)) <= 5 {
			t.Fatalf("unexpected questions: %+v", questions)
		}
		return []QuestionAnswer{{Header: questions[0].Header, Selected: []string{questions[0].Options[0].Label}}}, nil
	})
	properties := tool.Parameters()["properties"].(map[string]any)
	items := properties["questions"].(map[string]any)["items"].(map[string]any)
	fields := items["properties"].(map[string]any)
	header := fields["header"].(map[string]any)
	options := fields["options"].(map[string]any)["items"].(map[string]any)
	label := options["properties"].(map[string]any)["label"].(map[string]any)
	if !strings.Contains(header["description"].(string), "aim for 30 characters or fewer") {
		t.Fatalf("header must state a recommendation: %v", header)
	}
	if _, hardLimit := header["maxLength"]; hardLimit || strings.Contains(label["description"].(string), "1-5 words") {
		t.Fatalf("length guidance must not imply runtime limits: header=%v label=%v", header, label)
	}
	raw := json.RawMessage(`{"questions":[{"question":"Which option should be selected?","header":"Choose how to organize the generated report","options":[{"label":"Keep all related items in one report","description":"Group the related items together."}]}]}`)
	if _, err := tool.Execute(context.Background(), raw); err != nil {
		t.Fatalf("length recommendations must not reject valid questions: %v", err)
	}
}

// Both the question dialog and the answered tool card render label-only
// choices, so an omitted option description must not fail validation and cost
// a model retry.
func TestQuestionToolAcceptsLabelOnlyOptions(t *testing.T) {
	raw := json.RawMessage(`{"questions":[{"question":"Which strategy should be used?","header":"Strategy","options":[{"label":"Additive"},{"label":"Replace"}]}]}`)
	if err := ValidateToolArgs(NewQuestionTool(nil), raw); err != nil {
		t.Fatalf("label-only options should pass validation: %v", err)
	}

	// The label stays mandatory, and a non-string description still fails the
	// type check.
	missingLabel := json.RawMessage(`{"questions":[{"question":"q","header":"h","options":[{"description":"d"}]}]}`)
	if err := ValidateToolArgs(NewQuestionTool(nil), missingLabel); err == nil || !strings.Contains(err.Error(), "args.questions[0].options[0].label is required") {
		t.Fatalf("missing label should still fail, got %v", err)
	}
	wrongType := json.RawMessage(`{"questions":[{"question":"q","header":"h","options":[{"label":"a","description":7}]}]}`)
	if err := ValidateToolArgs(NewQuestionTool(nil), wrongType); err == nil || !strings.Contains(err.Error(), "args.questions[0].options[0].description must be a string") {
		t.Fatalf("non-string description should still fail, got %v", err)
	}
}

func TestQuestionToolExecuteAcceptsSingleObject(t *testing.T) {
	var received []QuestionItem
	tool := NewQuestionTool(func(_ context.Context, qs []QuestionItem) ([]QuestionAnswer, error) {
		received = qs
		out := make([]QuestionAnswer, len(qs))
		for i, q := range qs {
			out[i] = QuestionAnswer{Header: q.Header, Selected: []string{"ok"}}
		}
		return out, nil
	})

	// A single question object instead of the documented array is coerced into
	// a one-element list and executes normally.
	raw := json.RawMessage(`{"questions":{"header":"h","question":"q?"}}`)
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute with single object: %v", err)
	}
	if len(received) != 1 || received[0].Header != "h" {
		t.Fatalf("callback received %+v, want one question with header h", received)
	}

	// Result stays a clean JSON array of answers (no inline note).
	var answers []QuestionAnswer
	if err := json.Unmarshal([]byte(out), &answers); err != nil {
		t.Fatalf("result is not a clean answers array: %v (out=%q)", err, out)
	}
	if len(answers) != 1 || answers[0].Header != "h" {
		t.Fatalf("answers = %+v, want one answer for header h", answers)
	}
}
