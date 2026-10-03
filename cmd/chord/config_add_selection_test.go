package main

import "testing"

func TestConfigAddExplicitCatalogOverridesExactBinding(t *testing.T) {
	for _, id := range []string{"openai/gpt-6-sol", "openai/gpt-6.1-sol"} {
		mode, borrow, err := resolveConfigAddMode("codex", "gpt-6.1-sol", id)
		if err != nil || mode != configAddBorrow || borrow != id {
			t.Fatalf("explicit choice = %s, %q, %v", mode, borrow, err)
		}
	}
	for _, id := range []string{"sample/missing", "anthropic/claude-opus-5-5"} {
		if _, _, err := resolveConfigAddMode("codex", "gpt-6.1-sol", id); err == nil {
			t.Fatalf("invalid explicit choice %q was ignored", id)
		}
	}
}
