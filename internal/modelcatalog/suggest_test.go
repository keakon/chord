package modelcatalog

import (
	"reflect"
	"testing"
)

// suggestionFixture uses synthetic model IDs so catalog refreshes never change
// these unit-test expectations.
func suggestionFixture() *Catalog {
	return &Catalog{
		Models: []ModelFacts{
			{ID: "sample/gpt-6-sol"},
			{ID: "sample/gpt-6.1-sol"},
			{ID: "sample/gpt-6.2-sol"},
			{ID: "sample/gpt-6-astra"},
		},
		Bindings: []Binding{
			{Endpoint: "b-endpoint", WireModelID: "gpt-6-sol", ModelID: "sample/gpt-6-sol"},
			{Endpoint: "a-endpoint", WireModelID: "gpt-6-sol", ModelID: "sample/gpt-6-sol"},
		},
	}
}

func TestSuggestModelsRanksFamilyTokens(t *testing.T) {
	c := suggestionFixture()
	suggestions := suggestModels("gpt-6-sol-messages", 5, c)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions for a near-catalog wire name")
	}
	if got := suggestions[0].ModelID; got != "sample/gpt-6-sol" {
		t.Fatalf("top suggestion = %q, want the exact same-family model", got)
	}
	// bestBindingForModel picks the lexicographically smallest endpoint.
	if suggestions[0].Endpoint != "a-endpoint" || suggestions[0].WireModel != "gpt-6-sol" {
		t.Fatalf("binding annotation = %s/%s, want a-endpoint/gpt-6-sol", suggestions[0].Endpoint, suggestions[0].WireModel)
	}
	for _, newer := range []string{"sample/gpt-6.1-sol", "sample/gpt-6.2-sol"} {
		var found bool
		for _, s := range suggestions {
			if s.ModelID == newer {
				found = true
			}
		}
		if !found {
			t.Errorf("newer same-family generation %s missing from suggestions", newer)
		}
	}
}

func TestSuggestModelsFiltersDissimilarNames(t *testing.T) {
	c := suggestionFixture()
	for _, query := range []string{"no-such-model-here", "totally-unrelated-thing", ""} {
		if got := suggestModels(query, 5, c); len(got) != 0 {
			t.Errorf("suggestModels(%q) = %d suggestions, want none", query, len(got))
		}
	}
}

func TestSuggestModelsDeterministic(t *testing.T) {
	c := suggestionFixture()
	first := suggestModels("gpt-6-sol-msg", 5, c)
	second := suggestModels("gpt-6-sol-msg", 5, c)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("suggestion order must be deterministic for the same query")
	}
}

func TestSuggestNewerVersion(t *testing.T) {
	c := suggestionFixture()
	if s, ok := suggestNewerVersion("gpt-6-sol-alias", "sample/gpt-6-sol", c); !ok || s.ModelID != "sample/gpt-6.1-sol" {
		t.Fatalf("newer suggestion = (%q, %v), want the next same-family generation", s.ModelID, ok)
	}
	if s, ok := suggestNewerVersion("gpt-6.2-sol-alias", "sample/gpt-6.2-sol", c); ok {
		t.Fatalf("no newer generation should exist above the newest fixture, got %q", s.ModelID)
	}
	if s, ok := suggestNewerVersion("gpt-6-astra-alias", "sample/gpt-6-sol", c); ok {
		t.Errorf("different families must never count as newer generations, got %q", s.ModelID)
	}
}

func TestModelVersion(t *testing.T) {
	tests := []struct {
		name string
		want modelGeneration
		ok   bool
	}{
		{"family-6.1", modelGeneration{6, 1}, true},
		{"family-6", modelGeneration{6, 0}, true},
		{"family-5-5", modelGeneration{5, 5}, true},
		{"family-3.8", modelGeneration{3, 8}, true},
		{"family-6-0", modelGeneration{6, 0}, true},
		{"test-model", modelGeneration{0, 0}, false},
		{"x128000", modelGeneration{0, 0}, false},
	}
	for _, tt := range tests {
		got, ok := modelVersion(tt.name)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("modelVersion(%q) = (%v, %v), want (%v, %v)", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSuggestionScoreAndMultiDigitGeneration(t *testing.T) {
	if got := similarity("gpt-gpt-gpt-6-sol", "sample/gpt-6-sol"); got > 1 {
		t.Fatalf("score outside range: %v", got)
	}
	a, _ := modelVersion("sample-6.10")
	b, _ := modelVersion("sample-6.9")
	if !a.newerThan(b) {
		t.Fatal("6.10 must follow 6.9")
	}
	if sameFamily("sample/opus-5-5", "sample/sol-6") {
		t.Fatal("different families matched")
	}
}
