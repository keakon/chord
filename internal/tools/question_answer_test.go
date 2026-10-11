package tools

import (
	"reflect"
	"testing"
)

func TestQuestionSelectedOptionsResolveIDsAndKeepAnswerOrder(t *testing.T) {
	item := QuestionItem{Options: []QuestionOption{
		{ID: "first", Label: "Use this", Description: "First destination"},
		{ID: "second", Label: "Use this", Description: "Second destination"},
	}}
	answer := QuestionAnswer{SelectedIDs: []string{"second", "first"}, Selected: []string{"Use this", "Use this"}}
	want := []QuestionOption{item.Options[1], item.Options[0]}
	if got := answer.SelectedOptions(item); !reflect.DeepEqual(got, want) {
		t.Fatalf("selected options=%+v, want %+v", got, want)
	}
	got := answer.SelectedOptions(item)
	got[0].Label = "Changed"
	if item.Options[1].Label != "Use this" || answer.Selected[0] != "Use this" {
		t.Fatal("selection projection mutated the source")
	}
}

func TestQuestionSelectedOptionsDoNotInterpretFreeTextAsIDs(t *testing.T) {
	item := QuestionItem{Options: []QuestionOption{{ID: "first", Label: "Use this"}}}
	if got := (QuestionAnswer{Selected: []string{"first"}}).SelectedOptions(item); len(got) != 0 {
		t.Fatalf("free text was interpreted as an option: %+v", got)
	}
}
