package main

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestNormalizeProviderConfigDetectsGeminiFromModelsPath(t *testing.T) {
	got, err := normalizeProviderConfig("gemini", config.ProviderConfig{
		APIURL: "https://generativelanguage.googleapis.com/v1beta/models",
		Models: map[string]config.ModelConfig{"gemini-2.5-flash": {}},
	}, nil)
	if err != nil {
		t.Fatalf("normalizeProviderConfig() error = %v", err)
	}
	if got.Type != config.ProviderTypeGenerateContent {
		t.Fatalf("Type = %q, want %q", got.Type, config.ProviderTypeGenerateContent)
	}
	if got.APIURL != "https://generativelanguage.googleapis.com/v1beta/models" {
		t.Fatalf("APIURL = %q", got.APIURL)
	}
}

func TestNormalizeProviderConfigGeminiRequiresAPIURL(t *testing.T) {
	_, err := normalizeProviderConfig("gemini", config.ProviderConfig{
		Type:   config.ProviderTypeGenerateContent,
		Models: map[string]config.ModelConfig{"gemini-2.5-flash": {}},
	}, nil)
	if err == nil {
		t.Fatal("normalizeProviderConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "requires api_url") {
		t.Fatalf("error = %v, want requires api_url", err)
	}
}
