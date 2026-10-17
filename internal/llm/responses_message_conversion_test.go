package llm

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
	"github.com/keakon/chord/internal/tools"
)

func TestConvertMessagesToResponses(t *testing.T) {
	tests := []struct {
		name         string
		systemPrompt string
		messages     []message.Message
		wantLen      int
		wantFirst    string // expected type of first item
	}{
		{
			name:         "system prompt only",
			systemPrompt: "You are a helpful assistant",
			messages:     []message.Message{},
			wantLen:      1,
			wantFirst:    "message",
		},
		{
			name:         "user message",
			systemPrompt: "You are helpful",
			messages: []message.Message{
				{Role: "user", Content: "Hello"},
			},
			wantLen:   2,
			wantFirst: "message",
		},
		{
			name:         "assistant with tool call",
			systemPrompt: "You are helpful",
			messages: []message.Message{
				{Role: "user", Content: "Read file"},
				{
					Role:    "assistant",
					Content: "I'll read the file",
					ToolCalls: []message.ToolCall{
						{ID: "call_1", Name: "Read", Args: json.RawMessage(`{"path":"test.go"}`)},
					},
				},
			},
			wantLen: 4, // system + user + assistant text + function_call
		},
		{
			name:         "tool result",
			systemPrompt: "You are helpful",
			messages: []message.Message{
				{Role: "tool", Content: "file contents", ToolCallID: "call_1"},
			},
			wantLen: 2, // system + function_call_output
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertMessagesToResponses(tt.systemPrompt, tt.messages)
			if len(items) != tt.wantLen {
				t.Errorf("convertMessagesToResponses() returned %d items, want %d", len(items), tt.wantLen)
			}
			if tt.wantFirst != "" && len(items) > 0 {
				if items[0].Type != tt.wantFirst {
					t.Errorf("first item type = %q, want %q", items[0].Type, tt.wantFirst)
				}
			}
		})
	}
}

func TestConvertMessagesToResponses_WithImageParts(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "what is in this image?"},
			{Type: "image", MimeType: "image/png", Data: tinyPNG},
		},
	}})
	if len(items) != 1 {
		t.Fatalf("convertMessagesToResponses() returned %d items, want 1", len(items))
	}
	if items[0].Type != "message" || items[0].Role != "user" {
		t.Fatalf("first item = %#v, want user message", items[0])
	}
	blocks, ok := items[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("content type = %T, want []responsesContentBlock", items[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Type != "input_text" || blocks[0].Text != "what is in this image?" {
		t.Fatalf("text block = %#v", blocks[0])
	}
	if blocks[1].Type != "input_image" {
		t.Fatalf("image block type = %q, want input_image", blocks[1].Type)
	}
	if !strings.HasPrefix(blocks[1].ImageURL, "data:image/png;base64,") {
		t.Fatalf("image block URL = %q, want data URL prefix", blocks[1].ImageURL)
	}
	if blocks[1].Detail != "auto" {
		t.Fatalf("image block detail = %q, want auto", blocks[1].Detail)
	}
}

// TestConvertMessagesToResponses_MergesAdjacentTextParts verifies that a user
// message carrying multiple pure-text parts collapses to the canonical single
// input_text block instead of emitting one block per part. This matches the
// Responses API shape (and avoids third-party gateways that 400 on multi-block
// text content).
func TestConvertMessagesToResponses_MergesAdjacentTextParts(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Is directory a git repo: yes\n\nGit branch: main"},
			{Type: "text", Text: "fix the failing test"},
			{Type: "text", Text: "verify with go test"},
		},
	}})
	if len(items) != 1 {
		t.Fatalf("convertMessagesToResponses() returned %d items, want 1", len(items))
	}
	blocks, ok := items[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("content type = %T, want []responsesContentBlock", items[0].Content)
	}
	if len(blocks) != 1 {
		t.Fatalf("content blocks = %d, want 1 (text parts merged)", len(blocks))
	}
	if blocks[0].Type != "input_text" {
		t.Fatalf("block type = %q, want input_text", blocks[0].Type)
	}
	want := "Is directory a git repo: yes\n\nGit branch: main\nfix the failing test\nverify with go test"
	if blocks[0].Text != want {
		t.Fatalf("merged text = %q, want %q", blocks[0].Text, want)
	}
}

// TestConvertMessagesToResponses_MergesTextAcrossNonTextBlocks verifies that
// pure-text parts around an image each fold into a single input_text block,
// while the image stays its own block.
func TestConvertMessagesToResponses_MergesTextAcrossNonTextBlocks(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "before"},
			{Type: "text", Text: "and after"},
			{Type: "image", MimeType: "image/png", Data: tinyPNG},
			{Type: "text", Text: "see this chart"},
			{Type: "text", Text: "then fix"},
		},
	}})
	blocks, ok := items[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("content type = %T, want []responsesContentBlock", items[0].Content)
	}
	if len(blocks) != 3 {
		t.Fatalf("content blocks = %d, want 3 (merged text + image + merged text)", len(blocks))
	}
	if blocks[0].Type != "input_text" || blocks[0].Text != "before\nand after" {
		t.Fatalf("leading text block = %#v", blocks[0])
	}
	if blocks[1].Type != "input_image" || blocks[1].ImageURL != "data:image/png;base64,"+tinyPNGBase64 {
		t.Fatalf("image block = %#v", blocks[1])
	}
	if blocks[2].Type != "input_text" || blocks[2].Text != "see this chart\nthen fix" {
		t.Fatalf("trailing text block = %#v", blocks[2])
	}
}

// TestConvertMessagesToResponses_MergesWithoutDoubleNewline verifies a part
// that already ends in a newline does not get an extra one inserted.
func TestConvertMessagesToResponses_MergesWithoutDoubleNewline(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "header\n"},
			{Type: "text", Text: "body"},
		},
	}})
	blocks, _ := items[0].Content.([]responsesContentBlock)
	if len(blocks) != 1 || blocks[0].Text != "header\nbody" {
		t.Fatalf("merged text = %#v, want %q without extra newline", blocks, "header\nbody")
	}
}

// TestConvertMessagesToResponses_BlankTextPartSkipped verifies an empty user
// text part produces no block on its own and does not force a stray
// input_text for a text-only request.
func TestConvertMessagesToResponses_BlankTextPartSkipped(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "hello"},
			{Type: "text", Text: ""},
		},
	}})
	blocks, ok := items[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("content type = %T, want []responsesContentBlock", items[0].Content)
	}
	if len(blocks) != 1 || blocks[0].Type != "input_text" || blocks[0].Text != "hello" {
		t.Fatalf("content blocks = %#v, want only the non-blank text", blocks)
	}
}

func TestConvertMessagesToResponsesMarksInterruptedAssistant(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{Role: "assistant", Content: "partial", StopReason: "interrupted"}})
	if len(items) != 1 || items[0].Type != "message" || items[0].Role != "assistant" {
		t.Fatalf("convertMessagesToResponses() = %#v", items)
	}
	blocks, ok := items[0].Content.([]responsesContentBlock)
	if !ok || len(blocks) != 1 {
		t.Fatalf("convertMessagesToResponses() = %#v", items)
	}
	text := blocks[0].Text
	if !strings.Contains(text, "partial") || !strings.Contains(text, "interrupted before completion") {
		t.Fatalf("interrupted assistant content = %q", text)
	}
}

func TestConvertMessagesToResponses_DoesNotReplayReasoningContent(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{
		{Role: "user", Content: "do something"},
		{
			Role:             "assistant",
			ReasoningContent: "I will call a tool now.",
			Content:          "Calling tool.",
			Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIResponses},
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)},
			},
		},
	})

	// Expect: user message, assistant visible output text, function_call item.
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[1].Type != "message" || items[1].Role != "assistant" {
		t.Fatalf("items[1]=%#v", items[1])
	}
	blocks, ok := items[1].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("items[1].Content type=%T", items[1].Content)
	}
	if len(blocks) != 1 || blocks[0].Type != "output_text" || blocks[0].Text != "Calling tool." {
		t.Fatalf("unexpected assistant replay content: %#v", items[1].Content)
	}
	if items[2].Type != "function_call" || items[2].CallID != "c1" || items[2].Name != "Shell" {
		t.Fatalf("unexpected function_call: %#v", items[2])
	}
}

func TestConvertMessagesToResponses_KeepsToolOutputWhenNativeResponsesCallNameMissing(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{
		{
			Role: "assistant",
			ResponsesOutput: []message.ResponsesOutputItem{{
				Type:      "function_call",
				CallID:    "call_1",
				Name:      "",
				Arguments: `{"path":"sample.txt"}`,
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call_1",
			Content:    "IMPORTANT TOOL RESULT",
		},
	})

	if len(items) != 1 {
		t.Fatalf("convertMessagesToResponses() returned %d items, want 1", len(items))
	}
	if items[0].Type != "function_call_output" || items[0].CallID != "call_1" || items[0].Output != "IMPORTANT TOOL RESULT" {
		t.Fatalf("items[0] = %#v, want orphan tool result preserved", items[0])
	}
}

func TestConvertMessagesToResponses_SkipsReasoningOnlyAssistant(t *testing.T) {
	// A reasoning-only assistant turn (no text, no tool calls) is dropped.
	// Responses no longer replays visible reasoning text as assistant output,
	// and the modelcompat normalize pass also clears it on the integrated path.
	tests := []struct {
		name       string
		provenance *message.MessageProvenance
	}{
		{name: "with openai responses provenance", provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIResponses}},
		{name: "without provenance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertMessagesToResponses("", []message.Message{
				{Role: "user", Content: "before"},
				{Role: "assistant", ReasoningContent: "hidden", Provenance: tt.provenance},
				{Role: "user", Content: "after"},
			})
			if len(items) != 2 {
				t.Fatalf("convertMessagesToResponses() len = %d, want 2: %#v", len(items), items)
			}
			for _, item := range items {
				if item.Type == "message" && item.Role == "assistant" {
					t.Fatalf("reasoning-only assistant was not skipped: %#v", items)
				}
			}
		})
	}
}

func TestConvertMessagesToResponses_DoesNotReplayReasoningForToolCallOnlyAssistant(t *testing.T) {
	tests := []struct {
		name       string
		provenance *message.MessageProvenance
	}{
		{name: "with openai responses provenance", provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIResponses}},
		{name: "without provenance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertMessagesToResponses("", []message.Message{{
				Role:             "assistant",
				ReasoningContent: "hidden reasoning",
				Provenance:       tt.provenance,
				ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
			}})
			if len(items) != 1 || items[0].Type != "function_call" || items[0].CallID != "c1" || items[0].Name != "Shell" {
				t.Fatalf("convertMessagesToResponses() = %#v, want only the function_call item", items)
			}
		})
	}
}

func TestConvertToolsToResponses(t *testing.T) {
	empty := convertToolsToResponses(nil)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("convertToolsToResponses(nil) = %#v, want non-nil empty slice", empty)
	}

	tools := []message.ToolDefinition{
		{
			Name:        "Read",
			Description: "Read a file",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
	}

	items := convertToolsToResponses(tools)
	if len(items) != 1 {
		t.Fatalf("convertToolsToResponses() returned %d items, want 1", len(items))
	}
	if items[0].Type != "function" {
		t.Errorf("tool type = %q, want %q", items[0].Type, "function")
	}
	if items[0].Name != "Read" {
		t.Errorf("tool name = %q, want %q", items[0].Name, "Read")
	}
	// Tool descriptions are the single source for per-tool usage rules and are
	// also the only documentation an MCP tool carries (this converter renders
	// the additional_tools item too), so the wire must keep them.
	if items[0].Description != "Read a file" {
		t.Errorf("tool description = %q, want %q", items[0].Description, "Read a file")
	}
}

// TestResponsesFunctionToolMarshalCarriesStrictFalse locks the wire shape of
// the Responses function conversions: an explicit "strict":false so hosts do
// not rewrite the schema into a strict (every property required) form, and a
// parameters schema that stays exactly as declared so optional fields remain
// omittable.
func TestResponsesFunctionToolMarshalCarriesStrictFalse(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message":        map[string]any{"type": "string"},
			"target_task_id": map[string]any{"type": "string"},
			"message_type":   map[string]any{"type": "string", "enum": []string{"response"}},
		},
		"required":             []string{"message", "target_task_id"},
		"additionalProperties": false,
	}
	defs := []message.ToolDefinition{{Name: "notify", Description: "Send a message", InputSchema: schema}}
	for _, tc := range []struct {
		name    string
		convert func([]message.ToolDefinition) []responsesTool
	}{
		{name: "convertToolsToResponses", convert: convertToolsToResponses},
		{
			name: "convertToolsToResponsesForTarget",
			convert: func(defs []message.ToolDefinition) []responsesTool {
				provider := responsesProviderFor(t, config.ProviderConfig{})
				return convertToolsToResponsesForTarget(provider, "deepseek-v4-flash", defs)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := tc.convert(defs)
			if len(tools) != 1 || tools[0].Type != "function" {
				t.Fatalf("tools = %#v, want one function tool", tools)
			}
			raw, err := json.Marshal(tools[0])
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Contains(raw, []byte(`"strict":false`)) {
				t.Fatalf("function tool JSON = %s, want explicit \"strict\":false", raw)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			strict, ok := decoded["strict"].(bool)
			if !ok || strict {
				t.Fatalf("strict = %#v (ok=%v), want explicit false (pointer false must serialize)", decoded["strict"], ok)
			}
			params, _ := decoded["parameters"].(map[string]any)
			required, err := json.Marshal(params["required"])
			if err != nil {
				t.Fatalf("marshal required: %v", err)
			}
			if string(required) != `["message","target_task_id"]` {
				t.Fatalf("parameters.required = %s, want the declared subset unchanged (optional fields must stay optional)", required)
			}
		})
	}

	t.Run("custom_tool_keeps_no_strict_or_parameters", func(t *testing.T) {
		provider := responsesProviderFor(t, config.ProviderConfig{})
		tools := convertToolsToResponsesForTarget(provider, "gpt-5.5", sampleToolDefs())
		if len(tools) != 2 || tools[0].Type != "custom" {
			t.Fatalf("tools = %#v, want a custom apply_patch plus a function tool", tools)
		}
		raw, err := json.Marshal(tools[0])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if bytes.Contains(raw, []byte("strict")) || bytes.Contains(raw, []byte(`"parameters"`)) {
			t.Fatalf("custom tool JSON = %s, want no strict or parameters", raw)
		}
	})
}

// TestConvertMessagesToResponses_ArgumentsAsString ensures function_call items serialize
// with "arguments" as a JSON string (per Responses API), not an object.
func TestConvertMessagesToResponses_ArgumentsAsString(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{
		{Role: "user", Content: "run ls"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"ls -la"}`)},
			},
		},
	})
	var found bool
	for _, it := range items {
		if it.Type != "function_call" {
			continue
		}
		found = true
		// Arguments must be string so that when marshaled to JSON we get "arguments": "{\"command\":\"ls -la\"}".
		if it.Arguments == "" {
			t.Error("function_call item must have non-empty Arguments")
		}
		if it.Arguments != `{"command":"ls -la"}` {
			t.Errorf("Arguments = %q, want JSON string content", it.Arguments)
		}
		break
	}
	if !found {
		t.Fatal("no function_call item in converted input")
	}
}

func TestConvertMessagesToResponses_EmptyInput(t *testing.T) {
	// Empty system prompt and empty messages should return empty non-nil slice.
	items := convertMessagesToResponses("", []message.Message{})
	if items == nil {
		t.Error("convertMessagesToResponses() returned nil, want empty non-nil slice")
	}
	if len(items) != 0 {
		t.Errorf("convertMessagesToResponses() returned %d items, want 0", len(items))
	}
}

func TestConvertMessagesToResponses_NonNilSlice(t *testing.T) {
	// Verify that the function always returns a non-nil slice.
	tests := []struct {
		name         string
		systemPrompt string
		messages     []message.Message
	}{
		{"empty", "", []message.Message{}},
		{"system only", "You are helpful", []message.Message{}},
		{"messages only", "", []message.Message{{Role: "user", Content: "Hi"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertMessagesToResponses(tt.systemPrompt, tt.messages)
			if items == nil {
				t.Error("convertMessagesToResponses() returned nil slice, want non-nil")
			}
		})
	}
}

func TestConvertMessagesToResponses_WhitespaceOnlyToolOutputPreserved(t *testing.T) {
	items := convertMessagesToResponses("system", []message.Message{
		{Role: "user", Content: "run something"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"input.go","limit":240,"offset":358}`)},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: "   359\t\n"},
	})

	for _, item := range items {
		if item.Type != "function_call_output" {
			continue
		}
		if item.Output != "   359\t\n" {
			t.Fatalf("output = %q, want exact whitespace-only payload preserved", item.Output)
		}
		return
	}
	t.Fatal("no function_call_output item found")
}

func TestConvertMessagesToResponses_ToolOutputWithImageParts(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{
		Role:       "tool",
		ToolCallID: "c1",
		Content:    "Loaded image",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Loaded image"},
			{Type: "image", MimeType: "image/png", Data: tinyPNG},
		},
	}})

	if len(items) != 1 || items[0].Type != "function_call_output" {
		t.Fatalf("items = %#v, want function_call_output", items)
	}
	blocks, ok := items[0].Output.([]responsesContentBlock)
	if !ok {
		t.Fatalf("output type = %T, want []responsesContentBlock", items[0].Output)
	}
	if len(blocks) != 2 || blocks[0].Type != "input_text" || blocks[0].Text != "Loaded image" {
		t.Fatalf("text output block = %#v", blocks)
	}
	if blocks[1].Type != "input_image" || blocks[1].ImageURL != "data:image/png;base64,"+tinyPNGBase64 || blocks[1].Detail != "auto" {
		t.Fatalf("image output block = %#v", blocks[1])
	}
}

// TestConvertMessagesToResponses_EmptyToolOutput ensures that function_call_output items
// with empty content still include the "output" field in JSON (not omitted by omitempty).
// This prevents API error 400: Missing required parameter: 'input[N].output'.
func TestConvertMessagesToResponses_EmptyToolOutput(t *testing.T) {
	items := convertMessagesToResponses("system", []message.Message{
		{Role: "user", Content: "run something"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"git diff --staged"}`)},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: ""}, // empty tool output
	})

	// Find the function_call_output item and verify it marshals with "output" field.
	for _, item := range items {
		if item.Type != "function_call_output" {
			continue
		}
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal function_call_output: %v", err)
		}
		// The JSON must contain "output" key even when the value is empty.
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal to map: %v", err)
		}
		if _, ok := raw["output"]; !ok {
			t.Fatalf("function_call_output JSON missing 'output' field: %s\n"+
				"This causes API error 400: Missing required parameter: 'input[N].output'", string(data))
		}
		return
	}
	t.Fatal("no function_call_output item found")
}

func TestConvertToolsToResponsesStableAcrossRegistryConstructionOrder(t *testing.T) {
	regA := tools.NewRegistry()
	regA.Register(tools.GlobTool{})
	regA.Register(tools.ReadTool{})

	regB := tools.NewRegistry()
	regB.Register(tools.ReadTool{})
	regB.Register(tools.GlobTool{})

	defsA := regA.ListDefinitions()
	defsB := regB.ListDefinitions()
	// Registry.ListTools() sorts by name, so registration order doesn't affect output
	if !reflect.DeepEqual(defsA, defsB) {
		t.Fatalf("registry definitions should be stable across construction order:\nA=%#v\nB=%#v", defsA, defsB)
	}

	gotA := convertToolsToResponses(defsA)
	gotB := convertToolsToResponses(defsB)
	if !reflect.DeepEqual(gotA, gotB) {
		t.Fatalf("convertToolsToResponses should be stable across registry construction order:\nA=%#v\nB=%#v", gotA, gotB)
	}
	if len(gotA) != len(defsA) || gotA[0].Name != defsA[0].Name || gotA[1].Name != defsA[1].Name {
		t.Fatalf("unexpected tool order: defs=%#v tools=%#v", defsA, gotA)
	}
}

// TestConvertMessagesToResponses_InterleavesToolCallsAndOutputs verifies that
// parallel tool calls are serialized in call-then-output order (each call
// immediately followed by its result) rather than grouped with all calls first.
// This is the canonical Responses input shape and is required by
// Chat-Completions-converting gateways, which reject grouped calls with
// "assistant message with 'tool_calls' must be followed by tool messages".
func TestConvertMessagesToResponses_InterleavesToolCallsAndOutputs(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{
		{Role: "user", Content: "do it"},
		{Role: "assistant", Content: "calling", ToolCalls: []message.ToolCall{
			{ID: "call_AAA", Name: "Shell", Args: json.RawMessage(`{"cmd":"a"}`)},
			{ID: "call_BBB", Name: "Grep", Args: json.RawMessage(`{"p":"b"}`)},
			{ID: "call_CCC", Name: "Read", Args: json.RawMessage(`{"f":"c"}`)},
		}},
		{Role: "tool", ToolCallID: "call_AAA", Content: "out-a"},
		{Role: "tool", ToolCallID: "call_BBB", Content: "out-b"},
		{Role: "tool", ToolCallID: "call_CCC", Content: "out-c"},
	})

	// Expect: user, assistant, then call_AAA→out, call_BBB→out, call_CCC→out.
	if len(items) != 8 {
		t.Fatalf("got %d items, want 8: %#v", len(items), items)
	}
	var kinds []string
	for _, it := range items[2:] {
		kinds = append(kinds, it.Type+"("+it.CallID+")")
	}
	want := []string{
		"function_call(call_AAA)", "function_call_output(call_AAA)",
		"function_call(call_BBB)", "function_call_output(call_BBB)",
		"function_call(call_CCC)", "function_call_output(call_CCC)",
	}
	if strings.Join(kinds[:6], ",") != strings.Join(want, ",") {
		t.Fatalf("call/output order = %v, want %v", kinds[:6], want)
	}
	for _, it := range items[2:] {
		if it.Type == "function_call_output" {
			wantOuts := map[string]string{"call_AAA": "out-a", "call_BBB": "out-b", "call_CCC": "out-c"}[it.CallID]
			out, _ := it.Output.(string)
			if out != wantOuts {
				t.Fatalf("output for %s = %q, want %q", it.CallID, out, wantOuts)
			}
		}
	}
	// Verify user+assistant items precede the interleaved calls.
	if items[0].Type != "message" || items[1].Type != "message" {
		t.Fatalf("first two items = %v, want user + assistant messages", items[:2])
	}
}

// TestConvertMessagesToResponses_InterleaveLeavesOrphanResults verifies that a
// tool result not matching any surviving call (or appearing after a gap) is
// still emitted as a plain function_call_output, keeping history self-consistent.
func TestConvertMessagesToResponses_InterleaveLeavesOrphanResults(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{
		{Role: "assistant", Content: "calling", ToolCalls: []message.ToolCall{
			{ID: "call_A", Name: "Shell", Args: json.RawMessage(`{}`)},
		}},
		{Role: "tool", ToolCallID: "call_X", Content: "orphan"},
		{Role: "assistant", Content: "again", ToolCalls: []message.ToolCall{
			{ID: "call_B", Name: "Grep", Args: json.RawMessage(`{}`)},
		}},
		{Role: "tool", ToolCallID: "call_B", Content: "out-b"},
	})
	// Expect: assistant(A), function_call(A), function_call_output(X),
	// assistant(again), function_call(B), function_call_output(B).
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Type+"("+it.CallID+")")
	}
	want := []string{
		"message()", "function_call(call_A)", "function_call_output(call_X)",
		"message()", "function_call(call_B)", "function_call_output(call_B)",
	}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("item order = %v, want %v", kinds, want)
	}
}

// TestConvertMessagesToResponses_InterleavesNativeResponsesOutput verifies that
// parallel tool calls carried as native ResponsesOutput items (the stateless
// replay path) are still interleaved with their tool results, instead of
// emitting all function_calls grouped before all outputs. The tool results are
// adjacent tool messages consumed per call.
func TestConvertMessagesToResponses_InterleavesNativeResponsesOutput(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{
		{Role: "user", Content: "do it"},
		{
			Role: "assistant",
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "message", Role: "assistant", Content: []message.ResponsesOutputContent{{Type: "output_text", Text: "calling"}}},
				{Type: "function_call", CallID: "call_1", Name: "Shell", Arguments: `{"cmd":"a"}`},
				{Type: "function_call", CallID: "call_2", Name: "Grep", Arguments: `{"p":"b"}`},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "out-1"},
		{Role: "tool", ToolCallID: "call_2", Content: "out-2"},
	})
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Type+"("+it.CallID+")")
	}
	// Expect: user message, assistant message, call_1→out_1, call_2→out_2.
	want := []string{
		"message()", "message()",
		"function_call(call_1)", "function_call_output(call_1)",
		"function_call(call_2)", "function_call_output(call_2)",
	}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("item order = %v, want %v", kinds, want)
	}
	for _, it := range items {
		if it.Type == "function_call_output" {
			wantOuts := map[string]string{"call_1": "out-1", "call_2": "out-2"}[it.CallID]
			out, _ := it.Output.(string)
			if out != wantOuts {
				t.Fatalf("output for %s = %q, want %q", it.CallID, out, wantOuts)
			}
		}
	}
}

// History tool-call arguments that arrive malformed or empty — an imported
// call whose source rollout recorded corrupt arguments, or a nil-args call —
// are replaced with the malformed sentinel instead of reaching the request
// raw, matching the guard the other wire converters apply.
func TestConvertMessagesToResponses_SanitizesMalformedToolArgs(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "call_1", Name: "shell", Args: json.RawMessage(`{invalid`)},
			{ID: "call_2", Name: "shell"},
		}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, ToolCallID: "call_2", Content: "ok", ToolStatus: message.ToolStatusSuccess},
	}
	items := convertMessagesToResponsesWithItemIDs("", msgs, false, false, false)
	var got []string
	for _, item := range items {
		if item.Type == "function_call" {
			got = append(got, item.Arguments)
		}
	}
	want := []string{MalformedArgsSentinel, MalformedArgsSentinel}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("function_call arguments = %q, want %q", got, want)
	}
}

// The continuation path (a previous Response's tool calls replayed as input
// items) applies the same guard.
func TestResponsesToolCallsToInputItems_SanitizesMalformedArgs(t *testing.T) {
	items := responsesToolCallsToInputItems([]message.ToolCall{
		{ID: "call_1", Name: "shell", Args: json.RawMessage(`not json`)},
	}, false)
	if len(items) != 1 || items[0].Arguments != MalformedArgsSentinel {
		t.Fatalf("arguments = %q, want the sentinel", items[0].Arguments)
	}
}

// A stored Responses output item whose own arguments are malformed — a
// restored session, or an accumulated payload that recorded a truncated
// arguments string — is guarded on replay the same way ToolCalls are. An item
// with no arguments keeps the required-field fallback instead.
func TestResponsesOutputToInputItems_SanitizesMalformedArgs(t *testing.T) {
	items := responsesOutputToInputItems([]responsesOutputEntry{
		{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "shell", Arguments: `{"cmd":`},
		{Type: "function_call", ID: "fc_2", CallID: "call_2", Name: "shell"},
	}, false)
	if len(items) != 2 {
		t.Fatalf("items = %+v, want both calls", items)
	}
	if items[0].Arguments != MalformedArgsSentinel {
		t.Fatalf("malformed arguments = %q, want the sentinel", items[0].Arguments)
	}
	if items[1].Arguments != "{}" {
		t.Fatalf("empty arguments = %q, want the fallback object", items[1].Arguments)
	}
}

// A completed function_call collected into ResponsesOutput carries the same
// guard, so a restored session does not replay the corrupt string raw.
func TestCollectResponsesOutput_SanitizesMalformedArgs(t *testing.T) {
	resp := &message.Response{}
	collectResponsesOutput(resp, []responsesOutputEntry{
		{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "shell", Status: "completed", Arguments: `not json`},
	})
	if len(resp.ResponsesOutput) != 1 || resp.ResponsesOutput[0].Arguments != MalformedArgsSentinel {
		t.Fatalf("ResponsesOutput = %+v, want the sentinel", resp.ResponsesOutput)
	}
}
