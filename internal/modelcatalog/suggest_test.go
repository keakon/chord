package modelcatalog

import (
	"reflect"
	"testing"
)

func TestSuggestModelsRanksFamilyTokens(t *testing.T) {
	suggestions := SuggestModels("gpt-6-sol-messages", 5)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions for a near-catalog wire name")
	}
	if got := suggestions[0].ModelID; got != "openai/gpt-6-sol" {
		t.Fatalf("top suggestion = %q, want openai/gpt-6-sol", got)
	}
	// bestBindingForModel picks the lexicographically smallest endpoint for
	// display; gpt-6-sol is bound to both codex and openai.
	if suggestions[0].Endpoint != "codex" || suggestions[0].WireModel != "gpt-6-sol" {
		t.Fatalf("binding annotation = %s/%s, want codex/gpt-6-sol", suggestions[0].Endpoint, suggestions[0].WireModel)
	}
	var foundNewer bool
	for _, s := range suggestions {
		if s.ModelID == "openai/gpt-6.1-sol" {
			foundNewer = true
		}
	}
	if !foundNewer {
		t.Error("newer same-family generation missing from suggestions")
	}
}

func TestSuggestModelsFiltersDissimilarNames(t *testing.T) {
	for _, query := range []string{"no-such-model-here", "totally-unrelated-thing", ""} {
		if got := SuggestModels(query, 5); len(got) != 0 {
			t.Errorf("SuggestModels(%q) = %d suggestions, want none", query, len(got))
		}
	}
}

func TestSuggestModelsDeterministic(t *testing.T) {
	first := SuggestModels("gpt-6-sol-msg", 5)
	second := SuggestModels("gpt-6-sol-msg", 5)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("suggestion order must be deterministic for the same query")
	}
}

func TestSuggestNewerVersion(t *testing.T) {
	if s, ok := SuggestNewerVersion("gpt-6-sol-messages", "openai/gpt-6-sol"); !ok || s.ModelID != "openai/gpt-6.1-sol" {
		t.Fatalf("newer suggestion = (%q, %v), want openai/gpt-6.1-sol", s.ModelID, ok)
	}
	if s, ok := SuggestNewerVersion("gpt-6.1-sol-alias", "openai/gpt-6.1-sol"); ok {
		t.Fatalf("no newer generation should exist above gpt-6.1-sol, got %q", s.ModelID)
	}
	if _, ok := SuggestNewerVersion("claude-opus-5-5-gateway", "anthropic/claude-sonnet-5-5"); ok {
		t.Error("different families must never count as newer generations of each other")
	}
}

func TestModelVersion(t *testing.T) {
	tests := []struct {
		name string
		want modelGeneration
		ok   bool
	}{
		{"gpt-6.1-sol", modelGeneration{6, 1}, true},
		{"gpt-6-sol", modelGeneration{6, 0}, true},
		{"claude-opus-5-5", modelGeneration{5, 5}, true},
		{"gemini-3.8-flash", modelGeneration{3, 8}, true},
		{"gpt-6-astra", modelGeneration{6, 0}, true},
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
	if got := similarity("gpt-gpt-gpt-6-sol", "openai/gpt-6-sol"); got > 1 {
		t.Fatalf("score outside range: %v", got)
	}
	a, _ := modelVersion("sample-6.10")
	b, _ := modelVersion("sample-6.9")
	if !a.newerThan(b) {
		t.Fatal("6.10 must follow 6.9")
	}
	if sameFamily("anthropic/claude-opus-5-5", "anthropic/claude-sonnet-6") {
		t.Fatal("different families matched")
	}
}
