package llm

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestReorderFallbacksByScore(t *testing.T) {
	prov := func(name, model string) *ProviderConfig {
		return NewProviderConfig(name, config.ProviderConfig{
			Type:   config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{model: {}},
		}, []string{"key"})
	}
	fallbacks := []FallbackModel{
		{ProviderConfig: prov("p1", "m1"), ModelID: "m1"},
		{ProviderConfig: prov("p2", "m1"), ModelID: "m1"},
		{ProviderConfig: prov("p4", "m1"), ModelID: "m1"},
		{ProviderConfig: prov("p3", "m2"), ModelID: "m2"},
	}
	scores := map[string]float64{"p1/m1": 1, "p2/m1": 3, "p3/m2": 10, "p4/m1": 2}
	out := reorderFallbacksByScore(fallbacks, func(ref string) float64 { return scores[ref] })

	got := make([]string, 0, len(out))
	for _, fb := range out {
		got = append(got, modelRefWithVariant(fb))
	}
	// Same-model entries are reordered by score, but m2 must not jump ahead of
	// the m1 group despite its higher score: cross-model order is configured.
	want := []string{"p2/m1", "p4/m1", "p1/m1", "p3/m2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}

	if r := reorderFallbacksByScore(fallbacks[:1], func(string) float64 { return 0 }); len(r) != 1 {
		t.Fatal("single entry should pass through")
	}
	if r := reorderFallbacksByScore(fallbacks, nil); len(r) != len(fallbacks) {
		t.Fatal("nil scorer should pass through")
	}
}

func TestReorderFallbacksByScorePreservesConfiguredBoundaries(t *testing.T) {
	provider := func(name, model string, variants map[string]config.ModelVariant) *ProviderConfig {
		return NewProviderConfig(name, config.ProviderConfig{
			Type:   config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{model: {Variants: variants}},
		}, []string{"key"})
	}

	t.Run("different model splits groups", func(t *testing.T) {
		fallbacks := []FallbackModel{
			{ProviderConfig: provider("p1", "m1", nil), ModelID: "m1"},
			{ProviderConfig: provider("quality", "m2", nil), ModelID: "m2"},
			{ProviderConfig: provider("p2", "m1", nil), ModelID: "m1"},
		}
		out := reorderFallbacksByScore(fallbacks, func(ref string) float64 {
			if ref == "p2/m1" {
				return 10
			}
			return 0
		})
		for i, want := range []string{"p1/m1", "quality/m2", "p2/m1"} {
			if got := modelRefWithVariant(out[i]); got != want {
				t.Fatalf("order = %v, want configured boundary at %d (%s)", out, i, want)
			}
		}
	})

	t.Run("different variant splits groups", func(t *testing.T) {
		variants := map[string]config.ModelVariant{"fast": {}, "deep": {}}
		fallbacks := []FallbackModel{
			{ProviderConfig: provider("p1", "m", variants), ModelID: "m", Variant: "fast"},
			{ProviderConfig: provider("p2", "m", variants), ModelID: "m", Variant: "deep"},
		}
		out := reorderFallbacksByScore(fallbacks, func(ref string) float64 {
			if ref == "p2/m@deep" {
				return 10
			}
			return 0
		})
		if got := modelRefWithVariant(out[0]); got != "p1/m@fast" {
			t.Fatalf("different variants reordered: first = %s", got)
		}
	})
}
