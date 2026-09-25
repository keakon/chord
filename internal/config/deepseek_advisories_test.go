package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestDeepSeekReplayAdvisories(t *testing.T) {
	tests := []struct {
		name, wire, model, providerContract, modelContract, providerReplay, modelReplay string
		want                                                                            bool
	}{
		{name: "explicit contract", wire: ProviderTypeChatCompletions, model: "test-model", providerContract: ReasoningContractDeepSeek, providerReplay: ReasoningReplayNone, want: true},
		{name: "inferred contract", wire: ProviderTypeMessages, model: "deepseek-test", modelReplay: ReasoningReplayCurrentTurn, want: true},
		{name: "model contract overrides opt out", wire: ProviderTypeMessages, model: "test-model", providerContract: ReasoningContractNone, modelContract: ReasoningContractDeepSeek, providerReplay: ReasoningReplayNone, want: true},
		{name: "model opts out", wire: ProviderTypeChatCompletions, model: "deepseek-test", providerContract: ReasoningContractDeepSeek, modelContract: ReasoningContractNone, providerReplay: ReasoningReplayNone},
		{name: "unset replay", wire: ProviderTypeMessages, model: "deepseek-test"},
		{name: "explicit all", wire: ProviderTypeMessages, model: "deepseek-test", providerReplay: ReasoningReplayAll},
		{name: "model overrides replay", wire: ProviderTypeMessages, model: "deepseek-test", providerReplay: ReasoningReplayNone, modelReplay: ReasoningReplayAll},
		{name: "model conflict overrides all", wire: ProviderTypeMessages, model: "deepseek-test", providerReplay: ReasoningReplayAll, modelReplay: ReasoningReplayNone, want: true},
		{name: "unsupported wire", wire: ProviderTypeResponses, model: "deepseek-test", providerReplay: ReasoningReplayNone},
		{name: "unrelated model", wire: ProviderTypeChatCompletions, model: "test-model", providerReplay: ReasoningReplayNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := ProviderConfig{
				Type:   tt.wire,
				Compat: &ProviderCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{Contract: tt.providerContract, ReasoningReplay: tt.providerReplay}},
				Models: map[string]ModelConfig{tt.model: {
					Compat: &ModelCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{Contract: tt.modelContract, ReasoningReplay: tt.modelReplay}},
				}},
			}
			got := deepSeekContractAdvisories("sample", provider)
			if (len(got) > 0) != tt.want {
				t.Fatalf("advisories = %v, want warning %v", got, tt.want)
			}
			if tt.want && (len(got) != 1 || !strings.Contains(got[0], tt.model) || !strings.Contains(got[0], "reasoning_replay: all")) {
				t.Fatalf("unexpected advisory: %v", got)
			}
			if provider.Compat.ReasoningContinuity.ReasoningReplay != tt.providerReplay || provider.Models[tt.model].Compat.ReasoningContinuity.ReasoningReplay != tt.modelReplay {
				t.Fatal("advisory changed configured values")
			}
		})
	}
}

func TestDeepSeekReplayAdvisoryLoadsWithoutConfigIssue(t *testing.T) {
	got := collectTestAdvisories(t, `providers:
  sample:
    type: chat-completions
    compat:
      reasoning_continuity:
        contract: deepseek
        reasoning_replay: none
    models:
      test-model:
`)
	if !strings.Contains(got, "DeepSeek contract always replays") {
		t.Fatalf("missing replay advisory: %s", got)
	}
	provider := ProviderConfig{Type: ProviderTypeMessages, Compat: &ProviderCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{Contract: ReasoningContractDeepSeek, ReasoningReplay: ReasoningReplayNone}}, Models: map[string]ModelConfig{"model-b": {}, "model-a": {}}}
	first := deepSeekContractAdvisories("sample", provider)
	if len(first) != 2 || !strings.Contains(first[0], "model-a") || !reflect.DeepEqual(first, deepSeekContractAdvisories("sample", provider)) {
		t.Fatalf("advisories must have stable model order: %v", first)
	}
}

func TestDeepSeekModeAdvisories(t *testing.T) {
	for _, wire := range []string{ProviderTypeChatCompletions, ProviderTypeMessages} {
		provider := ProviderConfig{Type: wire, Compat: &ProviderCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{Contract: ReasoningContractDeepSeek, Mode: ReasoningContinuityModeNone}}, Models: map[string]ModelConfig{"model-1": {}}}
		got := deepSeekContractAdvisories("sample", provider)
		if len(got) != 1 || !strings.Contains(got[0], "remove the mode setting") {
			t.Fatalf("missing mode advisory: %v", got)
		}
		mode := ReasoningContinuityModeOpenAIVisible
		if wire == ProviderTypeMessages {
			mode = ReasoningContinuityModeAnthropicUnsigned
		}
		provider.Models["model-1"] = ModelConfig{Compat: &ModelCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{Mode: mode}}}
		if got := deepSeekContractAdvisories("sample", provider); len(got) != 0 {
			t.Fatalf("effective model mode should win: %v", got)
		}
	}
}

func TestDeepSeekContractPlacementAdvisories(t *testing.T) {
	for _, wire := range []string{ProviderTypeResponses, ProviderTypeGenerateContent} {
		cfg := ProviderConfig{Type: wire, Compat: &ProviderCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{Contract: ReasoningContractDeepSeek}}, Models: map[string]ModelConfig{"model-1": {}}}
		if got := deepSeekContractAdvisories("sample", cfg); len(got) != 1 || !strings.Contains(got[0], "has no effect") {
			t.Fatalf("missing placement advisory: %v", got)
		}
		cfg.Models["model-1"] = ModelConfig{Compat: &ModelCompatConfig{ReasoningContinuity: &ReasoningContinuityCompatConfig{Contract: ReasoningContractNone}}}
		if got := deepSeekContractAdvisories("sample", cfg); len(got) != 0 {
			t.Fatalf("model opt out should win: %v", got)
		}
	}
}
