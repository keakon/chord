package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionRestoreDerivesDecisionsWithoutChangingFacts(t *testing.T) {
	for _, tc := range []struct {
		operation string
		outcome   string
	}{
		{QuestionOpAnswer, tools.QuestionOutcomeAnswered},
		{QuestionOpRevise, tools.QuestionOutcomeAnswered},
		{questionOpExpire, tools.QuestionOutcomeDefaulted},
		{questionOpExpire, tools.QuestionOutcomeNoResponse},
	} {
		t.Run(tc.operation+"/"+tc.outcome, func(t *testing.T) {
			fact := QuestionFact{
				Operation: tc.operation,
				Updates:   []QuestionSnapshot{{Item: tools.QuestionItem{Header: "Choice", Question: "Which option?", Options: []tools.QuestionOption{{ID: "yes", Label: "Yes", Description: "First destination"}, {ID: "other", Label: "Yes", Description: "Second destination"}}}}},
				Result:    &tools.QuestionAnswer{QuestionID: "q-1", ResultID: "result-1", Header: "Choice", Outcome: tc.outcome, Selected: []string{"Yes"}, SelectedIDs: []string{"yes"}},
			}
			payload, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			original := []message.Message{{Role: message.RoleUser, Content: `{"selected":["Yes"]}`, Question: payload, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "Unrelated body"}}}}
			restored := restoreQuestionMessages(original)
			msg := restored[0]
			if msg.Role != message.RoleUser || msg.Kind != message.KindQuestionResult || message.IsUserAuthored(msg) || len(msg.Parts) != 0 {
				t.Fatalf("restored decision = %+v", msg)
			}
			for _, want := range []string{"Which option?", "Selected: Yes", "Description: First destination", `Selected option IDs: ["yes"]`, "Question ID: q-1", "Result ID: result-1"} {
				if !strings.Contains(msg.Content, want) {
					t.Fatalf("body %q does not contain %q", msg.Content, want)
				}
			}
			if !reflect.DeepEqual(msg.Question, original[0].Question) || original[0].Kind != "" || len(original[0].Parts) != 1 {
				t.Fatal("restore changed the durable fact or source slice")
			}
			if again := restoreQuestionMessages(restored); !reflect.DeepEqual(again, restored) {
				t.Fatal("decision normalization is not idempotent")
			}
		})
	}
}

func TestQuestionRestorePreservesStateAndUserText(t *testing.T) {
	fact := QuestionFact{Operation: QuestionOpAnswer, Result: &tools.QuestionAnswer{QuestionID: "q-1", Outcome: tools.QuestionOutcomeAnswered}}
	payload, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []message.Message{
		{Role: message.RoleSystem, Kind: message.KindQuestionState, Question: payload},
		{Role: message.RoleUser, Content: "Choose another option"},
		{Role: message.RoleUser, Content: "Malformed metadata", Question: json.RawMessage(`{`)},
	}
	for _, op := range []string{QuestionOpWithdraw, QuestionOpReplace} {
		fact.Operation = op
		fact.SourceID = "input-1"
		payload, err = json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, message.Message{Role: message.RoleUser, Content: "Choose another option", Question: payload})
	}
	if got := restoreQuestionMessages(msgs); !reflect.DeepEqual(got, msgs) {
		t.Fatalf("restore changed state or user text: %+v", got)
	}
}
