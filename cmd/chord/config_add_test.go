package main

import (
	"strings"
	"testing"
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

func TestEditConfigYAMLForAddRejectsAnchors(t *testing.T) {
	current := []byte(`providers:
  openai: &base
    preset: openai
model_pools:
  default:
    - openai/gpt-6.1-sol
`)
	_, err := editConfigYAMLForAdd(current, configAddEdit{
		providerName: "gw", wireModel: "m", borrowID: "openai/gpt-6-sol",
		poolName: "default", poolRef: "gw/m",
	})
	if err == nil || !strings.Contains(err.Error(), "anchors") {
		t.Fatalf("anchor-bearing config must be refused, got %v", err)
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
	// A verified binding on the preset outranks an explicit borrow of a model
	// the preset does not serve; borrowing across presets is an error.
	if _, _, err := resolveConfigAddMode("openai", "gpt-6-sol-mirror", "anthropic/claude-opus-5-5"); err == nil {
		t.Fatal("catalog model not bound to the preset must fail")
	}
}

func TestPrintCatalogSuggestionsListsAndGuides(t *testing.T) {
	var out strings.Builder
	if !printCatalogSuggestions(&out, "gpt-6-sol-messages") {
		t.Fatal("expected suggestions for a near-catalog wire name")
	}
	text := out.String()
	if !strings.Contains(text, "openai/gpt-6-sol") || !strings.Contains(text, "--catalog openai/gpt-6-sol") {
		t.Errorf("suggestion output must list models and the adopt command:\n%s", text)
	}
	out.Reset()
	if printCatalogSuggestions(&out, "kimi-k3") {
		t.Error("below-threshold queries must report no suggestions")
	}
}
