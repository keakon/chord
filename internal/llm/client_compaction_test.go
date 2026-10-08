package llm

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestCompactionBudgetsStayFixedAcrossRequestOutputCaps(t *testing.T) {
	primary := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 400000, Output: 128000}},
		},
	}, []string{"key"})
	fallback := NewProviderConfig("alternate", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-b": {Limit: config.ModelLimit{Context: 100000, Output: 20000}},
		},
	}, []string{"key"})
	client := NewClient(primary, &scriptedProvider{}, "model-a", 128000, "")
	t.Cleanup(client.Close)
	client.fallbackModels = []FallbackModel{{ProviderConfig: fallback, ModelID: "model-b", ContextLimit: 100000, DeriveInputLimit: true}}
	for _, cap := range []int{0, 8192, 128000} {
		client.SetOutputTokenMax(cap)
		for _, tc := range []struct {
			ref  string
			want int
		}{{"", 272000}, {"sample/model-a", 272000}, {"alternate/model-b", 80000}, {"missing/model", 0}} {
			if got := client.CompactionBudgetForModelRef(tc.ref); got != tc.want {
				t.Fatalf("output cap %d, model %q: compaction budget = %d, want %d", cap, tc.ref, got, tc.want)
			}
		}
	}
	client.SetOutputTokenMax(8192)
	if got := client.InputLimitForModelRef("sample/model-a"); got != 391808 {
		t.Fatalf("request input budget = %d, want 391808", got)
	}
}

func TestFallbackCompactionBudgetWithoutModelFacts(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"unknown": {},
		},
	}, []string{"key"})
	for _, tc := range []struct {
		model FallbackModel
		want  int
	}{
		{FallbackModel{ContextLimit: 400000, InputLimit: 300000}, 300000},
		{FallbackModel{ContextLimit: 400000, InputLimit: 336000, DeriveInputLimit: true}, 400000},
		{FallbackModel{ProviderConfig: provider, ModelID: "unknown", ContextLimit: 400000, InputLimit: 300000}, 300000},
		{FallbackModel{ProviderConfig: provider, ModelID: "unknown", ContextLimit: 400000, InputLimit: 336000, DeriveInputLimit: true}, 400000},
		{FallbackModel{}, 0},
	} {
		if got := tc.model.CompactionBudget(); got != tc.want {
			t.Fatalf("CompactionBudget() = %d, want %d", got, tc.want)
		}
	}
}
