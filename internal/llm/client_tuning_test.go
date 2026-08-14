package llm

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestOpenAITuningEffectiveReasoningEffort(t *testing.T) {
	if got := (OpenAITuning{ReasoningEffort: "high"}).EffectiveReasoningEffort(); got != "high" {
		t.Fatalf("unmapped effort = %q, want high", got)
	}
	if got := (OpenAITuning{ReasoningEffort: "high", ReasoningEffortMap: map[string]string{"high": "max"}}).EffectiveReasoningEffort(); got != "max" {
		t.Fatalf("mapped effort = %q, want max", got)
	}
	if got := (OpenAITuning{ReasoningEffort: "high", ReasoningEffortMap: map[string]string{"low": "xhigh"}}).EffectiveReasoningEffort(); got != "high" {
		t.Fatalf("unmatched map must pass through, got %q", got)
	}
	if got := (OpenAITuning{ReasoningEffortMap: map[string]string{"high": "max"}}).EffectiveReasoningEffort(); got != "" {
		t.Fatalf("empty effort must stay empty, got %q", got)
	}
}

func TestTuningFromModelCarriesReasoningEffortMap(t *testing.T) {
	model := config.ModelConfig{
		Reasoning: &config.ReasoningConfig{
			Effort:    "high",
			EffortMap: map[string]string{"high": "max"},
		},
	}
	tuning := tuningFromModel(model, "", nil, nil)
	if got := tuning.OpenAI.EffectiveReasoningEffort(); got != "max" {
		t.Fatalf("model effort = %q, want max", got)
	}

	tuning = mergeVariantTuning(tuning, config.ModelVariant{
		Reasoning: &config.ReasoningConfig{Effort: "low"},
	})
	if got := tuning.OpenAI.EffectiveReasoningEffort(); got != "low" {
		t.Fatalf("variant effort override = %q, want low", got)
	}
	if tuning.OpenAI.ReasoningEffortMap["high"] != "max" {
		t.Fatalf("model map must survive variant effort override: %v", tuning.OpenAI.ReasoningEffortMap)
	}

	tuning = mergeVariantTuning(tuning, config.ModelVariant{
		Reasoning: &config.ReasoningConfig{EffortMap: map[string]string{"low": "xhigh"}},
	})
	if got := tuning.OpenAI.EffectiveReasoningEffort(); got != "xhigh" {
		t.Fatalf("variant map override = %q, want xhigh", got)
	}
}
