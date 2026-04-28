package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestModelOptionMatchesCurrent(t *testing.T) {
	t.Parallel()
	if modelOptionMatchesCurrent(agent.ModelOption{ProviderModel: "sample/gpt-5.5"}, "sample/gpt-5.5@xhigh") {
		t.Fatal("base option should not be treated as current when current ref has inline variant")
	}
	if !modelOptionMatchesCurrent(agent.ModelOption{ProviderModel: "sample/gpt-5.5@xhigh"}, "sample/gpt-5.5@xhigh") {
		t.Fatal("exact match")
	}
	if modelOptionMatchesCurrent(agent.ModelOption{ProviderModel: "sample/gpt-5.5@high"}, "sample/gpt-5.5@xhigh") {
		t.Fatal("different variants must not both match")
	}
}

func TestBuildModelSelectOptionsPrefersExactVariantRefForCursor(t *testing.T) {
	t.Parallel()
	models := []agent.ModelOption{
		{ProviderModel: "sample/gpt-5.5", ProviderName: "sample", ModelID: "gpt-5.5"},
		{ProviderModel: "sample/gpt-5.5@xhigh", ProviderName: "sample", ModelID: "gpt-5.5"},
	}
	options, cursorRef := buildModelSelectOptions(models, "sample/gpt-5.5@xhigh", "")
	if len(options) < 3 {
		t.Fatalf("len(options) = %d, want at least 3", len(options))
	}
	if cursorRef != "sample/gpt-5.5@xhigh" {
		t.Fatalf("cursorRef = %q, want sample/gpt-5.5@xhigh", cursorRef)
	}
	var exactCurrentCount int
	for _, opt := range options {
		if opt.Header {
			continue
		}
		if opt.IsCurrent {
			exactCurrentCount++
		}
		if opt.Value == "sample/gpt-5.5@xhigh" && opt.Label != "gpt-5.5@xhigh" {
			t.Fatalf("variant option label = %q, want gpt-5.5@xhigh", opt.Label)
		}
	}
	if exactCurrentCount != 1 {
		t.Fatalf("exact current count = %d, want 1", exactCurrentCount)
	}
}

func TestBuildModelSelectOptionsFallsBackToBaseRefWhenExactVariantMissing(t *testing.T) {
	t.Parallel()
	models := []agent.ModelOption{{ProviderModel: "sample/gpt-5.5", ProviderName: "sample", ModelID: "gpt-5.5"}}
	options, cursorRef := buildModelSelectOptions(models, "sample/gpt-5.5@xhigh", "")
	if cursorRef != "sample/gpt-5.5" {
		t.Fatalf("cursorRef = %q, want sample/gpt-5.5", cursorRef)
	}
	if len(options) < 2 || !options[1].IsCurrent {
		t.Fatalf("base option should be used as visible current fallback when exact variant is unavailable")
	}
}

func TestNewModelSelectTableUsesVariantAwareLabelInModelColumn(t *testing.T) {
	t.Parallel()
	tbl := newModelSelectTable([]ModelSelectOption{
		{Header: true, Label: "sample"},
		{Label: "gpt-5.5@xhigh", ModelID: "gpt-5.5", Value: "sample/gpt-5.5@xhigh", IsCurrent: true},
	}, 5)
	if tbl == nil {
		t.Fatal("newModelSelectTable returned nil")
	}
	if len(tbl.items) != 2 {
		t.Fatalf("len(tbl.items) = %d, want 2", len(tbl.items))
	}
	if got := tbl.items[1].Cells[0]; got != "gpt-5.5@xhigh" {
		t.Fatalf("model column = %q, want %q", got, "gpt-5.5@xhigh")
	}
}
