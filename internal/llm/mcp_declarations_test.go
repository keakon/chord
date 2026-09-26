package llm

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestTargetAcceptsMCPDeclarationsFollowsWireAndCompat(t *testing.T) {
	target := func(providerType string, chatOptIn, responsesOptIn bool) FallbackModel {
		return FallbackModel{
			ProviderConfig: mcpDynamicProviderConfig(providerType, "model-1", chatOptIn, responsesOptIn),
			ModelID:        "model-1",
		}
	}
	cases := []struct {
		name   string
		target FallbackModel
		want   bool
	}{
		{"chat target opted in", target(config.ProviderTypeChatCompletions, true, false), true},
		{"chat target without opt-in", target(config.ProviderTypeChatCompletions, false, false), false},
		{"responses target opted in", target(config.ProviderTypeResponses, false, true), true},
		{"responses target without opt-in", target(config.ProviderTypeResponses, false, false), false},
	}
	for _, tc := range cases {
		if got := targetAcceptsMCPDeclarations(tc.target); got != tc.want {
			t.Fatalf("%s: targetAcceptsMCPDeclarations = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestInlineMCPToolDeclarationsFoldsDeclarationsIntoTopLevelTools(t *testing.T) {
	defs := []message.ToolDefinition{{
		Name:        "mcp_sample_lookup",
		Description: "lookup",
		InputSchema: map[string]any{"type": "object"},
	}}
	messages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		message.NewSystemToolsMessage(defs),
		{Role: message.RoleAssistant, Content: "done"},
	}
	tools := []message.ToolDefinition{{Name: "read", Description: "read files"}}

	gotMessages, gotTools := inlineMCPToolDeclarations(messages, tools, defs)

	if len(gotMessages) != 2 {
		t.Fatalf("declaration message not dropped: %#v", gotMessages)
	}
	if gotMessages[1].Role != message.RoleAssistant {
		t.Fatalf("messages reordered: %#v", gotMessages)
	}
	if len(gotTools) != 2 || gotTools[0].Name != "read" || gotTools[1].Name != "mcp_sample_lookup" {
		t.Fatalf("merged tools = %#v", gotTools)
	}
	// The inputs stay untouched; every target adapts its own view.
	if len(messages) != 3 || len(messages[1].MCPTools) != 1 || len(tools) != 1 {
		t.Fatalf("inputs mutated: messages=%#v tools=%#v", messages, tools)
	}
}

func TestInlineMCPToolDeclarationsDeduplicatesByName(t *testing.T) {
	defs := []message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "dynamic"}}
	messages := []message.Message{
		message.NewSystemToolsMessage(defs),
		{Role: message.RoleUser, Content: "hello"},
	}
	tools := []message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "top-level"}}

	gotMessages, gotTools := inlineMCPToolDeclarations(messages, tools, defs)
	if len(gotMessages) != 1 {
		t.Fatalf("declaration message not dropped: %#v", gotMessages)
	}
	if len(gotTools) != 1 || gotTools[0].Description != "top-level" {
		t.Fatalf("top-level definition must win on duplicate names: %#v", gotTools)
	}
}

// Mount snapshots are append-only, so only the request's explicitly passed
// current set is advertised: snapshots left in the transcript must not
// resurrect tools that were replaced or disabled since.
func TestInlineMCPToolDeclarationsAdvertisesOnlyTheCurrentSet(t *testing.T) {
	messages := []message.Message{
		message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "stale"}}),
		{Role: message.RoleUser, Content: "hello"},
		message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "current"}}),
	}
	current := []message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "current"}}

	gotMessages, gotTools := inlineMCPToolDeclarations(messages, nil, current)
	if len(gotMessages) != 1 {
		t.Fatalf("declaration messages not dropped: %#v", gotMessages)
	}
	if len(gotTools) != 1 || gotTools[0].Description != "current" {
		t.Fatalf("the current set must win: %#v", gotTools)
	}
}

// A tool unregistered mid-session may still be declared by an earlier
// transcript snapshot; folding must not re-advertise it to a fallback target
// that cannot carry dynamic declarations.
func TestInlineMCPToolDeclarationsDoesNotAdvertiseDisabledTools(t *testing.T) {
	messages := []message.Message{
		message.NewSystemToolsMessage([]message.ToolDefinition{
			{Name: "mcp_sample_lookup", Description: "lookup"},
			{Name: "mcp_sample_fetch", Description: "fetch"},
		}),
		{Role: message.RoleUser, Content: "hello"},
	}
	current := []message.ToolDefinition{{Name: "mcp_sample_fetch", Description: "fetch"}}

	_, gotTools := inlineMCPToolDeclarations(messages, nil, current)
	if len(gotTools) != 1 || gotTools[0].Name != "mcp_sample_fetch" {
		t.Fatalf("disabled tool must not be re-advertised: %#v", gotTools)
	}
}

// The top-level list is the surface the request was built from, so it outranks
// the current declaration set on duplicate names.
func TestInlineMCPToolDeclarationsTopLevelOutranksCurrentDeclarations(t *testing.T) {
	messages := []message.Message{
		message.NewSystemToolsMessage([]message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "older"}}),
		{Role: message.RoleUser, Content: "hello"},
	}
	tools := []message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "top-level"}}
	current := []message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "current"}}

	_, gotTools := inlineMCPToolDeclarations(messages, tools, current)
	if len(gotTools) != 1 || gotTools[0].Description != "top-level" {
		t.Fatalf("top-level definition must win on duplicate names: %#v", gotTools)
	}
}

func TestInlineMCPToolDeclarationsKeepsMixedPayloadMessage(t *testing.T) {
	defs := []message.ToolDefinition{{Name: "mcp_sample_lookup", Description: "dynamic"}}
	mixedCases := []struct {
		name string
		msg  message.Message
	}{
		{"content", message.Message{Role: message.RoleSystem, Content: "note", MCPTools: defs}},
		{"parts", message.Message{Role: message.RoleSystem, Parts: []message.ContentPart{{Type: "text", Text: "hi"}}, MCPTools: defs}},
		{"tool calls", message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "1", Name: "read"}}, MCPTools: defs}},
		{"responses output", message.Message{Role: message.RoleAssistant, ResponsesOutput: []message.ResponsesOutputItem{{Type: "reasoning", Summary: []message.ResponsesReasoningSummary{{Type: "summary_text", Text: "hi"}}}}, MCPTools: defs}},
	}
	for _, tc := range mixedCases {
		t.Run(tc.name, func(t *testing.T) {
			messages := []message.Message{
				{Role: message.RoleUser, Content: "first"},
				tc.msg,
				message.NewSystemToolsMessage(defs),
			}
			gotMessages, gotTools := inlineMCPToolDeclarations(messages, nil, defs)
			if len(gotMessages) != 2 {
				t.Fatalf("mixed message not retained / pure declaration not dropped: %#v", gotMessages)
			}
			if len(gotMessages[1].MCPTools) != 0 {
				t.Fatalf("MCPTools not stripped: %#v", gotMessages[1])
			}
			if len(gotTools) != 1 || gotTools[0].Name != "mcp_sample_lookup" {
				t.Fatalf("defs not merged: %#v", gotTools)
			}
			if len(messages[1].MCPTools) != 1 {
				t.Fatalf("inputs mutated: %#v", messages)
			}
		})
	}
}

func TestInlineMCPToolDeclarationsWithoutDeclarationsReturnsInputs(t *testing.T) {
	messages := []message.Message{{Role: message.RoleUser, Content: "hello"}}
	tools := []message.ToolDefinition{{Name: "read"}}

	gotMessages, gotTools := inlineMCPToolDeclarations(messages, tools, nil)
	if len(gotMessages) != 1 || len(gotTools) != 1 {
		t.Fatalf("unexpected rewrite: messages=%#v tools=%#v", gotMessages, gotTools)
	}
	if &gotMessages[0] != &messages[0] || &gotTools[0] != &tools[0] {
		t.Fatal("a request without declarations must be returned unchanged")
	}
}
