package main

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

func TestEditConfigYAMLForAddCreatesProviderModelAndPool(t *testing.T) {
	current := []byte(`# my chord config
providers:
  openai:
    preset: openai
model_pools:
  default:
    - openai/gpt-6.1-sol
`)
	edited, err := editConfigYAMLForAdd(current, configAddEdit{
		providerName: "gw",
		providerNew:  true,
		providerType: "messages",
		apiURL:       "https://gateway.example.com/v1/messages",
		wireModel:    "claude-gw",
		borrowID:     "anthropic/claude-opus-5-5",
		poolName:     "default",
		poolRef:      "gw/claude-gw",
	})
	if err != nil {
		t.Fatalf("editConfigYAMLForAdd: %v", err)
	}
	text := string(edited)
	for _, want := range []string{
		"# my chord config", // comments survive the node round-trip
		"preset: openai",
		"gw:",
		"type: messages",
		"api_url: https://gateway.example.com/v1/messages",
		"claude-gw:",
		"catalog: anthropic/claude-opus-5-5",
		"gw/claude-gw",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("edited config missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "- openai/gpt-6.1-sol") || strings.Index(text, "gw/claude-gw") < strings.Index(text, "openai/gpt-6.1-sol") {
		t.Errorf("existing pool entries must stay ahead of the appended reference:\n%s", text)
	}
}

func TestEditConfigYAMLForAddAppendsPoolReferenceIdempotently(t *testing.T) {
	current := []byte(`model_pools:
  default:
    - gw/claude-gw
`)
	edit := configAddEdit{providerName: "gw", wireModel: "claude-gw", poolName: "default", poolRef: "gw/claude-gw"}
	edited, err := editConfigYAMLForAdd(current, edit)
	if err != nil {
		t.Fatalf("editConfigYAMLForAdd: %v", err)
	}
	if got := strings.Count(string(edited), "gw/claude-gw"); got != 1 {
		t.Fatalf("reference count = %d, want 1 (no duplicate appends):\n%s", got, edited)
	}
}

func TestEditConfigYAMLForAddCreatesPool(t *testing.T) {
	current := []byte("providers: {}\n")
	edited, err := editConfigYAMLForAdd(current, configAddEdit{
		providerName: "gw", providerNew: true, providerType: "responses",
		apiURL:    "https://example.invalid/v1/responses",
		wireModel: "m1", borrowID: "openai/gpt-6-sol",
		poolName: "secondary", poolRef: "gw/m1",
	})
	if err != nil {
		t.Fatalf("editConfigYAMLForAdd: %v", err)
	}
	if !strings.Contains(string(edited), "secondary:") || !strings.Contains(string(edited), "gw/m1") {
		t.Errorf("missing pool creation:\n%s", edited)
	}
}

func TestEditConfigYAMLForAddPreservesUntouchedAnchors(t *testing.T) {
	current := []byte(`providers:
  openai: &base
    preset: openai
  secondary:
    <<: *base
model_pools:
  default:
    - openai/gpt-6.1-sol
`)
	edited, err := editConfigYAMLForAdd(current, configAddEdit{
		providerName: "gw", wireModel: "m", borrowID: "openai/gpt-6-sol",
		poolName: "default", poolRef: "gw/m",
	})
	if err != nil || !strings.Contains(string(edited), "&base") {
		t.Fatalf("untouched anchor must survive editing: %s, %v", edited, err)
	}
	if strings.Contains(string(edited), "!!merge") || !strings.Contains(string(edited), "<<: *base") {
		t.Fatalf("merge key spelling changed: %s", edited)
	}
}

func TestResolveConfigAddMode(t *testing.T) {
	mode, borrowID, err := resolveConfigAddMode("openai", "gpt-6-sol", "")
	if err != nil || mode != configAddExact || borrowID != "" {
		t.Fatalf("preset-bound wire name = (%v, %q, %v), want exact with no borrow", mode, borrowID, err)
	}
	mode, borrowID, err = resolveConfigAddMode("", "claude-gw", "anthropic/claude-opus-5-5")
	if err != nil || mode != configAddBorrow || borrowID != "anthropic/claude-opus-5-5" {
		t.Fatalf("custom endpoint borrow = (%v, %q, %v)", mode, borrowID, err)
	}
	if _, _, err := resolveConfigAddMode("", "claude-gw", ""); err == nil {
		t.Fatal("unmatched wire name without --catalog must fail")
	}
	if _, _, err := resolveConfigAddMode("", "claude-gw", "nope/missing"); err == nil {
		t.Fatal("unknown catalog ID must fail")
	}
	// Explicit catalog choices must be served by the selected preset.
	if _, _, err := resolveConfigAddMode("openai", "gpt-6-sol-mirror", "anthropic/claude-opus-5-5"); err == nil {
		t.Fatal("catalog model not bound to the preset must fail")
	}
}

func TestPrintCatalogSuggestionsListsAndGuides(t *testing.T) {
	var out strings.Builder
	if !printCatalogSuggestions(&out, "gpt-6-sol-messages", "", "") {
		t.Fatal("expected suggestions for a near-catalog wire name")
	}
	text := out.String()
	if !strings.Contains(text, "openai/gpt-6-sol") || !strings.Contains(text, "--catalog openai/gpt-6-sol") {
		t.Errorf("suggestion output must list models and the adopt command:\n%s", text)
	}
	out.Reset()
	if printCatalogSuggestions(&out, "no-such-model-here", "", "") {
		t.Error("below-threshold queries must report no suggestions")
	}
}

func TestCatalogScopeMatches(t *testing.T) {
	cases := []struct {
		scope, preset, apiURL string
		want                  bool
	}{
		{"openai", "openai", "", true},
		{"OpenAI", "openai", "", true},
		{"gateway-a", "", "https://api.gateway-a.example.com/v1/messages", true},
		{"gateway-a", "", "https://api.other.example.com/v1/messages", false},
		{"", "openai", "", false},
		{"openai", "", "", false},
	}
	for _, tc := range cases {
		if got := catalogScopeMatches(tc.scope, tc.preset, tc.apiURL); got != tc.want {
			t.Errorf("catalogScopeMatches(%q, %q, %q) = %v, want %v", tc.scope, tc.preset, tc.apiURL, got, tc.want)
		}
	}
}

func TestCatalogCandidateOrderPrefersMatchingScope(t *testing.T) {
	candidates := []modelcatalog.CandidateSuggestion{
		{Candidate: modelcatalog.Candidate{WireModelID: "w", Scope: "far-gateway"}, Score: 0.9},
		{Candidate: modelcatalog.Candidate{WireModelID: "w", Scope: "openai"}, Score: 0.9},
	}
	ordered := catalogCandidateOrder(candidates, "openai", "")
	if ordered[0].Candidate.Scope != "openai" {
		t.Fatalf("the scope matching the endpoint must be listed first: %+v", ordered)
	}
	// Without a matching scope the ranked order stays untouched.
	if got := catalogCandidateOrder(candidates, "anthropic", ""); len(got) != 2 || got[0].Candidate.Scope != "far-gateway" {
		t.Fatalf("ranked order must survive when nothing matches: %+v", got)
	}
}

func TestSuggestionExampleIDPrefersCandidateReference(t *testing.T) {
	suggestions := []modelcatalog.Suggestion{{ModelID: "openai/gpt-6-sol"}}
	candidates := []modelcatalog.CandidateSuggestion{
		{Candidate: modelcatalog.Candidate{WireModelID: "w", Scope: "gw"}},
		{Candidate: modelcatalog.Candidate{WireModelID: "w2", Scope: "gw2", ModelID: "anthropic/claude-opus-5-5"}},
	}
	if got := suggestionExampleID(suggestions, candidates); got != "anthropic/claude-opus-5-5" {
		t.Fatalf("example = %q, want the candidate's referenced verified model", got)
	}
	if got := suggestionExampleID(suggestions, nil); got != "openai/gpt-6-sol" {
		t.Fatalf("example = %q, want the top verified suggestion", got)
	}
	if got := suggestionExampleID(nil, nil); got != "" {
		t.Fatalf("example = %q, want empty", got)
	}
}

func TestCandidateObservedFactsOmitsUnobserved(t *testing.T) {
	c := modelcatalog.Candidate{Context: 400000, Output: 128000, InputModalities: []string{"text", "image"}}
	if got := candidateObservedFacts(c); got != "context 400000, output 128000, modalities text,image" {
		t.Fatalf("observed = %q", got)
	}
	if got := candidateObservedFacts(modelcatalog.Candidate{}); got != "" {
		t.Fatalf("unobserved facts must render as nothing, got %q", got)
	}
}

func TestEditConfigYAMLForAddHandlesNullAndTypeConflicts(t *testing.T) {
	edit := configAddEdit{providerName: "sample", wireModel: "model-1", borrowID: "openai/gpt-6.1-sol", poolName: "default", poolRef: "sample/model-1"}
	for _, content := range []string{"providers: null\nmodel_pools: null\n", "providers:\n  sample:\n    models: null\nmodel_pools:\n  default: null\n", "null\n"} {
		if _, err := editConfigYAMLForAdd([]byte(content), edit); err != nil {
			t.Fatalf("null containers: %v", err)
		}
	}
	for _, content := range []string{"[]\n", "providers: []\n", "providers:\n  sample:\n    models: []\n", "model_pools:\n  default: {}\n"} {
		if _, err := editConfigYAMLForAdd([]byte(content), edit); err == nil {
			t.Fatalf("invalid container accepted: %s", content)
		}
	}
}
