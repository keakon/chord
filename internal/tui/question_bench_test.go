package tui

import (
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func benchmarkModelForQuestionDialog() Model {
	m := benchmarkModelForView()
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{
			Header:   "Deploy?",
			Question: "Choose an action before continuing.",
			Options: []tools.QuestionOption{{ID: "Ship", Label: "Ship", Description: "Deploy the current build."}, {
				Label:       "Wait",
				Description: "Keep gathering more evidence before deployment.",
			}, {ID: "Abort", Label: "Abort", Description: "Stop and revisit the plan."}},
		}},
		cursor:   1,
		selected: map[int]bool{},
		custom:   false,
		prevMode: ModeInsert,
	}
	return m
}

func BenchmarkRenderQuestionDialogOpen(b *testing.B) {
	m := benchmarkModelForQuestionDialog()
	_ = m.renderQuestionDialog()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderQuestionDialog()
	}
}
