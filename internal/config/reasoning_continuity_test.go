package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProviderReasoningContinuityAcceptsKnownSelectors(t *testing.T) {
	cfg := ProviderConfig{
		Compat: &ProviderCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{
			Mode:            ReasoningContinuityModeOpenAIVisible,
			ReasoningReplay: ReasoningReplayAll,
		}},
		Models: map[string]ModelConfig{
			"model-unset": {},
			"model-none": {Compat: &ModelCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{
				Mode: ReasoningContinuityModeNone,
			}}},
			"model-unsigned": {Compat: &ModelCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{
				Mode:            ReasoningContinuityModeAnthropicUnsigned,
				ReasoningReplay: ReasoningReplayNone,
			}}},
		},
	}
	if err := ValidateProviderReasoningContinuity("sample", cfg); err != nil {
		t.Fatalf("ValidateProviderReasoningContinuity: %v", err)
	}
}

func TestValidateProviderReasoningContinuityRejectsUnknownSelectors(t *testing.T) {
	providerLevel := ProviderConfig{
		Compat: &ProviderCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{
			ReasoningReplay: "All",
		}},
	}
	err := ValidateProviderReasoningContinuity("sample", providerLevel)
	if err == nil || !strings.Contains(err.Error(), `invalid reasoning_replay "All"`) || !strings.Contains(err.Error(), `for provider "sample"`) {
		t.Fatalf("provider reasoning_replay error = %v, want the invalid value reported against the provider", err)
	}

	modelReplay := ProviderConfig{
		Models: map[string]ModelConfig{
			"model-1": {Compat: &ModelCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{
				ReasoningReplay: "preserve_history",
			}}},
		},
	}
	err = ValidateProviderReasoningContinuity("sample", modelReplay)
	if err == nil || !strings.Contains(err.Error(), `invalid reasoning_replay "preserve_history"`) || !strings.Contains(err.Error(), `for model "model-1" in provider "sample"`) {
		t.Fatalf("model reasoning_replay error = %v, want the invalid value reported against the model", err)
	}

	modelMode := ProviderConfig{
		Models: map[string]ModelConfig{
			"model-1": {Compat: &ModelCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{
				Mode: "openai-visible",
			}}},
		},
	}
	err = ValidateProviderReasoningContinuity("sample", modelMode)
	if err == nil || !strings.Contains(err.Error(), `invalid reasoning_continuity mode "openai-visible"`) || !strings.Contains(err.Error(), `for model "model-1" in provider "sample"`) {
		t.Fatalf("model mode error = %v, want the invalid value reported against the model", err)
	}
}

// A misspelled selector used to be swallowed: the value is only read while
// resolving a request, where it is indistinguishable from unset, so a typo
// silently dropped the reasoning continuity the backend's contract requires
// while the removed preserve_history key was rejected loudly by the decoder.
func TestLoadConfigFromPathResetsInvalidReasoningContinuity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`providers:
  sample:
    type: chat-completions
    compat:
      reasoning_continuity:
        mode: openai-visible
        reasoning_replay: All
    models:
      model-1:
        compat:
          reasoning_continuity:
            mode: openai_visible
            reasoning_replay: all
      model-2:
        compat:
          reasoning_continuity:
            mode: anthropic_unsigned
`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	providerCfg := cfg.Providers["sample"]
	if providerCfg.Compat == nil || providerCfg.Compat.ReasoningContinuity == nil {
		t.Fatal("provider reasoning_continuity block was dropped")
	}
	providerCompat := providerCfg.Compat.ReasoningContinuity
	if got := providerCompat.EffectiveMode(); got != "" {
		t.Fatalf("provider mode = %q, want it reset to unset", got)
	}
	if got := providerCompat.ReasoningReplayValue(); got != "" {
		t.Fatalf("provider reasoning_replay = %q, want it reset to unset", got)
	}

	validModel := providerCfg.Models["model-1"]
	if validModel.Compat == nil || validModel.Compat.ReasoningContinuity == nil {
		t.Fatal("model-1 reasoning_continuity block was dropped")
	}
	valid := validModel.Compat.ReasoningContinuity
	if got := valid.EffectiveMode(); got != "openai_visible" {
		t.Fatalf("model-1 mode = %q, want the valid selection preserved", got)
	}
	if got := valid.ReasoningReplayValue(); got != "all" {
		t.Fatalf("model-1 reasoning_replay = %q, want the valid selection preserved", got)
	}

	unsignedModel := providerCfg.Models["model-2"]
	if unsignedModel.Compat == nil || unsignedModel.Compat.ReasoningContinuity == nil {
		t.Fatal("model-2 reasoning_continuity block was dropped")
	}
	if got := unsignedModel.Compat.ReasoningContinuity.EffectiveMode(); got != "anthropic_unsigned" {
		t.Fatalf("model-2 mode = %q, want the valid selection preserved", got)
	}
}
