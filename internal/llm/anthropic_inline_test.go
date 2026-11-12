package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestAnthropicInlineDeclarationBlocksRendersTextAndToolAdditions(t *testing.T) {
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{
		{Name: "mcp_sample_lookup", Description: "lookup", InputSchema: map[string]any{"type": "object"}},
		{Name: "mcp_sample_fetch", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}}},
	})
	blocks, err := anthropicInlineDeclarationBlocks(declaration)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (one tool_addition per tool)", len(blocks))
	}
	for i, def := range declaration.MCPTools {
		if blocks[i].Type != anthropicInlineToolAdditionType || blocks[i].Tool == nil {
			t.Fatalf("block %d = %+v, want tool_addition", i, blocks[i])
		}
		if blocks[i].Tool.Type != "tool_definition" {
			t.Fatalf("block %d tool type = %q, want tool_definition", i, blocks[i].Tool.Type)
		}
		if blocks[i].Tool.Definition.Name != def.Name || blocks[i].Tool.Definition.Description != def.Description {
			t.Fatalf("block %d definition = %#v, want %#v", i, blocks[i].Tool.Definition, def)
		}
	}

	// A non-empty content rides along as a leading text block.
	textual := declaration
	textual.Content = "Additional sample tools are available."
	blocks, err = anthropicInlineDeclarationBlocks(textual)
	if err != nil {
		t.Fatalf("render with text: %v", err)
	}
	if len(blocks) != 3 || blocks[0].Type != "text" || blocks[0].Text != textual.Content {
		t.Fatalf("blocks with text = %#v", blocks)
	}
}

func TestAnthropicInlineDeclarationBlocksRejectsUnrenderablePayload(t *testing.T) {
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup"}})
	for _, tc := range []struct {
		name   string
		mutate func(*message.Message)
	}{
		{"assistant role", func(m *message.Message) { m.Role = message.RoleAssistant }},
		{"tool calls", func(m *message.Message) { m.ToolCalls = []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup"}} }},
		{"parts", func(m *message.Message) {
			m.Parts = []message.ContentPart{{Type: message.ContentPartText, Text: "attached"}}
		}},
		{"responses output", func(m *message.Message) { m.ResponsesOutput = []message.ResponsesOutputItem{{Type: "message"}} }},
		{"native tools", func(m *message.Message) { m.NativeTools = &message.NativeToolHistory{} }},
		{"thinking blocks", func(m *message.Message) { m.ThinkingBlocks = []message.ThinkingBlock{{Thinking: "reasoning"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := declaration
			tc.mutate(&msg)
			if _, err := anthropicInlineDeclarationBlocks(msg); err == nil {
				t.Fatalf("%s rendered without error", tc.name)
			}
			if anthropicInlineDeclarationsRenderable([]message.Message{{Role: message.RoleUser, Content: "turn"}, msg}) {
				t.Fatalf("%s reported renderable", tc.name)
			}
		})
	}
}

func TestConvertMessagesRendersInlineDeclarations(t *testing.T) {
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{
		{Name: "mcp_sample_lookup", Description: "lookup", InputSchema: map[string]any{"type": "object"}},
	})
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		declaration,
		{Role: message.RoleAssistant, Content: "done"},
	}
	wire, messageMap := convertMessagesWithMap(msgs)
	if len(wire) != 3 {
		t.Fatalf("wire messages = %d, want 3: %#v", len(wire), wire)
	}
	if wire[1].Role != "system" {
		t.Fatalf("wire[1].Role = %q, want system", wire[1].Role)
	}
	blocks, ok := wire[1].Content.([]anthropicContent)
	if !ok || len(blocks) != 1 {
		t.Fatalf("wire[1].Content = %#v, want one tool_addition block", wire[1].Content)
	}
	if blocks[0].Type != anthropicInlineToolAdditionType || blocks[0].Tool == nil || blocks[0].Tool.Definition.Name != "mcp_sample_lookup" {
		t.Fatalf("tool_addition block = %#v", blocks[0])
	}
	if entry := messageMap[1]; !entry.Valid || entry.MessageIndex != 1 {
		t.Fatalf("messageMap[1] = %+v, want the declaration wire position", messageMap[1])
	}
	if entry := messageMap[2]; !entry.Valid || entry.MessageIndex != 2 {
		t.Fatalf("messageMap[2] = %+v, want the assistant shifted past the declaration", messageMap[2])
	}

	// A system message without declarations never reaches the wire.
	skipped, _ := convertMessagesWithMap([]message.Message{
		{Role: message.RoleUser, Content: "turn"},
		{Role: message.RoleSystem, Content: "runtime note"},
	})
	if len(skipped) != 1 {
		t.Fatalf("wire without declarations = %d messages, want 1", len(skipped))
	}
}

func TestAnthropicBetaHeaderIncludesInlineToolsBeta(t *testing.T) {
	with := anthropicBetaHeader(AnthropicTuning{}, 200000, true)
	if !strings.Contains(with, AnthropicInlineToolsBeta) {
		t.Fatalf("anthropic-beta = %q, want %s", with, AnthropicInlineToolsBeta)
	}
	without := anthropicBetaHeader(AnthropicTuning{}, 200000, false)
	if strings.Contains(without, AnthropicInlineToolsBeta) {
		t.Fatalf("anthropic-beta = %q, want no inline-tools beta without declarations", without)
	}
}

func TestAnthropicCompleteStreamSetsInlineToolsBetaWithDeclarations(t *testing.T) {
	type capturedRequest struct {
		Header http.Header
		Body   anthropicRequest
	}
	var captured capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Header = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced test error"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL}, []string{"test-key"})
	impl, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{
		{Name: "mcp_sample_lookup", InputSchema: map[string]any{"type": "object"}},
	})
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		declaration,
		{Role: message.RoleAssistant, Content: "done"},
		{Role: message.RoleUser, Content: "next"},
	}
	_, err = impl.CompleteStream(context.Background(), "test-key", "claude-sonnet", "system", msgs, nil, 2048, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if got := captured.Header.Get("anthropic-beta"); !strings.Contains(got, AnthropicInlineToolsBeta) {
		t.Fatalf("anthropic-beta = %q, want %s with declarations present", got, AnthropicInlineToolsBeta)
	}
	systems := 0
	for _, m := range captured.Body.Messages {
		if m.Role != "system" {
			continue
		}
		systems++
		blocks, ok := m.Content.([]any)
		if !ok || len(blocks) != 1 {
			t.Fatalf("system message content = %#v, want one tool_addition block", m.Content)
		}
		block, ok := blocks[0].(map[string]any)
		if !ok || block["type"] != anthropicInlineToolAdditionType {
			t.Fatalf("system block = %#v, want type %s", blocks[0], anthropicInlineToolAdditionType)
		}
	}
	if systems != 1 {
		t.Fatalf("system messages on wire = %d, want 1", systems)
	}

	// The same request without declarations sends no beta flag.
	msgs = []message.Message{{Role: message.RoleUser, Content: "hello"}}
	_, err = impl.CompleteStream(context.Background(), "test-key", "claude-sonnet", "system", msgs, nil, 2048, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if got := captured.Header.Get("anthropic-beta"); strings.Contains(got, AnthropicInlineToolsBeta) {
		t.Fatalf("anthropic-beta = %q, want no inline-tools beta without declarations", got)
	}
}

func TestAnthropicCacheBoundaryResolvesAcrossInlineDeclarations(t *testing.T) {
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{
		{Name: "mcp_sample_lookup", InputSchema: map[string]any{"type": "object"}},
	})
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "session reminder"},
		declaration,
		{Role: message.RoleUser, Content: "stable-prefix-end"},
		{Role: message.RoleAssistant, Content: "old assistant"},
		{Role: message.RoleUser, Content: "last user"},
	}
	apiMessages, messageMap := convertMessagesWithMap(msgs)
	boundary := resolveAnthropicCacheBoundary(AnthropicCacheBoundary{MessageIndex: 2, Valid: true}, messageMap)
	if !boundary.Valid || boundary.MessageIndex != 2 || boundary.BlockIndex != 0 {
		t.Fatalf("resolved boundary = %+v, want wire message 2 block 0 (the declaration shifted the source index)", boundary)
	}

	system := []anthropicContent{{Type: "text", Text: "system"}}
	applyCacheBreakpoints(system, apiMessages, boundary, AnthropicCacheBoundary{}, "", 0)
	texts := cacheControlBlockTexts(apiMessages)
	if len(texts) == 0 || texts[0] != "stable-prefix-end" {
		t.Fatalf("cache_control texts = %#v, want the breakpoint on stable-prefix-end", texts)
	}
}

func TestAnthropicExplicitCacheProjectionKeepsDeclarationsDurable(t *testing.T) {
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{
		{Name: "mcp_sample_lookup", InputSchema: map[string]any{"type": "object"}},
	})
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "session reminder"},
		declaration,
		{Role: message.RoleUser, Content: "runtime hint", Kind: message.KindTurnOverlay},
	}
	tuning := RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "explicit", PromptCacheTTL: "1h"}}
	projected := promptCacheMessagesForTarget(modelcompat.WireFamilyAnthropic, tuning, false, msgs, 1)
	if len(projected) != 2 {
		t.Fatalf("explicit projection = %d messages, want 2: %#v", len(projected), projected)
	}
	if len(projected[1].MCPTools) == 0 {
		t.Fatalf("projection dropped the declaration: %#v", projected)
	}
	if got := anthropicCacheDurableMessageCount(msgs); got != 2 {
		t.Fatalf("durable count = %d, want 2 (declaration durable, overlay stripped)", got)
	}
}

func TestAnthropicInlineTargetAcceptsDeclarations(t *testing.T) {
	target := FallbackModel{
		ProviderConfig: NewProviderConfig("sample", config.ProviderConfig{
			Type:   config.ProviderTypeMessages,
			APIURL: "https://example.invalid/v1",
			Compat: &config.ProviderCompatConfig{Anthropic: &config.AnthropicCompatConfig{MCPInlineTools: new(true)}},
			Models: map[string]config.ModelConfig{"claude-sonnet": {}},
		}, []string{"test-key"}),
		ModelID: "claude-sonnet",
	}
	if !targetAcceptsMCPDeclarations(target) {
		t.Fatal("an inline-tools target must accept message-level declarations")
	}
	if !anthropicInlineDeclarationsRenderable([]message.Message{
		{Role: message.RoleUser, Content: "first"},
		message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup"}}),
		{Role: message.RoleAssistant, Content: "done"},
	}) {
		t.Fatal("a legal projection must render inline")
	}
	// The non-folding path still validates the final placement shape.
	wire, _ := convertMessagesWithMap([]message.Message{
		{Role: message.RoleUser, Content: "first"},
		message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup"}}),
		{Role: message.RoleAssistant, Content: "done"},
	})
	if err := validateAnthropicInlinePlacement(wire); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicInlineInvalidPlacementDoesNotSendRequest(t *testing.T) {
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup"}})
	user := message.Message{Role: message.RoleUser, Content: "first"}
	assistant := message.Message{Role: message.RoleAssistant, Content: "done"}
	for _, tc := range []struct {
		name     string
		messages []message.Message
	}{
		{"at start", []message.Message{declaration, assistant}},
		{"after assistant", []message.Message{user, assistant, declaration}},
		{"before user", []message.Message{user, declaration, user}},
		{"removed assistant", []message.Message{user, declaration, {Role: message.RoleAssistant}, user}},
		{"removed tool result", []message.Message{user, assistant, {Role: message.RoleTool}, declaration}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer srv.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL}, []string{"test-key"})
			impl, err := NewAnthropicProvider(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = impl.CompleteStream(t.Context(), "test-key", "test-model", "", tc.messages, nil, 512, RequestTuning{}, nil)
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.StatusCode != 400 || !strings.Contains(apiErr.Message, "compat.anthropic.mcp_inline_tools") {
				t.Fatalf("error = %v, want local placement error with configuration hint", err)
			}
			if requests.Load() != 0 {
				t.Fatal("invalid placement reached the HTTP server")
			}
		})
	}
}

func TestAnthropicInlinePlacementAcceptsGroupedDeclarationsAndToolResults(t *testing.T) {
	declaration := message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup"}})
	for _, messages := range [][]message.Message{
		{{Role: message.RoleUser, Content: "first"}, declaration},
		{{Role: message.RoleUser, Content: "first"}, declaration, declaration, {Role: message.RoleAssistant, Content: "done"}},
		{{Role: message.RoleUser, Content: "first"}, {Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}}}, {Role: message.RoleTool, ToolCallID: "call-1", Content: "result"}, declaration},
	} {
		wire, _ := convertMessagesWithMap(messages)
		if err := validateAnthropicInlinePlacement(wire); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSupportsAnthropicInlineToolsProviderAndModelConfig(t *testing.T) {
	providerDefault := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: "https://example.invalid/v1",
		Compat: &config.ProviderCompatConfig{Anthropic: &config.AnthropicCompatConfig{MCPInlineTools: new(true)}},
		Models: map[string]config.ModelConfig{
			"claude-sonnet": {},
		},
	}, []string{"test-key"})
	client := NewClient(providerDefault, &recordingProvider{}, "claude-sonnet", 512, "")
	if !client.SupportsAnthropicInlineTools("sample/claude-sonnet") {
		t.Fatal("provider-level mcp_inline_tools should be the default")
	}

	modelOverride := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: "https://example.invalid/v1",
		Compat: &config.ProviderCompatConfig{Anthropic: &config.AnthropicCompatConfig{MCPInlineTools: new(true)}},
		Models: map[string]config.ModelConfig{
			"claude-sonnet": {
				Compat: &config.ModelCompatConfig{Anthropic: &config.AnthropicCompatConfig{MCPInlineTools: new(false)}},
			},
		},
	}, []string{"test-key"})
	client = NewClient(modelOverride, &recordingProvider{}, "claude-sonnet", 512, "")
	if client.SupportsAnthropicInlineTools("sample/claude-sonnet") {
		t.Fatal("model-level false must override provider-level true")
	}

	withoutOptIn := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: "https://example.invalid/v1",
		Models: map[string]config.ModelConfig{"claude-sonnet": {}},
	}, []string{"test-key"})
	client = NewClient(withoutOptIn, &recordingProvider{}, "claude-sonnet", 512, "")
	if client.SupportsAnthropicInlineTools("sample/claude-sonnet") {
		t.Fatal("the switch must default to off without explicit opt-in")
	}
}

// TestNativeToolsAcceptCallsToDeclaredMCPTools locks the callable-set rule: a
// native-tool request treats the top-level defs unioned with the request's
// message-level declarations as the callable client tool set, while a call to
// a tool in neither set stays a terminal native error.
func TestNativeToolsAcceptCallsToDeclaredMCPTools(t *testing.T) {
	for _, tc := range []struct {
		name     string
		call     string
		declared bool
		wantErr  bool
	}{
		{"declared mcp tool", "mcp_sample_lookup", true, false},
		{"undeclared tool", "mcp_sample_other", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				output := fmt.Sprintf(`[{"type":"function_call","id":"fc-1","call_id":"call-1","name":%q,"arguments":"{}"}]`, tc.call)
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":%s}}\n\n", output)
			}))
			defer srv.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key-1"})
			impl, err := NewResponsesProvider(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(context.Context, NativeRequestRecord) (string, error) { return "request-1", nil }, Finish: func(string, message.NativeRequestOutcome, *message.Response, error) error { return nil }}
			ctx := context.WithValue(t.Context(), nativePolicyContextKey{}, policy)
			var declarations []message.ToolDefinition
			if tc.declared {
				declarations = []message.ToolDefinition{{Name: "mcp_sample_lookup", InputSchema: map[string]any{"type": "object"}}}
			}
			_, err = completeWithNativeTools(ctx, impl, provider, "key-1", "test-model", "", []message.Message{{Role: message.RoleUser, Content: "Search for it"}}, nil, declarations, 1024, RequestTuning{}, config.ServiceTierStandard, nil)
			if tc.wantErr {
				if !IsNativeToolError(err) {
					t.Fatalf("undeclared call err = %v, want a terminal native error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("declared call failed: %v", err)
			}
		})
	}
}
