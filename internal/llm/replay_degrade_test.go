package llm

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// replayRejectingProvider rejects the first rejectCount requests with a
// reasoning-replay 400 and records the message list of every attempt.
type replayRejectingProvider struct {
	mu                    sync.Mutex
	rejectCount           int
	contextLengthExceeded bool
	rejectionCode         string
	rejectionMessage      string
	statusCode            int
	attempts              [][]message.Message
	tunings               []RequestTuning
}

func (p *replayRejectingProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	msgs []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	copied := make([]message.Message, len(msgs))
	copy(copied, msgs)
	p.attempts = append(p.attempts, copied)
	p.tunings = append(p.tunings, cloneRequestTuning(tuning))
	if p.contextLengthExceeded {
		return nil, &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}
	}
	if len(p.attempts) <= p.rejectCount {
		msg := p.rejectionMessage
		if msg == "" {
			msg = "Item 'fc_1' of type 'function_call' was provided without its required 'reasoning' item: 'rs_1'"
		}
		statusCode := p.statusCode
		if statusCode == 0 {
			statusCode = 400
		}
		return nil, &APIError{StatusCode: statusCode, Code: p.rejectionCode, Message: msg}
	}
	return &message.Response{Content: "ok"}, nil
}

func crossProviderReplayMessages() []message.Message {
	return []message.Message{
		{Role: message.RoleUser, Content: "continue"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-1"},
				{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "read", Arguments: `{}`},
			},
			ToolCalls: []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
			Provenance: &message.MessageProvenance{
				WireFamily: "openai-responses",
				ProviderID: "other-provider",
				ModelID:    "gpt-5.6-sol",
			},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "READ_RESULT ok"},
		{Role: message.RoleUser, Content: "go on"},
	}
}

func replayTestClient(rejectCount int) (*Client, *ProviderConfig, *replayRejectingProvider) {
	cfg := NewProviderConfig("responses", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.6-sol": {}},
	}, []string{"key"})
	impl := &replayRejectingProvider{rejectCount: rejectCount}
	return NewClient(cfg, impl, "gpt-5.6-sol", 1024, ""), cfg, impl
}

func callReplayTestStream(t *testing.T, client *Client, cfg *ProviderConfig, impl *replayRejectingProvider) (*message.Response, error) {
	t.Helper()
	return callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		cfg,
		impl,
		"gpt-5.6-sol",
		1024,
		RequestTuning{},
		"",
		crossProviderReplayMessages(),
		nil,
		nil,
		false,
		nil,
		-2,
		&CallStatus{},
	)
}

func requireStrictReplayEvidence(t *testing.T, messages []message.Message, toolName, callID string) message.Message {
	t.Helper()
	wantCall := "[Historical tool call: " + toolName + "]"
	wantResult := "[Historical tool result for " + callID + "]"
	var evidence message.Message
	foundContinuation := false
	for _, msg := range messages {
		if msg.Role == message.RoleTool || len(msg.ToolCalls) > 0 {
			t.Fatalf("strict replay retained structured tool history: %+v", messages)
		}
		if msg.Kind == "replay_evidence" && msg.Role == message.RoleAssistant &&
			strings.Contains(msg.Content, wantCall) && strings.Contains(msg.Content, wantResult) {
			evidence = msg
		}
		if msg.Kind == "replay_continuation" && msg.Role == message.RoleUser &&
			strings.Contains(msg.Content, "Continue the current task") {
			foundContinuation = true
		}
	}
	if evidence.Kind == "" || !foundContinuation {
		t.Fatalf("strict replay did not preserve isolated evidence and continuation: %+v", messages)
	}
	return evidence
}

func TestCompleteStreamDegradesReplayCompatLadderOnRejection(t *testing.T) {
	client, cfg, impl := replayTestClient(2)

	resp, err := callReplayTestStream(t, client, cfg, impl)
	if err != nil {
		t.Fatalf("CompleteStream error = %v, want success after ladder degradation", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("response = %+v, want scripted success", resp)
	}
	impl.mu.Lock()
	attempts := impl.attempts
	impl.mu.Unlock()
	if len(attempts) != 3 {
		t.Fatalf("attempts = %d, want native, synthesized, strict", len(attempts))
	}
	nativeKept := false
	for _, m := range attempts[0] {
		if len(m.ResponsesOutput) > 0 {
			nativeKept = true
		}
	}
	if !nativeKept {
		t.Fatalf("first attempt should replay foreign native items optimistically: %+v", attempts[0])
	}
	callsKept := false
	for _, m := range attempts[1] {
		if len(m.ResponsesOutput) > 0 {
			t.Fatalf("second attempt should strip native items, got %+v", m.ResponsesOutput)
		}
		if len(m.ToolCalls) > 0 {
			callsKept = true
		}
	}
	if !callsKept {
		t.Fatalf("second attempt should keep synthesized tool calls: %+v", attempts[1])
	}
	for _, m := range attempts[2] {
		if len(m.ToolCalls) > 0 || m.Role == message.RoleTool {
			t.Fatalf("strict attempt should remove the rejected structured trajectory, got %+v", attempts[2])
		}
	}
	requireStrictReplayEvidence(t, attempts[2], "read", "call_1")

	// The achieved level is remembered: a new request on the same client
	// starts at the strict level without paying failing round trips again.
	if _, err := callReplayTestStream(t, client, cfg, impl); err != nil {
		t.Fatalf("second CompleteStream error = %v", err)
	}
	impl.mu.Lock()
	defer impl.mu.Unlock()
	if len(impl.attempts) != 4 {
		t.Fatalf("attempts after second call = %d, want a single request at the remembered level", len(impl.attempts))
	}
	for _, m := range impl.attempts[3] {
		if len(m.ToolCalls) > 0 || m.Role == message.RoleTool {
			t.Fatalf("remembered level should avoid the rejected structured trajectory, got %+v", impl.attempts[3])
		}
	}
	requireStrictReplayEvidence(t, impl.attempts[3], "read", "call_1")
}

func TestFallbackTargetStartsWithSynthesizedReplayForForeignNativePayload(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"deepseek-v4-flash-free": {}},
	}, []string{"key"})
	target := FallbackModel{ProviderConfig: cfg, ModelID: "deepseek-v4-flash-free"}
	if got := fallbackReplayLevel(crossProviderReplayMessages(), target); got != modelcompat.ReplayCompatSynthesized {
		t.Fatalf("fallbackReplayLevel = %v, want synthesized for cross-wire tool trajectory", got)
	}
}

func TestFallbackReplayLevelKeepsNativeForSameWireFamily(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"other": {}},
	}, []string{"key"})
	msg := crossProviderReplayMessages()[1]
	msg.Provenance = &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat}
	if got := fallbackReplayLevel([]message.Message{msg}, FallbackModel{ProviderConfig: cfg, ModelID: "other"}); got != modelcompat.ReplayCompatNative {
		t.Fatalf("same-wire fallbackReplayLevel = %v, want native", got)
	}
}

func TestFallbackSynthesizedReplayPreservesForeignAnthropicToolTrajectory(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"other": {}},
	}, []string{"key"})
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "continue"},
		{Role: message.RoleAssistant, Content: "", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}}, Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyAnthropic}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "READ_RESULT ok"},
	}
	prepared, report := normalizeMessagesForPoolTargetWithOptions(msgs, FallbackModel{ProviderConfig: cfg, ModelID: "other"}, RequestTuning{}, modelcompat.ReplayCompatSynthesized)
	if report.DowngradedToolCalls != 0 || len(prepared[1].ToolCalls) != 1 || len(prepared[2].Content) == 0 {
		t.Fatalf("synthesized cross-wire replay = %+v report=%+v, want preserved reasoning/tool trajectory", prepared, report)
	}
}

func TestReplayCompatibleRequestTuningDisablesMissingToolReasoning(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"deepseek": {
			Reasoning: &config.ReasoningConfig{Effort: "high"},
			Compat: &config.ModelCompatConfig{RequestOverrides: &config.RequestOverridesConfig{Body: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			}}},
		}},
	}, []string{"key"})
	target := FallbackModel{ProviderConfig: cfg, ModelID: "deepseek"}
	tuning := tuningForPoolTarget(target)
	missing := []message.Message{{
		Role:       message.RoleAssistant,
		ToolCalls:  []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
		Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyAnthropic},
	}}
	if got := replayCompatibleRequestTuning(tuning, missing, target); !got.DisableReasoning {
		t.Fatalf("missing reasoning tuning = %+v, want disabled", got)
	}
	missing[0].ReasoningContent = "portable reasoning"
	if got := replayCompatibleRequestTuning(tuning, missing, target); got.DisableReasoning {
		t.Fatalf("portable reasoning tuning = %+v, want enabled", got)
	}
}

func TestCompleteStreamTargetPassesReplayCompatibleTuning(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"deepseek": {
			Reasoning: &config.ReasoningConfig{Effort: "high"},
		}},
	}, []string{"key"})
	impl := &replayRejectingProvider{}
	client := NewClient(cfg, impl, "deepseek", 1024, "")
	result, _, err := client.completeStreamTarget(
		context.Background(), streamRetryTarget{
			provider: cfg, impl: impl, modelID: "deepseek", maxTokens: 1024,
			contextLimit: 128000, inputLimit: 128000, isFallback: true,
			tuning: tuningForPoolTarget(FallbackModel{ProviderConfig: cfg, ModelID: "deepseek"}),
		},
		0, []message.Message{
			{
				Role:       message.RoleAssistant,
				ToolCalls:  []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
				Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyAnthropic},
			},
			{Role: message.RoleTool, ToolCallID: "call_1", Content: "READ_RESULT ok"},
		}, nil, nil, false, nil, 0, false, &CallStatus{}, "", 0, 0,
		func() error { return nil }, nil,
	)
	if err != nil || result.resp == nil {
		t.Fatalf("completeStreamTarget = (%+v, %v), want success", result, err)
	}
	impl.mu.Lock()
	defer impl.mu.Unlock()
	if len(impl.tunings) != 1 || !impl.tunings[0].DisableReasoning {
		t.Fatalf("provider tunings = %+v, want DisableReasoning", impl.tunings)
	}
}

func TestCompleteStreamStrictlyTextifiesRejectedForeignToolTrajectory(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"deepseek": {Reasoning: &config.ReasoningConfig{Effort: "high"}}},
	}, []string{"key"})
	impl := &replayRejectingProvider{
		rejectCount:      1,
		rejectionMessage: "The `reasoning_content` in the thinking mode must be passed back to the API.",
	}
	client := NewClient(cfg, impl, "deepseek", 1024, "")
	result, _, err := client.completeStreamTarget(
		context.Background(), streamRetryTarget{
			provider: cfg, impl: impl, modelID: "deepseek", maxTokens: 1024,
			contextLimit: 128000, inputLimit: 128000, isFallback: true,
			tuning: tuningForPoolTarget(FallbackModel{ProviderConfig: cfg, ModelID: "deepseek"}),
		},
		0, []message.Message{
			{
				Role:       message.RoleAssistant,
				Content:    "checking file",
				ToolCalls:  []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
				Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyAnthropic},
			},
			{Role: message.RoleTool, ToolCallID: "call_1", Content: "READ_RESULT ok"},
		}, nil, nil, false, nil, 0, false, &CallStatus{}, "", 0, 0,
		func() error { return nil }, nil,
	)
	if err != nil || result.resp == nil {
		t.Fatalf("completeStreamTarget = (%+v, %v), want strict replay success", result, err)
	}
	impl.mu.Lock()
	defer impl.mu.Unlock()
	if len(impl.attempts) != 2 {
		t.Fatalf("attempts = %d, want rejected structured request plus strict retry", len(impl.attempts))
	}
	if len(impl.attempts[0][0].ToolCalls) != 1 {
		t.Fatalf("first request tool calls = %+v, want preserved structured trajectory", impl.attempts[0][0].ToolCalls)
	}
	for _, msg := range impl.attempts[1] {
		if len(msg.ToolCalls) > 0 || msg.Role == message.RoleTool {
			t.Fatalf("strict retry retained structured tool trajectory: %+v", impl.attempts[1])
		}
	}
}

func TestCompleteStreamContinuesAfterFallbackReplayRejection(t *testing.T) {
	newConfig := func(name, model string) *ProviderConfig {
		return NewProviderConfig(name, config.ProviderConfig{
			Type:   config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{model: {}},
		}, []string{"key"})
	}
	primaryCfg := newConfig("primary", "primary-model")
	firstFallbackCfg := newConfig("first-fallback", "first-model")
	secondFallbackCfg := newConfig("second-fallback", "second-model")
	primary := &replayRejectingProvider{rejectCount: 10, statusCode: 502, rejectionMessage: "upstream unavailable"}
	firstFallback := &replayRejectingProvider{rejectCount: 10, rejectionMessage: "The `reasoning_content` in the thinking mode must be passed back to the API."}
	secondFallback := &replayRejectingProvider{}
	client := NewClient(primaryCfg, primary, "primary-model", 1024, "")
	resp, err := callCompleteStreamWithRetryForTest(
		client, context.Background(), primaryCfg, primary, "primary-model", 1024,
		RequestTuning{}, "", []message.Message{{Role: message.RoleUser, Content: "continue"}}, nil, nil,
		true, []FallbackModel{
			{ProviderConfig: firstFallbackCfg, ProviderImpl: firstFallback, ModelID: "first-model", MaxTokens: 1024},
			{ProviderConfig: secondFallbackCfg, ProviderImpl: secondFallback, ModelID: "second-model", MaxTokens: 1024},
		}, -1, &CallStatus{},
	)
	if err != nil || resp == nil || resp.Content != "ok" {
		t.Fatalf("CompleteStream = (%+v, %v), want second fallback success", resp, err)
	}
	if len(primary.attempts) == 0 || len(firstFallback.attempts) == 0 || len(secondFallback.attempts) != 1 {
		t.Fatalf("attempt counts primary=%d first=%d second=%d, want all targets traversed", len(primary.attempts), len(firstFallback.attempts), len(secondFallback.attempts))
	}
}

func TestCompleteStreamCompactionReplayFloorUsesPortableShape(t *testing.T) {
	cfg := NewProviderConfig("responses", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.6-sol": {}},
	}, []string{"key"})
	impl := &replayRejectingProvider{}
	client := NewClient(cfg, impl, "gpt-5.6-sol", 4096, "sys")
	level := modelcompat.ReplayCompatSynthesized
	result, _, err := client.completeStreamTarget(
		context.Background(), streamRetryTarget{
			provider: cfg, impl: impl, modelID: "gpt-5.6-sol", maxTokens: 4096,
			contextLimit: 128000, inputLimit: 128000,
			tuning: RequestTuning{ReplayCompat: &level},
		},
		0, crossProviderReplayMessages(), nil, nil, false, nil, 0, false,
		&CallStatus{}, "sys", 0, 0, func() error { return nil }, nil,
	)
	if err != nil || result.resp == nil {
		t.Fatalf("completeStreamTarget = (%+v, %v), want success", result, err)
	}
	impl.mu.Lock()
	defer impl.mu.Unlock()
	if len(impl.attempts) != 1 {
		t.Fatalf("attempts = %d, want one portable request", len(impl.attempts))
	}
	for _, msg := range impl.attempts[0] {
		if len(msg.ResponsesOutput) > 0 {
			t.Fatalf("portable compaction request retained Responses output: %+v", msg.ResponsesOutput)
		}
	}
}

func TestCompleteStreamGeneric400OnlyDegradesNativeReplay(t *testing.T) {
	cfg := NewProviderConfig("responses", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.6-sol": {}},
	}, []string{"key"})
	impl := &replayRejectingProvider{
		rejectCount:      1,
		rejectionMessage: "请求未能完成,请检查模型、参数或客户端配置后重试。",
	}
	client := NewClient(cfg, impl, "gpt-5.6-sol", 4096, "sys")
	result, _, err := client.completeStreamTarget(
		context.Background(), streamRetryTarget{
			provider: cfg, impl: impl, modelID: "gpt-5.6-sol", maxTokens: 4096,
			contextLimit: 128000, inputLimit: 128000,
		},
		0, crossProviderReplayMessages(), nil, nil, false, nil, 0, false,
		&CallStatus{}, "sys", 0, 0, func() error { return nil }, nil,
	)
	if err != nil || result.resp == nil {
		t.Fatalf("completeStreamTarget = (%+v, %v), want recovery after generic 400", result, err)
	}
	impl.mu.Lock()
	defer impl.mu.Unlock()
	if len(impl.attempts) != 2 {
		t.Fatalf("attempts = %d, want native request plus one degraded retry", len(impl.attempts))
	}
	if len(impl.attempts[1][1].ResponsesOutput) != 0 || len(impl.attempts[1][1].ToolCalls) == 0 {
		t.Fatalf("synthesized request did not remove native output while retaining tool call: %+v", impl.attempts[1])
	}
}

func TestCompleteStreamSkipsEquivalentReplayLevelBeforeStrict(t *testing.T) {
	cfg := NewProviderConfig("messages", config.ProviderConfig{Type: config.ProviderTypeMessages}, []string{"key"})
	impl := &replayRejectingProvider{rejectCount: 1, rejectionMessage: "The `content[].thinking` in the thinking mode must be passed back to the API."}
	client := NewClient(cfg, impl, "deepseek-v4-pro", 4096, "sys")
	messages := []message.Message{
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "unsigned reasoning"}},
			ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
			Provenance:     &message.MessageProvenance{ProviderID: "source", ModelID: "deepseek-v4-pro", WireFamily: modelcompat.WireFamilyAnthropic},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
	}
	result, _, err := client.completeStreamTarget(
		context.Background(), streamRetryTarget{provider: cfg, impl: impl, modelID: "deepseek-v4-pro", maxTokens: 4096, contextLimit: 128000, inputLimit: 128000, tuning: RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "adaptive"}}},
		0, messages, nil, nil, false, nil, 0, false, &CallStatus{}, "sys", 0, 0, func() error { return nil }, nil,
	)
	if err != nil || result.resp == nil {
		t.Fatalf("completeStreamTarget = (%+v, %v)", result, err)
	}
	impl.mu.Lock()
	attempts := append([][]message.Message(nil), impl.attempts...)
	impl.mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want native then strict without identical synthesized retry", len(attempts))
	}
	requireStrictReplayEvidence(t, attempts[1], "read", "call-1")
	if got := client.replayCompatLevelFor(cfg.Name(), "deepseek-v4-pro", ""); got != modelcompat.ReplayCompatStrict {
		t.Fatalf("replay level = %d, want strict", got)
	}
}

func TestCompleteStreamDegradesConvertedUnsignedThinkingWithoutTextLeak(t *testing.T) {
	cfg := NewProviderConfig("messages", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"glm-5.2": {
				Thinking: &config.ThinkingConfig{Type: "adaptive"},
				Compat:   &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: modelcompat.ReasoningContinuityAnthropicUnsigned}},
			},
		},
	}, []string{"key"})
	impl := &replayRejectingProvider{rejectCount: 1, rejectionMessage: "The `content[].thinking` in the thinking mode must be passed back to the API."}
	client := NewClient(cfg, impl, "glm-5.2", 4096, "sys")
	messages := []message.Message{
		{
			Role:             message.RoleAssistant,
			Content:          "calling tool",
			ReasoningContent: "portable reasoning",
			ToolCalls:        []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
			Provenance:       &message.MessageProvenance{ProviderID: "chat-source", ModelID: "glm-5.2", WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
	}
	result, _, err := client.completeStreamTarget(
		context.Background(), streamRetryTarget{provider: cfg, impl: impl, modelID: "glm-5.2", maxTokens: 4096, contextLimit: 128000, inputLimit: 128000, tuning: RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "adaptive"}}},
		0, messages, nil, nil, false, nil, 0, false, &CallStatus{}, "sys", 0, 0, func() error { return nil }, nil,
	)
	if err != nil || result.resp == nil {
		t.Fatalf("completeStreamTarget = (%+v, %v)", result, err)
	}
	impl.mu.Lock()
	attempts := append([][]message.Message(nil), impl.attempts...)
	impl.mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want converted native then strict fallback", len(attempts))
	}
	if len(attempts[0][0].ThinkingBlocks) != 1 || attempts[0][0].ThinkingBlocks[0].Thinking != "portable reasoning" || attempts[0][0].ReasoningContent != "" {
		t.Fatalf("first attempt did not convert reasoning: %+v", attempts[0][0])
	}
	if len(attempts[1][0].ThinkingBlocks) != 0 || attempts[1][0].ReasoningContent != "" || len(attempts[1][0].ToolCalls) != 0 {
		t.Fatalf("fallback did not degrade to strict text trajectory: %+v", attempts[1][0])
	}
	evidence := requireStrictReplayEvidence(t, attempts[1], "read", "call-1")
	if strings.Contains(evidence.Content, "portable reasoning") || strings.Contains(evidence.Content, "Previous model reasoning") {
		t.Fatalf("fallback leaked reasoning into replay evidence: %q", evidence.Content)
	}
}

func TestResetReplayCompatibilityClearsReplayStateOnly(t *testing.T) {
	client, cfg, _ := replayTestClient(0)
	client.setReplayCompatLevelFor(cfg.Name(), "gpt-5.6-sol", "", modelcompat.ReplayCompatStrict)
	portable := modelcompat.ReplayCompatSynthesized
	client.MergeNextRequestTuningOverride(RequestTuning{DisableReasoning: true, ReplayCompat: &portable})

	client.ResetReplayCompatibility()

	if got := client.replayCompatLevelFor(cfg.Name(), "gpt-5.6-sol", ""); got != modelcompat.ReplayCompatNative {
		t.Fatalf("replay level after reset = %d, want native", got)
	}
	client.mu.Lock()
	override, ok := client.consumeRequestTuningOverrideLocked()
	client.mu.Unlock()
	if !ok || !override.DisableReasoning || override.ReplayCompat != nil {
		t.Fatalf("pending override after reset = %+v, ok=%v; want non-replay tuning preserved", override, ok)
	}
}

func TestCompleteStreamDegradesProviderNativeReplayOnKnownRejections(t *testing.T) {
	tests := []struct {
		name      string
		provider  config.ProviderConfig
		modelID   string
		tuning    RequestTuning
		messages  []message.Message
		err       *APIError
		hasNative func(message.Message) bool
	}{
		{
			name:     "openai visible reasoning",
			provider: config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Compat: &config.ProviderCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: modelcompat.ReasoningContinuityOpenAIVisible}}},
			modelID:  "target-chat",
			messages: []message.Message{{
				Role:             message.RoleAssistant,
				Content:          "calling tool",
				ReasoningContent: "native reasoning",
				ToolCalls:        []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
				Provenance:       &message.MessageProvenance{ProviderID: "source", ModelID: "source-chat", WireFamily: modelcompat.WireFamilyOpenAIChat},
			}, {Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"}},
			err:       &APIError{StatusCode: 400, Message: "The `reasoning_content` in the thinking mode must be passed back to the API."},
			hasNative: func(msg message.Message) bool { return msg.ReasoningContent != "" },
		},
		{
			name:     "anthropic thinking",
			provider: config.ProviderConfig{Type: config.ProviderTypeMessages},
			modelID:  "target-claude",
			tuning:   RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024}},
			messages: []message.Message{{
				Role:           message.RoleAssistant,
				Content:        "calling tool",
				ThinkingBlocks: []message.ThinkingBlock{{Thinking: "native thinking", Signature: "sig"}},
				ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
				Provenance:     &message.MessageProvenance{ProviderID: "source", ModelID: "source-claude", WireFamily: modelcompat.WireFamilyAnthropic},
			}, {Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"}},
			err:       &APIError{StatusCode: 400, Message: "The `content[].thinking` in the thinking mode must be passed back to the API."},
			hasNative: func(msg message.Message) bool { return len(msg.ThinkingBlocks) > 0 },
		},
		{
			name:     "gemini thought signature",
			provider: config.ProviderConfig{Type: config.ProviderTypeGenerateContent},
			modelID:  "target-gemini",
			messages: []message.Message{{
				Role:      message.RoleAssistant,
				Content:   "calling tool",
				ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`), ThoughtSignature: "sig"}},
				GeminiParts: []message.GeminiReplayPart{{
					Type: "function_call", ToolCallID: "call-1", ThoughtSignature: "sig",
				}},
				Provenance: &message.MessageProvenance{ProviderID: "source", ModelID: "source-gemini", WireFamily: modelcompat.WireFamilyGemini},
			}, {Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"}},
			err: &APIError{StatusCode: 400, Code: "INVALID_ARGUMENT", Message: "Function call is missing a valid thought signature"},
			hasNative: func(msg message.Message) bool {
				return len(msg.GeminiParts) > 0 || (len(msg.ToolCalls) > 0 && msg.ToolCalls[0].ThoughtSignature != "")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewProviderConfig("target", tc.provider, []string{"key"})
			impl := &replayRejectingProvider{
				rejectCount:      1,
				rejectionCode:    tc.err.Code,
				rejectionMessage: tc.err.Message,
			}
			client := NewClient(cfg, impl, tc.modelID, 4096, "sys")
			result, _, err := client.completeStreamTarget(
				context.Background(), streamRetryTarget{
					provider: cfg, impl: impl, modelID: tc.modelID, maxTokens: 4096,
					contextLimit: 128000, inputLimit: 128000, tuning: tc.tuning,
				},
				0, tc.messages, nil, nil, false, nil, 0, false, &CallStatus{}, "sys", 0, 0,
				func() error { return nil }, nil,
			)
			if err != nil || result.resp == nil {
				t.Fatalf("completeStreamTarget = (%+v, %v), want success after replay degradation", result, err)
			}
			impl.mu.Lock()
			attempts := append([][]message.Message(nil), impl.attempts...)
			impl.mu.Unlock()
			if len(attempts) != 2 || len(attempts[0]) == 0 || len(attempts[1]) == 0 {
				t.Fatalf("attempts = %#v, want two request shapes", attempts)
			}
			if !tc.hasNative(attempts[0][0]) {
				t.Fatalf("first attempt did not preserve native replay: %#v", attempts[0][0])
			}
			if tc.hasNative(attempts[1][0]) {
				t.Fatalf("second attempt did not degrade native replay: %#v", attempts[1][0])
			}
			if got := client.replayCompatLevelFor(cfg.Name(), tc.modelID, ""); got != modelcompat.ReplayCompatSynthesized {
				t.Fatalf("replay level = %d, want synthesized", got)
			}
		})
	}
}

func TestCompleteStreamDoesNotDegradeReplayForUnrelatedBadRequest(t *testing.T) {
	client, cfg, impl := replayTestClient(1)
	impl.rejectionCode = "invalid_parameter"
	impl.rejectionMessage = "unknown parameter service_tier"

	_, err := callReplayTestStream(t, client, cfg, impl)
	if err == nil {
		t.Fatal("CompleteStream error = nil, want unrelated request error")
	}
	impl.mu.Lock()
	attempts := append([][]message.Message(nil), impl.attempts...)
	impl.mu.Unlock()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want one request without replay degradation", len(attempts))
	}
	if got := client.replayCompatLevelFor(cfg.Name(), "gpt-5.6-sol", ""); got != modelcompat.ReplayCompatNative {
		t.Fatalf("replay compatibility level = %d, want native", got)
	}
}

// TestCompleteStreamDoesNotDegradeWhenNoForeignReplay guards against retrying a
// byte-identical request: when the request carries no foreign native replay
// items (ForeignNativeReplays == 0), a stricter replay level cannot change the
// wire payload, so escalating would only waste a billable retry on the same key.
// The client must fall through to normal failure handling after a single attempt.
func TestCompleteStreamDoesNotDegradeWhenNoForeignReplay(t *testing.T) {
	// Native (same-provenance) payload: the target's own reasoning output, so
	// normalization keeps it at every replay level and ForeignNativeReplays == 0.
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "continue"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-native"},
				{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "read", Arguments: `{}`},
			},
			ToolCalls: []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
			Provenance: &message.MessageProvenance{
				WireFamily: "openai-responses",
				ProviderID: "responses",
				ModelID:    "gpt-5.6-sol",
			},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "READ_RESULT ok"},
		{Role: message.RoleUser, Content: "go on"},
	}

	client, cfg, impl := replayTestClient(0)
	// Always reject so the call never succeeds; we only care about attempt
	// content. A hard cap of 2 rounds forces the outer retry loop to make at
	// most two attempts on the single key.
	impl.rejectCount = 100

	_, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		cfg,
		impl,
		"gpt-5.6-sol",
		1024,
		RequestTuning{},
		"",
		msgs,
		nil,
		nil,
		false,
		nil,
		-2,
		&CallStatus{},
	)
	if err == nil {
		t.Fatalf("expected error from always-rejecting provider")
	}
	impl.mu.Lock()
	attempts := impl.attempts
	impl.mu.Unlock()
	if len(attempts) < 2 {
		t.Fatalf("attempts = %d, want >= 2 to compare degradation", len(attempts))
	}
	// Without any foreign replay items, a stricter replay level cannot change
	// the wire payload. Both attempts must send a byte-identical message list;
	// if the second attempt differs, an ineffective degradation rewrote it and
	// wasted a billable retry that would fail the same way.
	for i := 1; i < len(attempts); i++ {
		if !reflect.DeepEqual(attempts[0], attempts[i]) {
			t.Fatalf("attempt %d differs from attempt 0: a stricter replay level changed the request despite no foreign replay items", i)
		}
	}
}

func TestCompleteStreamPrioritizesContextLengthOverReplayDegradation(t *testing.T) {
	client, cfg, impl := replayTestClient(0)
	impl.contextLengthExceeded = true

	_, err := callReplayTestStream(t, client, cfg, impl)
	if err == nil || !IsContextLengthExceeded(err) {
		t.Fatalf("CompleteStream error = %v, want context-length error", err)
	}
	impl.mu.Lock()
	attempts := len(impl.attempts)
	impl.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("attempts = %d, want one request without replay degradation", attempts)
	}
	if got := client.replayCompatLevelFor(cfg.Name(), "gpt-5.6-sol", ""); got != modelcompat.ReplayCompatNative {
		t.Fatalf("replay compatibility level = %d, want native after oversize rejection", got)
	}
}

func TestIsReasoningReplayRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"pairing 400", &APIError{StatusCode: 400, Message: "Item 'fc_1' of type 'function_call' was provided without its required 'reasoning' item"}, true},
		{"encrypted 400", &APIError{StatusCode: 400, Message: "could not decrypt encrypted_content"}, true},
		{"openai visible reasoning 400", &APIError{StatusCode: 400, Message: "The `reasoning_content` in the thinking mode must be passed back to the API."}, true},
		{"anthropic thinking 400", &APIError{StatusCode: 400, Message: "The `content[].thinking` in the thinking mode must be passed back to the API."}, true},
		{"gemini signature 400", &APIError{StatusCode: 400, Code: "INVALID_ARGUMENT", Message: "Function call is missing a valid thought signature"}, true},
		{"gemini signature structured 400", &APIError{StatusCode: 400, Code: "INVALID_ARGUMENT", Message: "thought signature validation failed"}, true},
		{"unrelated 400", &APIError{StatusCode: 400, Message: "invalid_request_error: unknown parameter"}, false},
		{"unrelated invalid argument", &APIError{StatusCode: 400, Code: "INVALID_ARGUMENT", Message: "invalid tool schema"}, false},
		{"reasoning 500", &APIError{StatusCode: 500, Message: "required 'reasoning' item"}, false},
		{"plain error", context.Canceled, false},
	}
	for _, tc := range cases {
		if got := isReasoningReplayRejection(tc.err); got != tc.want {
			t.Fatalf("%s: isReasoningReplayRejection = %v, want %v", tc.name, got, tc.want)
		}
	}
}
