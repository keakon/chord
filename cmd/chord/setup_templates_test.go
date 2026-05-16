package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestBuildInitialSetupConfigYAML_OpenAIResponses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := buildInitialSetupConfigYAML(initialSetupConfigInput{
		Kind:         initialSetupProviderAPIKey,
		ProviderName: "openai",
		ProviderType: "responses",
		APIURL:       "https://api.openai.com/v1/responses",
		ModelName:    "gpt-5.5",
		ContextLimit: 400000,
		InputLimit:   272000,
		OutputLimit:  128000,
	})
	if err != nil {
		t.Fatalf("buildInitialSetupConfigYAML: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if got := cfg.ModelPools["default"]; len(got) != 1 || got[0] != "openai/gpt-5.5" {
		t.Fatalf("model_pools.default = %#v, want openai/gpt-5.5", got)
	}
	prov := cfg.Providers["openai"]
	if prov.APIURL != "https://api.openai.com/v1/responses" || prov.Type != "responses" {
		t.Fatalf("provider = %#v", prov)
	}
	if prov.Models["gpt-5.5"].Limit.Context != 400000 || prov.Models["gpt-5.5"].Limit.Input != 272000 || prov.Models["gpt-5.5"].Limit.Output != 128000 {
		t.Fatalf("model limits = %#v", prov.Models["gpt-5.5"].Limit)
	}
	if normalized, err := normalizeProviderConfig("openai", prov, nil); err != nil {
		t.Fatalf("normalizeProviderConfig: %v", err)
	} else if normalized.Type != config.ProviderTypeResponses {
		t.Fatalf("normalized type = %q, want %q", normalized.Type, config.ProviderTypeResponses)
	}
}

func TestBuildInitialSetupConfigYAML_OpenAIChatCompletions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := buildInitialSetupConfigYAML(initialSetupConfigInput{
		Kind:         initialSetupProviderAPIKey,
		ProviderName: "gateway",
		ProviderType: "chat-completions",
		APIURL:       "https://gateway.example/v1/chat/completions",
		ModelName:    "gpt-5.5",
		ContextLimit: 128000,
		OutputLimit:  32768,
	})
	if err != nil {
		t.Fatalf("buildInitialSetupConfigYAML: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	prov := cfg.Providers["gateway"]
	if normalized, err := normalizeProviderConfig("gateway", prov, nil); err != nil {
		t.Fatalf("normalizeProviderConfig: %v", err)
	} else if normalized.Type != config.ProviderTypeChatCompletions {
		t.Fatalf("normalized type = %q, want %q", normalized.Type, config.ProviderTypeChatCompletions)
	}
}

func TestBuildInitialSetupConfigYAML_Gemini(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := buildInitialSetupConfigYAML(initialSetupConfigInput{
		Kind:         initialSetupProviderAPIKey,
		ProviderName: "gemini",
		ProviderType: "generate-content",
		APIURL:       "https://generativelanguage.googleapis.com/v1beta/models",
		ModelName:    "gemini-3.1-pro-preview",
		ContextLimit: 1048576,
		OutputLimit:  65536,
	})
	if err != nil {
		t.Fatalf("buildInitialSetupConfigYAML: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	prov := cfg.Providers["gemini"]
	if prov.APIURL != "https://generativelanguage.googleapis.com/v1beta/models" {
		t.Fatalf("provider api_url = %q", prov.APIURL)
	}
	if prov.Models["gemini-3.1-pro-preview"].Limit.Context != 1048576 || prov.Models["gemini-3.1-pro-preview"].Limit.Output != 65536 {
		t.Fatalf("model limits = %#v", prov.Models["gemini-3.1-pro-preview"].Limit)
	}
	if normalized, err := normalizeProviderConfig("gemini", prov, nil); err != nil {
		t.Fatalf("normalizeProviderConfig: %v", err)
	} else if normalized.Type != config.ProviderTypeGenerateContent {
		t.Fatalf("normalized type = %q, want %q", normalized.Type, config.ProviderTypeGenerateContent)
	}
}

func TestBuildInitialSetupConfigYAML_Codex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := buildInitialSetupConfigYAML(initialSetupConfigInput{
		Kind:         initialSetupProviderCodex,
		ProviderName: "codex",
	})
	if err != nil {
		t.Fatalf("buildInitialSetupConfigYAML: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	prov := cfg.Providers["codex"]
	if prov.Preset != config.ProviderPresetCodex || prov.Type != config.ProviderTypeResponses {
		t.Fatalf("provider = %#v", prov)
	}
	wantPool := []string{"codex/gpt-5.2", "codex/gpt-5.3-codex", "codex/gpt-5.4", "codex/gpt-5.5"}
	gotPool := cfg.ModelPools["default"]
	if len(gotPool) != len(wantPool) {
		t.Fatalf("model_pools.default = %#v, want %#v", gotPool, wantPool)
	}
	for i := range wantPool {
		if gotPool[i] != wantPool[i] {
			t.Fatalf("model_pools.default[%d] = %q, want %q", i, gotPool[i], wantPool[i])
		}
	}
	for _, model := range []string{"gpt-5.2", "gpt-5.3-codex", "gpt-5.4", "gpt-5.5"} {
		if _, ok := prov.Models[model]; !ok {
			t.Fatalf("missing codex model %q in %#v", model, prov.Models)
		}
	}
	if normalized, err := normalizeProviderConfig("codex", prov, nil); err != nil {
		t.Fatalf("normalizeProviderConfig: %v", err)
	} else if normalized.Type != config.ProviderTypeResponses {
		t.Fatalf("normalized type = %q, want %q", normalized.Type, config.ProviderTypeResponses)
	}
}

func TestInitialSetupDefaultsForProviderType(t *testing.T) {
	defaults := initialSetupDefaultsForProviderType("responses")
	if defaults.APIURL != "https://api.openai.com/v1/responses" || defaults.ProviderName != "openai" || defaults.ModelName != "gpt-5.5" {
		t.Fatalf("responses defaults = %#v", defaults)
	}
	if defaults.InputLimit != 272000 || defaults.ContextLimit != 400000 || defaults.OutputLimit != 128000 {
		t.Fatalf("responses limits = %#v", defaults)
	}

	defaults = initialSetupDefaultsForProviderType("messages")
	if defaults.APIURL != "https://api.anthropic.com/v1/messages" || defaults.ProviderName != "anthropic" || defaults.ModelName != "claude-opus-4.7" {
		t.Fatalf("messages defaults = %#v", defaults)
	}

	defaults = initialSetupDefaultsForProviderType("generate-content")
	if defaults.APIURL != "https://generativelanguage.googleapis.com/v1beta/models" || defaults.ProviderName != "gemini" || defaults.ModelName != "gemini-3.1-pro-preview" {
		t.Fatalf("generate-content defaults = %#v", defaults)
	}
}

func TestDefaultAPIURLForProviderType(t *testing.T) {
	if got := defaultAPIURLForProviderType("generate-content"); got != "https://generativelanguage.googleapis.com/v1beta/models" {
		t.Fatalf("defaultAPIURLForProviderType(generate-content) = %q", got)
	}
	if got := defaultAPIURLForProviderType("responses"); got != "https://api.openai.com/v1/responses" {
		t.Fatalf("defaultAPIURLForProviderType(responses) = %q", got)
	}
}

func TestInferProviderTypeFromAPIURL_GeminiModels(t *testing.T) {
	if got := inferProviderTypeFromAPIURL("https://generativelanguage.googleapis.com/v1beta/models"); got != "generate-content" {
		t.Fatalf("inferProviderTypeFromAPIURL(gemini) = %q", got)
	}
	if got := inferProviderTypeFromAPIURL("https://generativelanguage.googleapis.com/v1beta/models/"); got != "generate-content" {
		t.Fatalf("inferProviderTypeFromAPIURL(gemini trailing slash) = %q", got)
	}
}
