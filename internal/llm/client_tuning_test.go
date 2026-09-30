package llm

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestOpenAITuningEffectiveReasoningEffort(t *testing.T) {
	if got := (OpenAITuning{ReasoningEffort: "high"}).EffectiveReasoningEffort(); got != "high" {
		t.Fatalf("unmapped effort = %q, want high", got)
	}
	if got := (OpenAITuning{ReasoningEffort: "high", ReasoningEffortMap: map[string]string{"high": "max"}}).EffectiveReasoningEffort(); got != "max" {
		t.Fatalf("mapped effort = %q, want max", got)
	}
	if got := (OpenAITuning{ReasoningEffort: "high", ReasoningEffortMap: map[string]string{"low": "xhigh"}}).EffectiveReasoningEffort(); got != "high" {
		t.Fatalf("unmatched map must pass through, got %q", got)
	}
	if got := (OpenAITuning{ReasoningEffortMap: map[string]string{"high": "max"}}).EffectiveReasoningEffort(); got != "" {
		t.Fatalf("empty effort must stay empty, got %q", got)
	}
}

func TestTuningCloneAndMergeCarryHostedTool(t *testing.T) {
	orig := RequestTuning{HostedTool: &HostedToolRequest{Name: "sample_tool", Declaration: json.RawMessage(`{"type":"sample_tool"}`), Include: []string{"sample.include"}}}
	clone := cloneRequestTuning(orig)
	if clone.HostedTool == nil || clone.HostedTool == orig.HostedTool {
		t.Fatalf("clone must copy HostedTool, got %#v", clone.HostedTool)
	}
	if clone.HostedTool.Name != "sample_tool" || string(clone.HostedTool.Declaration) != `{"type":"sample_tool"}` || len(clone.HostedTool.Include) != 1 {
		t.Fatalf("clone lost HostedTool fields: %#v", clone.HostedTool)
	}

	merged := mergeRequestTuning(RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "high"}}, orig)
	if merged.OpenAI.ReasoningEffort != "high" {
		t.Fatalf("merge must keep base fields, got %q", merged.OpenAI.ReasoningEffort)
	}
	if merged.HostedTool == nil || merged.HostedTool.Name != "sample_tool" {
		t.Fatalf("merge lost HostedTool: %#v", merged.HostedTool)
	}
	// A tuning without HostedTool must not clear an existing one.
	kept := mergeRequestTuning(RequestTuning{HostedTool: orig.HostedTool}, RequestTuning{})
	if kept.HostedTool == nil || kept.HostedTool.Name != "sample_tool" {
		t.Fatalf("merge cleared HostedTool: %#v", kept.HostedTool)
	}
}

func TestClientNextRequestTuningOverrideCarriesHostedTool(t *testing.T) {
	client, _, _ := replayTestClient(0)
	client.SetNextRequestTuningOverride(RequestTuning{HostedTool: &HostedToolRequest{Name: "sample_tool", Force: json.RawMessage(`{"type":"tool"}`)}})
	client.mu.Lock()
	override, ok := client.consumeRequestTuningOverrideLocked()
	client.mu.Unlock()
	if !ok || override.HostedTool == nil || override.HostedTool.Name != "sample_tool" || string(override.HostedTool.Force) != `{"type":"tool"}` {
		t.Fatalf("override with hosted tool = %+v, ok=%v", override, ok)
	}
	client.mu.Lock()
	_, ok = client.consumeRequestTuningOverrideLocked()
	client.mu.Unlock()
	if ok {
		t.Fatal("tuning override must be one-shot")
	}
}

func TestTuningFromModelCarriesReasoningEffortMap(t *testing.T) {
	model := config.ModelConfig{
		Reasoning: &config.ReasoningConfig{
			Effort:    "high",
			EffortMap: map[string]string{"high": "max"},
		},
	}
	tuning := tuningFromModel(model, "", nil, nil)
	if got := tuning.OpenAI.EffectiveReasoningEffort(); got != "max" {
		t.Fatalf("model effort = %q, want max", got)
	}

	tuning = mergeVariantTuning(tuning, config.ModelVariant{
		Reasoning: &config.ReasoningConfig{Effort: "low"},
	})
	if got := tuning.OpenAI.EffectiveReasoningEffort(); got != "low" {
		t.Fatalf("variant effort override = %q, want low", got)
	}
	if tuning.OpenAI.ReasoningEffortMap["high"] != "max" {
		t.Fatalf("model map must survive variant effort override: %v", tuning.OpenAI.ReasoningEffortMap)
	}

	tuning = mergeVariantTuning(tuning, config.ModelVariant{
		Reasoning: &config.ReasoningConfig{EffortMap: map[string]string{"low": "xhigh"}},
	})
	if got := tuning.OpenAI.EffectiveReasoningEffort(); got != "xhigh" {
		t.Fatalf("variant map override = %q, want xhigh", got)
	}
}
