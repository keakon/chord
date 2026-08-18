package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// wireRC dereferences an outbound message's reasoning_content pointer for
// assertions; nil (field absent from the wire request) maps to "".
func wireRC(m openAIMessage) string {
	if m.ReasoningContent == nil {
		return ""
	}
	return *m.ReasoningContent
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningContentByDefault(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "do something"},
		{
			Role:             "assistant",
			ReasoningContent: "I will call a tool now.",
			Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)},
			},
			StopReason: "tool_calls",
		},
		{Role: "tool", ToolCallID: "c1", Content: "hi\n"},
	}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	if len(out) < 2 {
		t.Fatalf("got %d messages, want >= 2", len(out))
	}
	var assistantWithToolCall *openAIMessage
	for i := range out {
		m := &out[i]
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			assistantWithToolCall = m
			break
		}
	}
	if assistantWithToolCall == nil {
		t.Fatal("expected an assistant tool_call message")
	}
	if wireRC(*assistantWithToolCall) != "" {
		t.Fatalf("ReasoningContent = %q, want empty", wireRC(*assistantWithToolCall))
	}
	for _, m := range out {
		if m.Role == "assistant" && wireRC(m) != "" && len(m.ToolCalls) == 0 && m.Content == nil {
			t.Fatalf("unexpected standalone reasoning-only assistant message: %#v", m)
		}
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningContentForOpenAIChatByDefault(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "deepseek thinking",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
	}}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	var replayed bool
	for _, m := range out {
		if m.Role == "assistant" && wireRC(m) == "deepseek thinking" && len(m.ToolCalls) > 0 {
			replayed = true
			break
		}
	}
	if replayed {
		t.Fatal("unexpected reasoning_content replay on assistant tool_call message for openai-chat provenance")
	}
}

func TestConvertMessagesToOpenAI_ReplaysReasoningContentWhenOpenAIVisibleContinuityEnabled(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "glm preserved reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
	}}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityOpenAIVisible, msgs)
	var replayed bool
	for _, m := range out {
		if m.Role == "assistant" && wireRC(m) == "glm preserved reasoning" && len(m.ToolCalls) > 0 {
			replayed = true
			break
		}
	}
	if !replayed {
		t.Fatal("expected reasoning_content replay when openai_visible continuity is enabled")
	}
}

func TestConvertMessagesToOpenAI_SkipsReasoningOnlyAssistant(t *testing.T) {
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, []message.Message{
		{Role: "user", Content: "before"},
		{Role: "assistant", ReasoningContent: "hidden", Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat}},
		{Role: "user", Content: "after"},
	})
	if len(out) != 2 {
		t.Fatalf("convertMessagesToOpenAI() len = %d, want 2: %#v", len(out), out)
	}
	for _, msg := range out {
		if msg.Role == "assistant" {
			t.Fatalf("reasoning-only assistant was not skipped: %#v", out)
		}
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningForNonOpenAITarget(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "hidden reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyAnthropic, modelcompat.ReasoningContinuityNone, msgs)
	for _, m := range out {
		if wireRC(m) != "" {
			t.Fatalf("unexpected reasoning replay for non-openai target: %#v", m)
		}
	}
}

func TestConvertMessagesToOpenAI_ReplaysPortableReasoningWithoutProvenance(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "hidden reasoning",
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityOpenAIVisible, msgs)
	var replayed bool
	for _, m := range out {
		if m.Role == "assistant" && wireRC(m) == "hidden reasoning" && len(m.ToolCalls) > 0 {
			replayed = true
			break
		}
	}
	if !replayed {
		t.Fatalf("expected portable reasoning replay once normalization has preserved it: %#v", out)
	}
}

func TestConvertMessagesToOpenAI_ReplaysPortableReasoningWithNonOpenAIChatProvenance(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "foreign reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyGemini},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityOpenAIVisible, msgs)
	var replayed bool
	for _, m := range out {
		if m.Role == "assistant" && wireRC(m) == "foreign reasoning" && len(m.ToolCalls) > 0 {
			replayed = true
			break
		}
	}
	if !replayed {
		t.Fatalf("expected portable reasoning replay once normalization has preserved it: %#v", out)
	}
}

func TestConvertMessagesToOpenAIMarksInterruptedAssistant(t *testing.T) {
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, []message.Message{{Role: "assistant", Content: "partial", StopReason: "interrupted"}})
	if len(out) != 1 || out[0].Role != "assistant" {
		t.Fatalf("convertMessagesToOpenAI() = %#v", out)
	}
	text, ok := out[0].Content.(string)
	if !ok {
		t.Fatalf("assistant content type = %T", out[0].Content)
	}
	if !strings.Contains(text, "partial") || !strings.Contains(text, "interrupted before completion") {
		t.Fatalf("interrupted assistant content = %q", text)
	}
}

func TestConvertMessagesToOpenAI_ToolOutputWithImageParts(t *testing.T) {
	msgs := []message.Message{{
		Role:       "tool",
		ToolCallID: "c1",
		Content:    "Loaded image",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Loaded image"},
			{Type: "image", MimeType: "image/png", Data: []byte("png")},
		},
	}}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	if len(out) != 1 || out[0].Role != "tool" || out[0].ToolCallID != "c1" {
		t.Fatalf("tool message = %#v", out)
	}
	if out[0].Content != "Loaded image" {
		t.Fatalf("content = %#v, want text-only tool result", out[0].Content)
	}
}

func TestConvertMessagesToOpenAIWithOptions_ToolResultName(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	out := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs, openAIConvertOptions{requiresToolResultName: true})
	if len(out) != 2 || out[1].Name != "Read" {
		t.Fatalf("tool result name not emitted: %#v", out)
	}

	out = convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	if len(out) != 2 || out[1].Name != "" {
		t.Fatalf("tool result name must be omitted by default: %#v", out)
	}
}

func TestConvertMessagesToOpenAIWithOptions_ToolResultNameUsesPrecedingCall(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "first"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Write", Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "second"},
	}
	out := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs, openAIConvertOptions{requiresToolResultName: true})
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(out), out)
	}
	if out[1].Name != "Read" || out[3].Name != "Write" {
		t.Fatalf("tool result names = %q, %q; want preceding call names", out[1].Name, out[3].Name)
	}
}

func TestConvertMessagesToOpenAIWithOptions_AssistantAfterToolResult(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
		{Role: message.RoleUser, Content: "next"},
	}
	out := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs, openAIConvertOptions{requiresAssistantAfterToolResult: true})
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(out), out)
	}
	if out[2].Role != "assistant" || out[2].Content != assistantAfterToolResultText {
		t.Fatalf("synthetic assistant not inserted: %#v", out[2])
	}
	if out[3].Role != "user" || out[3].Content != "next" {
		t.Fatalf("user message misplaced: %#v", out[3])
	}

	out = convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3 without the compat flag: %#v", len(out), out)
	}
}

func TestConvertMessagesToOpenAIWithOptions_AssistantAfterToolResultIgnoresSkippedAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
		{Role: message.RoleAssistant, ReasoningContent: "hidden reasoning"},
		{Role: message.RoleUser, Content: "next"},
	}
	out := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs, openAIConvertOptions{requiresAssistantAfterToolResult: true})
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(out), out)
	}
	if out[2].Role != "assistant" || out[2].Content != assistantAfterToolResultText {
		t.Fatalf("synthetic assistant not inserted after skipped assistant: %#v", out[2])
	}
	if out[3].Role != "user" || out[3].Content != "next" {
		t.Fatalf("user message misplaced: %#v", out[3])
	}
}

func TestOpenAICompleteStream_GLMPreservedContinuityAddsThinkingAndUsesMaxTokens(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("glm-main", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL,
		Models: map[string]config.ModelConfig{
			"glm-5.2": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
					RequestOverrides: &config.RequestOverridesConfig{RenameBodyFields: map[string]*string{
						"max_completion_tokens": new("max_tokens"),
					}, Body: map[string]any{
						"thinking": map[string]any{"type": "enabled", "clear_thinking": false},
					}},
				},
			},
		},
	}, []string{"k"})
	r, err := NewOpenAIProviderWithClient(provider, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}

	_, err = r.CompleteStream(
		context.Background(),
		"k",
		"glm-5.2",
		"",
		[]message.Message{{
			Role:             message.RoleAssistant,
			Content:          "Calling tool.",
			ReasoningContent: "preserved reasoning",
			Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
			ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
		}},
		nil,
		128,
		RequestTuning{OpenAI: OpenAITuning{ReasoningEffort: "max"}},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	thinking, ok := gotBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking object, got %#v", gotBody["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %#v, want enabled", thinking["type"])
	}
	if thinking["clear_thinking"] != false {
		t.Fatalf("thinking.clear_thinking = %#v, want false", thinking["clear_thinking"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	first, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatalf("first message = %#v", msgs[0])
	}
	if first["reasoning_content"] != "preserved reasoning" {
		t.Fatalf("reasoning_content = %#v, want preserved reasoning", first["reasoning_content"])
	}
	if _, ok := gotBody["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls should be omitted without tools, got %#v", gotBody["parallel_tool_calls"])
	}
	if gotBody["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", gotBody["reasoning_effort"])
	}
	if gotBody["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens = %#v, want 128", gotBody["max_tokens"])
	}
	if _, ok := gotBody["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens should be omitted, got %#v", gotBody["max_completion_tokens"])
	}
}

func TestOpenAICompleteStream_OpenAIVisibleDoesNotInjectRequestFields(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("compatible", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL,
		Models: map[string]config.ModelConfig{
			"reasoning-model": {Compat: &config.ModelCompatConfig{
				ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
			}},
		},
	}, []string{"k"})
	r, err := NewOpenAIProviderWithClient(provider, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}

	_, err = r.CompleteStream(context.Background(), "k", "reasoning-model", "", []message.Message{{
		Role:             message.RoleAssistant,
		ReasoningContent: "tool reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
	}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	if _, ok := gotBody["thinking"]; ok {
		t.Fatalf("thinking should require request_overrides, got %#v", gotBody["thinking"])
	}
	messages := gotBody["messages"].([]any)
	if got := messages[0].(map[string]any)["reasoning_content"]; got != "tool reasoning" {
		t.Fatalf("reasoning_content = %#v, want tool reasoning", got)
	}
}

func TestOpenAICompleteStream_OpenAIVisibleReplayUsesRequestOverrides(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("deepseek", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Compat: &config.ModelCompatConfig{
				ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
				RequestOverrides: &config.RequestOverridesConfig{Body: map[string]any{
					"thinking": map[string]any{"type": "enabled"},
				}},
			}},
		},
	}, []string{"k"})
	r, err := NewOpenAIProviderWithClient(provider, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}

	_, err = r.CompleteStream(context.Background(), "k", "deepseek-v4-pro", "", []message.Message{{
		Role:             message.RoleAssistant,
		ReasoningContent: "tool reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
	}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	thinking := gotBody["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %#v, want enabled", thinking["type"])
	}
	if _, ok := thinking["clear_thinking"]; ok {
		t.Fatalf("thinking.clear_thinking should be omitted, got %#v", thinking["clear_thinking"])
	}
	msgs := gotBody["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["reasoning_content"] != "tool reasoning" {
		t.Fatalf("reasoning_content = %#v, want tool reasoning", first["reasoning_content"])
	}
}

func TestOpenAICompleteStream_DisableReasoningRemovesRequestControls(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("deepseek", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-flash": {
				Reasoning: &config.ReasoningConfig{Effort: "high"},
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
					RequestOverrides: &config.RequestOverridesConfig{Body: map[string]any{
						"thinking": map[string]any{"type": "enabled"},
					}},
				},
			},
		},
	}, []string{"k"})
	impl, err := NewOpenAIProviderWithClient(provider, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}
	target := FallbackModel{ProviderConfig: provider, ModelID: "deepseek-v4-flash"}
	tuning := tuningForPoolTarget(target)
	tuning.DisableReasoning = true
	_, err = impl.CompleteStream(context.Background(), "k", target.ModelID, "", []message.Message{{
		Role:       message.RoleAssistant,
		ToolCalls:  []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
		Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyAnthropic},
	}}, nil, 128, tuning, func(message.StreamDelta) {})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	for _, key := range []string{"thinking", "reasoning", "reasoning_effort", "max_completion_tokens"} {
		if _, ok := gotBody[key]; ok {
			t.Fatalf("disabled reasoning request retained %q: %#v", key, gotBody)
		}
	}
	if gotBody["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens = %#v, want 128", gotBody["max_tokens"])
	}
}

func TestFillCurrentTurnEmptyReasoning(t *testing.T) {
	msgs := []message.Message{
		{
			Role:       "assistant",
			Content:    "prior turn",
			ToolCalls:  []message.ToolCall{{ID: "c0", Name: "Read", Args: json.RawMessage(`{}`)}},
			Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: "tool", ToolCallID: "c0", Content: "ok"},
		{Role: "user", Content: "continue"},
		{
			Role:             "assistant",
			ReasoningContent: "thought",
			ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{}`)}},
			Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{
			Role:       "assistant",
			ToolCalls:  []message.ToolCall{{ID: "c2", Name: "Shell", Args: json.RawMessage(`{}`)}},
			Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: "tool", ToolCallID: "c2", Content: "ok"},
		{Role: "assistant", Content: "done"},
	}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityOpenAIVisible, msgs)
	fillCurrentTurnEmptyReasoning(out)

	byToolID := func(id string) *openAIMessage {
		for i := range out {
			for _, tc := range out[i].ToolCalls {
				if tc.ID == id {
					return &out[i]
				}
			}
		}
		return nil
	}
	if m := byToolID("c0"); m == nil || m.ReasoningContent != nil {
		t.Fatalf("prior-turn tool call must not gain a reasoning_content field: %#v", m)
	}
	if m := byToolID("c1"); m == nil || m.ReasoningContent == nil || *m.ReasoningContent != "thought" {
		t.Fatalf("current-turn reasoning replay lost: %#v", m)
	}
	poisoned := byToolID("c2")
	if poisoned == nil || poisoned.ReasoningContent == nil || *poisoned.ReasoningContent != "" {
		t.Fatalf("current-turn reasoningless tool call must gain an empty field: %#v", poisoned)
	}
	for i := range out {
		if out[i].Role == "assistant" && len(out[i].ToolCalls) == 0 && out[i].ReasoningContent != nil {
			t.Fatalf("plain assistant must not gain a reasoning_content field: %#v", out[i])
		}
	}

	raw, err := json.Marshal(poisoned)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"reasoning_content":""`) {
		t.Fatalf("empty reasoning_content field missing from wire JSON: %s", raw)
	}
	rawPrior, err := json.Marshal(byToolID("c0"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(rawPrior), "reasoning_content") {
		t.Fatalf("prior-turn message must omit reasoning_content entirely: %s", rawPrior)
	}
}

func TestOpenAICompleteStream_FillsCurrentTurnEmptyReasoningEvenWhenDisabled(t *testing.T) {
	// DisableReasoning only strips request-side thinking controls; endpoints
	// that enable thinking server-side keep validating reasoning_content
	// passback, so the fill must run in both cases.
	for _, tc := range []struct {
		name             string
		disableReasoning bool
		wantField        bool
	}{
		{name: "reasoning active fills empty field", disableReasoning: false, wantField: true},
		{name: "reasoning disabled still fills empty field", disableReasoning: true, wantField: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			provider := NewProviderConfig("deepseek", config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				APIURL: server.URL,
				Models: map[string]config.ModelConfig{
					"deepseek-v4-flash": {
						Compat: &config.ModelCompatConfig{
							ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
						},
					},
				},
			}, []string{"k"})
			impl, err := NewOpenAIProviderWithClient(provider, server.Client(), "")
			if err != nil {
				t.Fatalf("NewOpenAIProviderWithClient: %v", err)
			}
			tuning := tuningForPoolTarget(FallbackModel{ProviderConfig: provider, ModelID: "deepseek-v4-flash"})
			tuning.DisableReasoning = tc.disableReasoning
			_, err = impl.CompleteStream(context.Background(), "k", "deepseek-v4-flash", "", []message.Message{
				{Role: message.RoleUser, Content: "continue"},
				{
					Role:       message.RoleAssistant,
					ToolCalls:  []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
					Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
				},
				{Role: message.RoleTool, ToolCallID: "c1", Content: "ok"},
			}, nil, 128, tuning, func(message.StreamDelta) {})
			if err != nil {
				t.Fatalf("CompleteStream returned error: %v", err)
			}
			rawMsgs, ok := gotBody["messages"].([]any)
			if !ok {
				t.Fatalf("request body missing messages: %#v", gotBody)
			}
			var sawField bool
			for _, rm := range rawMsgs {
				m, ok := rm.(map[string]any)
				if !ok || m["role"] != "assistant" {
					continue
				}
				if rc, present := m["reasoning_content"]; present {
					if rc != "" {
						t.Fatalf("reasoning_content = %#v, want empty string", rc)
					}
					sawField = true
				}
			}
			if sawField != tc.wantField {
				t.Fatalf("reasoning_content field present = %v, want %v (body %#v)", sawField, tc.wantField, gotBody)
			}
		})
	}
}

func TestOpenAICompleteStream_ToolOnlyFieldsGatedOnTools(t *testing.T) {
	var gotBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		gotBodies = append(gotBodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL,
	}, []string{"test-key"})
	r, err := NewOpenAIProviderWithClient(provider, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}

	// No tools: parallel_tool_calls and tool_choice must be omitted even when
	// explicitly tuned, or OpenAI-compatible endpoints reject the body with 400.
	_, err = r.CompleteStream(
		context.Background(), "test-key", "test-model",
		"", []message.Message{{Role: message.RoleUser, Content: "hello"}},
		nil, 0,
		RequestTuning{OpenAI: OpenAITuning{ParallelToolCalls: new(false), ToolChoice: "required"}},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream without tools: %v", err)
	}
	if _, ok := gotBodies[0]["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls should be omitted without tools, got %#v", gotBodies[0]["parallel_tool_calls"])
	}
	if _, ok := gotBodies[0]["tool_choice"]; ok {
		t.Fatalf("tool_choice should be omitted without tools, got %#v", gotBodies[0]["tool_choice"])
	}

	// With tools: default parallel_tool_calls stays true.
	_, err = r.CompleteStream(
		context.Background(), "test-key", "test-model",
		"", []message.Message{{Role: message.RoleUser, Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
		0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream with tools: %v", err)
	}
	if gotBodies[1]["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v, want true with tools", gotBodies[1]["parallel_tool_calls"])
	}
}
