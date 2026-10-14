package llm

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestGenerateContentProviderSnapshot(t *testing.T) {
	source := config.ProviderConfig{Compat: &config.ProviderCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(true), SingleRequestOutputLimit: new(65536)}}, Models: map[string]config.ModelConfig{"model-alias": {Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(false)}}}}}
	provider := NewProviderConfig("sample", source, nil)
	*source.Compat.GenerateContent.AutomaticContinuation = false
	*source.Models["model-alias"].Compat.GenerateContent.AutomaticContinuation = true
	for i := range 5 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			t.Parallel()
			got := provider.GenerateContentCompat("model-alias")
			if *got.AutomaticContinuation || *got.SingleRequestOutputLimit != 65536 {
				t.Fatal("snapshot changed")
			}
			*got.AutomaticContinuation = true
			if !*provider.GenerateContentCompat("unknown").AutomaticContinuation {
				t.Fatal("default changed")
			}
		})
	}
}
func TestGenerateContentBudget(t *testing.T) {
	for _, enabled := range []*bool{nil, new(false), new(true)} {
		compat := &config.GenerateContentCompatConfig{AutomaticContinuation: enabled, SingleRequestOutputLimit: new(65536)}
		want := 65536
		if enabled != nil && *enabled {
			want = 262144
		}
		if got := constrainGenerateContentOutputBudget(262144, compat); got != want {
			t.Fatalf("budget=%d", got)
		}
		if got := constrainGenerateContentOutputBudget(32768, compat); got != 32768 {
			t.Fatalf("small budget=%d", got)
		}
	}
}
