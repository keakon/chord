package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionToolDescriptionMentionsUserLanguage(t *testing.T) {
	desc := NewQuestionTool(nil).Description()
	if !strings.Contains(desc, "user's current language") {
		t.Fatalf("Description() missing user language guidance: %q", desc)
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
