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
	"github.com/keakon/chord/internal/toolname"
)

func TestIsApplyPatchModel(t *testing.T) {
	tests := []struct {
		modelID string
		want    bool
	}{
		{"gpt-5.1", true},
		{"gpt-5.5", true},
		{"gpt-5.5-pro", true},
		{"gpt-5.11", true},
		{"gpt-5.1-oss", true},
		{"gpt-5.5-oss-codex", true},
		{"gpt-5.3-codex", true},
		// Dotted subfamily names are gpt-5.x: the family includes gpt-5.1-mini,
		// gpt-5.1-codex and similar; undotted names (gpt-5, gpt-5-mini,
		// gpt-5-nano, gpt-5-codex) are part of the same family.
		{"gpt-5.1-mini", true},
		{"gpt-5.1-codex", true},
		{"gpt-5.2-flash", true},
		// The whole gpt-5 family is patch-native: the bare base name and
		// undotted subfamily names match too. Later majors inherit the same
		// signal (apply_patch is first-party Codex training data), so gpt-6
		// and beyond default to the patch surface without a client update.
		{"gpt-5", true},
		{"gpt-5-mini", true},
		{"gpt-5-nano", true},
		{"gpt-5-codex", true},
		{"gpt-5-chat", true},
		{"gpt-6", true},
		{"gpt-6.1", true},
		{"gpt-6-mini", true},
		{"gpt-7", true},
		{"gpt-50", true},
		{"gpt-5x", false},
		{"gpt-oss-120b", false},
		{"gpt-4o", false},
		{"gpt-4.1", false},
		{"gpt-3.5-turbo", false},
		{"o4-mini", false},
		{"o3", false},
		{"codex-auto-review", true},
		{"daybreak-codex", false}, // *-codex suffix alone is not a patch signal
		{"gpt-daybreak-blue", false},
		{"foo-codex-bar", false}, // *-codex suffix alone is not a patch signal
		{"claude-opus-4", false},
		{"deepseek-v4-flash", false},
		{"gpt", false},
		{"gptx", false},
		{"GPT-5.5", true},
	}
	for _, tt := range tests {
		if got := IsApplyPatchModel(tt.modelID); got != tt.want {
			t.Errorf("IsApplyPatchModel(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func sampleToolDefs() []message.ToolDefinition {
	return []message.ToolDefinition{
		{
			Name:        toolname.ApplyPatch,
			Description: "Apply a Codex-compatible patch",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"patch": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name:        "Read",
			Description: "Read a file",
			InputSchema: map[string]any{"type": "object"},
		},
	}
}

func responsesProviderFor(t *testing.T, cfg config.ProviderConfig) *ProviderConfig {
	t.Helper()
	if cfg.Type == "" {
		cfg.Type = config.ProviderTypeResponses
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.openai.com/v1/responses"
	}
	return NewProviderConfig("sample", cfg, []string{"test-key"})
}

func TestConvertToolsToResponsesForTarget(t *testing.T) {
	t.Run("gpt_5x_freeform_custom", func(t *testing.T) {
		provider := responsesProviderFor(t, config.ProviderConfig{})
		tools := convertToolsToResponsesForTarget(provider, "gpt-5.5", sampleToolDefs())
		if len(tools) != 2 {
			t.Fatalf("got %d tools, want 2", len(tools))
		}
		ap := tools[0]
		if ap.Type != "custom" {
			t.Errorf("apply_patch type = %q, want custom", ap.Type)
		}
		if ap.Name != toolname.ApplyPatch {
			t.Errorf("apply_patch name = %q", ap.Name)
		}
		if ap.Description != "The `apply_patch` tool can be used to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON." {
			t.Errorf("custom tool description = %q, want the exact Codex freeform description", ap.Description)
		}
		if ap.Parameters != nil {
			t.Errorf("custom tool must not send parameters, got %#v", ap.Parameters)
		}
		if ap.Format == nil {
			t.Fatal("custom tool must carry a grammar format")
		}
		if ap.Format.Type != "grammar" || ap.Format.Syntax != "lark" || !strings.Contains(ap.Format.Definition, "*** Begin Patch") {
			t.Errorf("format = %+v, want grammar/lark skeleton", ap.Format)
		}
		if tools[1].Type != "function" {
			t.Errorf("non-apply_patch tool type = %q, want function", tools[1].Type)
		}
	})

	t.Run("function_fallback_non_gpt5", func(t *testing.T) {
		provider := responsesProviderFor(t, config.ProviderConfig{})
		// The whitelist matches by name pattern: gpt-5-and-later family names
		// and codex-auto-review stay freeform (even with an -oss suffix);
		// everything else (o-series, gpt-4o, gpt-oss-*, *-codex names like
		// daybreak-codex, non-OpenAI) is function.
		for _, model := range []string{"o4-mini", "gpt-4o", "gpt-4.1", "gpt-oss-120b", "daybreak-codex", "claude-opus-4", "deepseek-v4-flash", "gpt-5x"} {
			tools := convertToolsToResponsesForTarget(provider, model, sampleToolDefs())
			if tools[0].Type != "function" {
				t.Errorf("model %q apply_patch type = %q, want function", model, tools[0].Type)
			}
			if tools[0].Format != nil {
				t.Errorf("model %q apply_patch must not carry format", model)
			}
			if tools[0].Parameters == nil {
				t.Errorf("model %q apply_patch must send parameters as function tool", model)
			}
		}
	})

	t.Run("azure_follows_model_whitelist", func(t *testing.T) {
		// A gpt-5 family model on an Azure OpenAI Responses host is patch-native,
		// so it emits the freeform shape at the wire by default. Hosts that
		// reject custom tools use the escape hatch compat.apply_patch.freeform: false.
		provider := responsesProviderFor(t, config.ProviderConfig{
			APIURL: "https://example.openai.azure.com/openai/v1/responses?api-version=preview",
		})
		tools := convertToolsToResponsesForTarget(provider, "gpt-5.5", sampleToolDefs())
		if tools[0].Type != "custom" {
			t.Errorf("azure gpt-5 family apply_patch type = %q, want custom", tools[0].Type)
		}
	})

	t.Run("non_responses_provider_never_freeform", func(t *testing.T) {
		provider := responsesProviderFor(t, config.ProviderConfig{Type: config.ProviderTypeChatCompletions})
		tools := convertToolsToResponsesForTarget(provider, "gpt-5.5", sampleToolDefs())
		if tools[0].Type != "function" {
			t.Errorf("non-Responses apply_patch type = %q, want function", tools[0].Type)
		}
	})

	t.Run("compat_override_wins", func(t *testing.T) {
		provider := responsesProviderFor(t, config.ProviderConfig{
			Compat: &config.ProviderCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Freeform: new(false)}},
		})
		tools := convertToolsToResponsesForTarget(provider, "gpt-5.5", sampleToolDefs())
		if tools[0].Type != "function" {
			t.Errorf("compat freeform:false apply_patch type = %q, want function", tools[0].Type)
		}
	})

	t.Run("compat_override_enables_non_gpt5", func(t *testing.T) {
		provider := responsesProviderFor(t, config.ProviderConfig{
			Compat: &config.ProviderCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Freeform: new(true)}},
		})
		tools := convertToolsToResponsesForTarget(provider, "deepseek-v4-flash", sampleToolDefs())
		if tools[0].Type != "custom" {
			t.Errorf("compat freeform:true apply_patch type = %q, want custom", tools[0].Type)
		}
	})

	t.Run("model_level_override_beats_provider", func(t *testing.T) {
		provider := responsesProviderFor(t, config.ProviderConfig{
			Compat: &config.ProviderCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Freeform: new(true)}},
			Models: map[string]config.ModelConfig{
				"gpt-5.5": {Compat: &config.ModelCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Freeform: new(false)}}},
			},
		})
		tools := convertToolsToResponsesForTarget(provider, "gpt-5.5", sampleToolDefs())
		if tools[0].Type != "function" {
			t.Errorf("model-level freeform:false apply_patch type = %q, want function", tools[0].Type)
		}
		// Unlisted model keeps the provider default.
		tools = convertToolsToResponsesForTarget(provider, "gpt-5.6", sampleToolDefs())
		if tools[0].Type != "custom" {
			t.Errorf("unlisted model apply_patch type = %q, want provider-level custom", tools[0].Type)
		}
	})

	t.Run("nil_provider_uses_inference", func(t *testing.T) {
		tools := convertToolsToResponsesForTarget(nil, "gpt-5.5", sampleToolDefs())
		if tools[0].Type != "custom" {
			t.Errorf("nil provider gpt-5.5 apply_patch type = %q, want custom", tools[0].Type)
		}
		tools = convertToolsToResponsesForTarget(nil, "deepseek-v4-flash", sampleToolDefs())
		if tools[0].Type != "function" {
			t.Errorf("nil provider deepseek apply_patch type = %q, want function", tools[0].Type)
		}
	})
}

func TestResponsesToolsHasCustom(t *testing.T) {
	if responsesToolsHasCustom(nil) {
		t.Error("nil tools must not contain custom")
	}
	plain := []responsesTool{{Type: "function", Name: "Read"}}
	if responsesToolsHasCustom(plain) {
		t.Error("function-only tools must not contain custom")
	}
	mixed := []responsesTool{{Type: "function", Name: "Read"}, {Type: "custom", Name: toolname.ApplyPatch}}
	if !responsesToolsHasCustom(mixed) {
		t.Error("custom tool must be detected")
	}
}

func TestCanonicalApplyPatchArgs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare_text", "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch", `{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}`},
		{"json_string_wrapped", `"*** Begin Patch"`, `{"patch":"*** Begin Patch"}`},
		{"already_canonical_object", `{"patch":"@@\n-old\n+new"}`, `{"patch":"@@\n-old\n+new"}`},
		{"empty_patch_object_passes", `{"patch":""}`, `{"patch":""}`},
		{"input_object_is_wrapped_as_text", `{"input":"*** Begin Patch"}`, `{"patch":"{\"input\":\"*** Begin Patch\"}"}`},
		{"empty_text", "", `{"patch":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(canonicalApplyPatchArgs(json.RawMessage(tt.in)))
			if got != tt.want {
				t.Errorf("canonicalApplyPatchArgs(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeResponsesOutputEntry(t *testing.T) {
	entry := normalizeResponsesOutputEntry(responsesOutputEntry{
		Type:  "custom_tool_call",
		ID:    "item_1",
		Name:  toolname.ApplyPatch,
		Input: "*** Begin Patch\n*** End Patch",
	})
	if entry.Type != "function_call" {
		t.Errorf("normalized type = %q, want function_call", entry.Type)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(entry.Arguments), &args); err != nil {
		t.Fatalf("normalized args not valid JSON: %v", err)
	}
	if args["patch"] != "*** Begin Patch\n*** End Patch" {
		t.Errorf("normalized patch = %#v, want the raw input text", args["patch"])
	}

	// Non-custom entries pass through unchanged.
	plain := responsesOutputEntry{Type: "function_call", Arguments: `{"x":1}`}
	if got := normalizeResponsesOutputEntry(plain); got.Arguments != `{"x":1}` {
		t.Errorf("function_call entry was modified: %#v", got)
	}
}

func TestApplyResponsesCompletionPayload_CustomToolCall(t *testing.T) {
	output := []responsesOutputEntry{
		{Type: "custom_tool_call", ID: "item_1", Name: toolname.ApplyPatch, Input: "*** Begin Patch\n*** End Patch"},
	}
	var resp message.Response
	truncated := false
	applyResponsesCompletionPayload(&resp, responsesCompletedPayload{
		ID:     "resp-1",
		Output: output,
		Usage:  &responsesUsagePayload{InputTokens: 10, OutputTokens: 5},
	}, &truncated)
	if resp.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q, want tool_calls for custom_tool_call output", resp.StopReason)
	}
	// ResponsesOutput replay carries the canonical function_call shape.
	if len(resp.ResponsesOutput) != 1 || resp.ResponsesOutput[0].Type != "function_call" {
		t.Errorf("ResponsesOutput = %#v, want normalized function_call", resp.ResponsesOutput)
	}
	if resp.ResponsesOutput[0].Arguments != `{"patch":"*** Begin Patch\n*** End Patch"}` {
		t.Errorf("ResponsesOutput args = %q, want canonical object", resp.ResponsesOutput[0].Arguments)
	}
	// The SSE handler recovers tool calls from completed output when the
	// streamed added/done events were missed; exercise that path directly.
	recoverResponsesToolCallsFromOutput(&resp, output, nil)
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d recovered tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != toolname.ApplyPatch {
		t.Errorf("tool name = %q", tc.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("recovered args not valid JSON: %v", err)
	}
	if args["patch"] != "*** Begin Patch\n*** End Patch" {
		t.Errorf("recovered patch = %#v, want canonical {patch} object", args["patch"])
	}
}

func TestResponsesOutputToInputItems_CustomToolCallReplaysAsFunctionCall(t *testing.T) {
	// Under a function-shape target (freeform=false) custom tool history
	// unifies to function_call with canonical {patch} arguments.
	items := responsesOutputToInputItems([]responsesOutputEntry{
		{Type: "custom_tool_call", ID: "item_1", Name: toolname.ApplyPatch, Input: "*** Begin Patch\n*** End Patch"},
		{Type: "custom_tool_call", ID: "item_2", Name: toolname.ApplyPatch, Input: "*** Begin Patch\n*** Update File: b.txt\n@@\n-x\n+y\n*** End Patch"},
	}, false)
	if len(items) != 2 {
		t.Fatalf("got %d input items, want 2", len(items))
	}
	for i, item := range items {
		if item.Type != "function_call" {
			t.Errorf("item %d type = %q, want function_call (unified replay)", i, item.Type)
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil {
			t.Fatalf("item %d args not valid JSON: %v", i, err)
		}
		if _, ok := args["patch"]; !ok {
			t.Errorf("item %d args = %#v, want canonical {patch} object", i, args)
		}
	}
}

func TestResponsesMixedHistoryReplaysAsFunctionCalls(t *testing.T) {
	// A session that switched models leaves mixed history: earlier function
	// calls and later freeform custom calls coexist in ResponsesOutput.
	// Replay must unify both to function_call with canonical arguments and
	// keep the paired tool results as function_call_output (no orphaned
	// outputs), regardless of emission form.
	items := responsesOutputToInputItems([]responsesOutputEntry{
		{Type: "function_call", ID: "item_r", CallID: "c_read", Name: "Read", Arguments: `{"path":"a.go"}`},
		{Type: "custom_tool_call", ID: "item_p", CallID: "c_patch", Name: toolname.ApplyPatch, Input: "*** Begin Patch\n*** End Patch"},
	}, false)
	if len(items) != 2 {
		t.Fatalf("got %d input items, want 2", len(items))
	}
	types := []string{items[0].Type, items[1].Type}
	for i, typ := range types {
		if typ != "function_call" {
			t.Errorf("item %d type = %q, want function_call (mixed history unified)", i, typ)
		}
	}
	if items[0].Arguments != `{"path":"a.go"}` {
		t.Errorf("item 0 args = %q, want unchanged function args", items[0].Arguments)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(items[1].Arguments), &args); err != nil {
		t.Fatalf("item 1 args not valid JSON: %v", err)
	}
	if args["patch"] != "*** Begin Patch\n*** End Patch" {
		t.Errorf("item 1 args = %#v, want canonical {patch} object", args)
	}

	// The same history through the message replay path: assistant items (both
	// forms) plus the paired tool result must replay without orphans.
	resp := &message.Response{
		StopReason: "tool_calls",
		ResponsesOutput: []message.ResponsesOutputItem{
			{Type: "function_call", ID: "item_r", CallID: "c_read", Name: "Read", Arguments: `{"path":"a.go"}`},
			{Type: "custom_tool_call", ID: "item_p", CallID: "c_patch", Name: toolname.ApplyPatch, Arguments: "*** Begin Patch\n*** End Patch"},
		},
	}
	replayed := responsesResponseToInputItems(resp, false)
	if len(replayed) != 2 {
		t.Fatalf("replay got %d items, want 2", len(replayed))
	}
	for i, item := range replayed {
		if item.Type != "function_call" {
			t.Errorf("replay item %d type = %q, want function_call", i, item.Type)
		}
	}
}

// TestResponsesMixedHistoryReplaysAsFreeformCustomCalls guards the freeform
// replay target: a session that switched models carries mixed history, but
// when the current request emits apply_patch as a freeform custom tool, both
// legacy function_call items and freeform custom items replay in the custom
// tool shape with raw patch text in input.
func TestResponsesMixedHistoryReplaysAsFreeformCustomCalls(t *testing.T) {
	items := responsesOutputToInputItems([]responsesOutputEntry{
		{Type: "function_call", ID: "item_r", CallID: "c_read", Name: "read", Arguments: `{"path":"a.go"}`},
		{Type: "function_call", ID: "item_p", CallID: "c_patch", Name: toolname.ApplyPatch, Arguments: `{"patch":"*** Begin Patch\n*** End Patch"}`},
	}, true)
	if len(items) != 2 {
		t.Fatalf("got %d input items, want 2", len(items))
	}
	if items[0].Type != "function_call" {
		t.Errorf("item 0 type = %q, want function_call (non-apply_patch untouched)", items[0].Type)
	}
	if items[1].Type != "custom_tool_call" || items[1].Status != "completed" {
		t.Fatalf("item 1 type/status = %q/%q, want custom_tool_call/completed", items[1].Type, items[1].Status)
	}
	if items[1].Input != "*** Begin Patch\n*** End Patch" {
		t.Errorf("item 1 input = %q, want raw patch text", items[1].Input)
	}
	if items[1].Arguments != "" {
		t.Errorf("item 1 arguments = %q, want empty for custom_tool_call", items[1].Arguments)
	}

	// The same history through the message replay path: a legacy apply_patch
	// function_call and a freeform custom call both replay as custom_tool_call.
	resp := &message.Response{
		StopReason: "tool_calls",
		ResponsesOutput: []message.ResponsesOutputItem{
			{Type: "function_call", ID: "item_r", CallID: "c_read", Name: "read", Arguments: `{"path":"a.go"}`},
			{Type: "function_call", ID: "item_p", CallID: "c_patch", Name: toolname.ApplyPatch, Arguments: `{"patch":"*** Begin Patch\n*** End Patch"}`},
			{Type: "custom_tool_call", ID: "item_p2", CallID: "c_patch2", Name: toolname.ApplyPatch, Arguments: "*** Begin Patch\n*** End Patch"},
		},
	}
	replayed := responsesResponseToInputItems(resp, true)
	if len(replayed) != 3 {
		t.Fatalf("replay got %d items, want 3", len(replayed))
	}
	if replayed[0].Type != "function_call" {
		t.Errorf("replay item 0 type = %q, want function_call", replayed[0].Type)
	}
	for i := 1; i < 3; i++ {
		if replayed[i].Type != "custom_tool_call" || replayed[i].Input != "*** Begin Patch\n*** End Patch" {
			t.Errorf("replay item %d = %+v, want custom_tool_call with raw patch input", i, replayed[i])
		}
	}
}

// TestResponsesFreeformReplayPairsToolResults guards that a freeform-targeted
// request pairs replayed apply_patch calls with custom_tool_call_output, while
// non-apply_patch calls keep function_call_output.
func TestResponsesFreeformReplayPairsToolResults(t *testing.T) {
	readArgs := json.RawMessage(`{"path":"a.go"}`)
	patchText := "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch"
	patchArgsJSON, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatal(err)
	}
	patchArgs := json.RawMessage(patchArgsJSON)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "update a.go"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "function_call", CallID: "c_read", Name: "read", Arguments: string(readArgs)},
				{Type: "function_call", CallID: "c_patch", Name: toolname.ApplyPatch, Arguments: string(patchArgs)},
			},
			ToolCalls: []message.ToolCall{
				{ID: "c_read", Name: "read", Args: readArgs},
				{ID: "c_patch", Name: toolname.ApplyPatch, Args: patchArgs},
			},
		},
		{Role: message.RoleTool, ToolCallID: "c_read", Content: "file read"},
		{Role: message.RoleTool, ToolCallID: "c_patch", Content: "patched"},
	}
	items := convertMessagesToResponsesWithItemIDs("", msgs, false, true)
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5 (user + call/output x2)", len(items))
	}
	// read stays function shape with its function_call_output.
	if items[1].Type != "function_call" || items[2].Type != "function_call_output" || items[2].CallID != "c_read" {
		t.Errorf("read pair = %+v / %+v, want function_call + function_call_output", items[1], items[2])
	}
	// apply_patch becomes custom_tool_call + custom_tool_call_output.
	if items[3].Type != "custom_tool_call" || items[3].Input != patchText {
		t.Errorf("apply_patch call = %+v, want custom_tool_call with raw patch", items[3])
	}
	if items[4].Type != "custom_tool_call_output" || items[4].CallID != "c_patch" {
		t.Errorf("apply_patch output = %+v, want custom_tool_call_output", items[4])
	}
}

func TestResponsesResponseToInputItems_CustomToolCallDefensive(t *testing.T) {
	// resp.ResponsesOutput is normally normalized at the wire boundary; this
	// defensive path covers hand-constructed or legacy message items that still
	// carry a raw custom_tool_call shape.
	resp := &message.Response{
		StopReason: "tool_calls",
		ResponsesOutput: []message.ResponsesOutputItem{
			{Type: "custom_tool_call", ID: "item_1", CallID: "ct_1", Name: toolname.ApplyPatch, Arguments: "*** Begin Patch\n*** End Patch"},
		},
	}
	items := responsesResponseToInputItems(resp, false)
	if len(items) != 1 {
		t.Fatalf("got %d input items, want 1", len(items))
	}
	if items[0].Type != "function_call" || items[0].CallID != "ct_1" {
		t.Errorf("replayed item = %#v, want function_call ct_1", items[0])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(items[0].Arguments), &args); err != nil {
		t.Fatalf("replayed args not valid JSON: %v", err)
	}
	if args["patch"] != "*** Begin Patch\n*** End Patch" {
		t.Errorf("replayed patch = %#v, want canonical {patch} object", args["patch"])
	}
}

// TestCollectResponsesOutputBackfillsStreamedCustomArguments guards the
// "Missing required parameter: 'input[N].arguments'" 400: a streaming custom
// apply_patch response's response.completed payload often omits the freeform
// input, so the canonical args must be back-filled from the accumulated tool
// call instead of being persisted empty.
func TestCollectResponsesOutputBackfillsStreamedCustomArguments(t *testing.T) {
	resp := &message.Response{
		ToolCalls: []message.ToolCall{
			{ID: "call_1", Name: toolname.ApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** End Patch"}`)},
		},
	}
	output := []responsesOutputEntry{
		{Type: "custom_tool_call", ID: "call_1", CallID: "call_1", Name: toolname.ApplyPatch},
	}
	collectResponsesOutput(resp, output)
	if len(resp.ResponsesOutput) != 1 {
		t.Fatalf("ResponsesOutput len = %d, want 1", len(resp.ResponsesOutput))
	}
	got := resp.ResponsesOutput[0]
	if got.Type != "function_call" {
		t.Fatalf("item type = %q, want function_call", got.Type)
	}
	if got.Arguments != `{"patch":"*** Begin Patch\n*** End Patch"}` {
		t.Fatalf("arguments = %q, want back-filled canonical patch", got.Arguments)
	}
}

// TestCollectResponsesOutputFallsBackWhenNoAccumulatedCall ensures a custom
// output entry without a matching accumulated call still keeps a valid
// arguments object (never empty, which would drop the wire field).
func TestCollectResponsesOutputFallsBackWhenNoAccumulatedCall(t *testing.T) {
	resp := &message.Response{}
	output := []responsesOutputEntry{
		{Type: "custom_tool_call", ID: "call_x", CallID: "call_x", Name: toolname.ApplyPatch},
	}
	collectResponsesOutput(resp, output)
	if len(resp.ResponsesOutput) != 1 {
		t.Fatalf("ResponsesOutput len = %d, want 1", len(resp.ResponsesOutput))
	}
	if got := resp.ResponsesOutput[0].Arguments; got == "" {
		t.Fatal("arguments empty, want non-empty fallback object")
	}
}

// TestConvertResponsesOutputItemBackfillsMissingArguments guards the replay
// side: legacy or damaged persisted function_call items with an empty
// arguments string must serialize with a valid arguments object, otherwise the
// Responses API rejects the request.
func TestConvertResponsesOutputItemBackfillsMissingArguments(t *testing.T) {
	applyPatch, ok := convertResponsesOutputItem(message.ResponsesOutputItem{
		Type: "function_call", CallID: "c1", Name: toolname.ApplyPatch,
	}, false, false)
	if !ok {
		t.Fatal("apply_patch item rejected")
	}
	if applyPatch.Arguments != `{"patch":""}` {
		t.Fatalf("apply_patch arguments = %q, want {patch:} fallback", applyPatch.Arguments)
	}

	read, ok := convertResponsesOutputItem(message.ResponsesOutputItem{
		Type: "function_call", CallID: "c2", Name: "read",
	}, false, false)
	if !ok {
		t.Fatal("read item rejected")
	}
	if read.Arguments != "{}" {
		t.Fatalf("read arguments = %q, want empty-object fallback", read.Arguments)
	}

	// Existing arguments pass through untouched.
	kept, ok := convertResponsesOutputItem(message.ResponsesOutputItem{
		Type: "function_call", CallID: "c3", Name: toolname.ApplyPatch, Arguments: `{"patch":"real"}`,
	}, false, false)
	if !ok || kept.Arguments != `{"patch":"real"}` {
		t.Fatalf("existing arguments changed: %q ok=%v", kept.Arguments, ok)
	}
}

// TestResponsesReplayNeverDropsArguments runs the full history-replay path: an
// assistant message whose persisted ResponsesOutput has an empty-arguments
// function_call must produce a wire item with a present arguments field.
func TestResponsesReplayNeverDropsArguments(t *testing.T) {
	msgs := []message.Message{{
		Role: "assistant",
		ResponsesOutput: []message.ResponsesOutputItem{
			{Type: "function_call", CallID: "call_1", Name: toolname.ApplyPatch},
		},
	}}
	items := convertMessagesToResponsesWithItemIDs("", msgs, false, false)
	var found bool
	for _, item := range items {
		if item.Type == "function_call" && item.CallID == "call_1" {
			found = true
			if item.Arguments == "" {
				t.Fatal("replayed function_call has empty arguments (would be dropped by omitempty and rejected with input[N].arguments)")
			}
			if item.Arguments != `{"patch":""}` {
				t.Fatalf("replayed arguments = %q, want apply_patch canonical fallback", item.Arguments)
			}
		}
	}
	if !found {
		t.Fatal("replayed function_call item not found")
	}
}

func TestParseResponsesSSE_CustomToolCall(t *testing.T) {
	t.Run("deltas_by_item_id", func(t *testing.T) {
		// Custom call lands at output_index 1 (after a text message at 0) and
		// its input deltas only carry item_id, no output_index.
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"*** Begin Patch\n"}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch","status":"completed","input":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		tc := resp.ToolCalls[0]
		if tc.ID != "ct_1" || tc.Name != toolname.ApplyPatch {
			t.Errorf("tool call id=%q name=%q, want ct_1 apply_patch", tc.ID, tc.Name)
		}
		var args map[string]any
		if err := json.Unmarshal(tc.Args, &args); err != nil {
			t.Fatalf("tool args not valid JSON: %v", err)
		}
		if args["patch"] != "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch" {
			t.Errorf("args[patch] = %#v, want the full patch text", args["patch"])
		}
	})

	t.Run("delta_before_added_registers_synthetic_index", func(t *testing.T) {
		// Defensive path: a custom delta arrives before its output_item.added,
		// and the done event carries no input — the patch must come entirely
		// from the deltas (the synthetic accumulator migrates on added).
		stream := buildSSEStream([]string{
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_x","delta":"*** Begin Patch\n"}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_x","delta":"*** End Patch"}`,
			`{"type":"response.output_item.added","output_index":2,"item":{"type":"custom_tool_call","id":"ct_x","name":"apply_patch"}}`,
			`{"type":"response.output_item.done","output_index":2,"item":{"type":"custom_tool_call","id":"ct_x","name":"apply_patch","status":"completed"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		var args map[string]any
		if err := json.Unmarshal(resp.ToolCalls[0].Args, &args); err != nil {
			t.Fatalf("tool args not valid JSON: %v", err)
		}
		if args["patch"] != "*** Begin Patch\n*** End Patch" {
			t.Errorf("args[patch] = %#v, want the deltas preserved (no done.input fallback)", args["patch"])
		}
	})

	t.Run("done_without_added_recovers_from_done_input", func(t *testing.T) {
		// Truncated stream: the added event is missed entirely, only the done
		// payload (with complete input) arrives. The accumulator is created
		// from the done item so the call is not dropped.
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.done","output_index":3,"item":{"type":"custom_tool_call","id":"ct_y","name":"apply_patch","status":"completed","input":"*** Begin Patch\n*** End Patch"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		tc := resp.ToolCalls[0]
		if tc.ID != "ct_y" || tc.Name != toolname.ApplyPatch {
			t.Errorf("tool call id=%q name=%q, want ct_y apply_patch", tc.ID, tc.Name)
		}
		var args map[string]any
		if err := json.Unmarshal(tc.Args, &args); err != nil {
			t.Fatalf("tool args not valid JSON: %v", err)
		}
		if args["patch"] != "*** Begin Patch\n*** End Patch" {
			t.Errorf("args[patch] = %#v, want the done input recovered", args["patch"])
		}
	})

	t.Run("mixed_custom_and_function_calls", func(t *testing.T) {
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Read"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"a.go\"}"}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"*** Begin Patch\n*** End Patch"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Read","arguments":"{\"path\":\"a.go\"}","status":"completed"}}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch","status":"completed","input":"*** Begin Patch\n*** End Patch"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 2 {
			t.Fatalf("got %d tool calls, want 2", len(resp.ToolCalls))
		}
		if resp.ToolCalls[0].Name != "Read" || resp.ToolCalls[1].Name != toolname.ApplyPatch {
			t.Errorf("tool order/names = %q, %q, want Read, apply_patch", resp.ToolCalls[0].Name, resp.ToolCalls[1].Name)
		}
		var args map[string]any
		if err := json.Unmarshal(resp.ToolCalls[1].Args, &args); err != nil {
			t.Fatalf("custom args not valid JSON: %v", err)
		}
		if args["patch"] != "*** Begin Patch\n*** End Patch" {
			t.Errorf("args[patch] = %#v, want full patch", args["patch"])
		}
	})
}

func TestResponsesParallelToolCallsWithCustomTool(t *testing.T) {
	captureBody := func(serverURL string) map[string]any {
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer server.Close()
		provider := NewProviderConfig("sample", config.ProviderConfig{
			Type:   config.ProviderTypeResponses,
			APIURL: server.URL,
		}, []string{"test-key"})
		r := &ResponsesProvider{provider: provider, client: server.Client()}
		_, err := r.CompleteStream(
			context.Background(), "test-key", "gpt-5.5", "",
			[]message.Message{{Role: "user", Content: "hello"}},
			sampleToolDefs(), 0, RequestTuning{},
			func(message.StreamDelta) {},
		)
		if err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
		return gotBody
	}

	t.Run("custom_apply_patch_forces_parallel_false", func(t *testing.T) {
		body := captureBody("")
		tools, ok := body["tools"].([]any)
		if !ok || len(tools) == 0 {
			t.Fatalf("request tools = %#v, want custom apply_patch tool", body["tools"])
		}
		first := tools[0].(map[string]any)
		if first["type"] != "custom" {
			t.Errorf("apply_patch tool type = %v, want custom on the wire", first["type"])
		}
		if first["parameters"] != nil {
			t.Errorf("custom tool must not carry parameters, got %v", first["parameters"])
		}
		if first["format"] == nil {
			t.Error("custom tool must carry format block")
		}
		if got := body["parallel_tool_calls"]; got != false {
			t.Errorf("parallel_tool_calls = %#v, want false with custom apply_patch", got)
		}
	})

	t.Run("explicit_parallel_override_wins", func(t *testing.T) {
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer server.Close()
		provider := NewProviderConfig("sample", config.ProviderConfig{
			Type:   config.ProviderTypeResponses,
			APIURL: server.URL,
		}, []string{"test-key"})
		r := &ResponsesProvider{provider: provider, client: server.Client()}
		_, err := r.CompleteStream(
			context.Background(), "test-key", "gpt-5.5", "",
			[]message.Message{{Role: "user", Content: "hello"}},
			sampleToolDefs(), 0, RequestTuning{OpenAI: OpenAITuning{ParallelToolCalls: new(true)}},
			func(message.StreamDelta) {},
		)
		if err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
		if got := gotBody["parallel_tool_calls"]; got != true {
			t.Errorf("parallel_tool_calls = %#v, want explicit true override", got)
		}
	})
}

func TestCompactParallelToolCalls(t *testing.T) {
	custom := []responsesTool{{Type: "custom", Name: "apply_patch", Format: &responsesToolFormat{}}}
	plain := []responsesTool{{Type: "function", Name: "apply_patch", Parameters: map[string]any{}}}

	t.Run("custom_forces_false", func(t *testing.T) {
		if got := compactParallelToolCalls(custom, nil); got == nil || *got {
			t.Fatalf("compactParallelToolCalls(custom, nil) = %v, want false", got)
		}
	})
	t.Run("explicit_wins_over_custom", func(t *testing.T) {
		if got := compactParallelToolCalls(custom, new(true)); got == nil || !*got {
			t.Fatalf("compactParallelToolCalls(custom, true) = %v, want true", got)
		}
	})
	t.Run("plain_function_keeps_omit", func(t *testing.T) {
		if got := compactParallelToolCalls(plain, nil); got != nil {
			t.Fatalf("compactParallelToolCalls(function, nil) = %v, want nil (omit)", got)
		}
	})
	t.Run("no_tools_omits", func(t *testing.T) {
		if got := compactParallelToolCalls(nil, nil); got != nil {
			t.Fatalf("compactParallelToolCalls(nil, nil) = %v, want nil", got)
		}
	})
}

// TestApplyPatchCompatMerge exercises the three-state merge used by both the
// freeform decision and the agent tool-surface policy.
func TestApplyPatchCompatMerge(t *testing.T) {
	provider := responsesProviderFor(t, config.ProviderConfig{
		Compat: &config.ProviderCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{
			Enabled:  new(false),
			Freeform: new(true),
		}},
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Compat: &config.ModelCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Enabled: new(true)}}},
		},
	})
	// Model-level overrides only the enabled knob; freeform stays provider-level.
	merged := provider.ApplyPatchCompat("gpt-5.5")
	if merged == nil {
		t.Fatal("ApplyPatchCompat returned nil")
	}
	if merged.Enabled == nil || !*merged.Enabled {
		t.Errorf("merged enabled = %v, want model-level true", merged.Enabled)
	}
	if merged.Freeform == nil || !*merged.Freeform {
		t.Errorf("merged freeform = %v, want provider-level true", merged.Freeform)
	}
	// Unlisted model keeps provider defaults.
	unlisted := provider.ApplyPatchCompat("gpt-5.6")
	if unlisted == nil || unlisted.Enabled == nil || *unlisted.Enabled {
		t.Errorf("unlisted merged = %#v, want provider enabled:false", unlisted)
	}
	// No compat at all returns nil (three-state: caller infers).
	plain := responsesProviderFor(t, config.ProviderConfig{})
	if got := plain.ApplyPatchCompat("gpt-5.5"); got != nil {
		t.Errorf("unconfigured ApplyPatchCompat = %#v, want nil", got)
	}
}

// TestResponsesToolCustomShapeMarshal pins the exact custom tool wire shape.
func TestResponsesToolCustomShapeMarshal(t *testing.T) {
	provider := responsesProviderFor(t, config.ProviderConfig{})
	tools := convertToolsToResponsesForTarget(provider, "gpt-5.5", sampleToolDefs())
	raw, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatalf("marshal custom tool: %v", err)
	}
	wantTool := responsesTool{
		Type:        "custom",
		Name:        "apply_patch",
		Description: "The `apply_patch` tool can be used to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
		Format: &responsesToolFormat{
			Type:       "grammar",
			Syntax:     "lark",
			Definition: responsesApplyPatchLarkGrammar,
		},
	}
	want, err := json.Marshal(wantTool)
	if err != nil {
		t.Fatalf("marshal expected custom tool: %v", err)
	}
	if string(raw) != string(want) {
		t.Errorf("custom tool marshal =\n%s\nwant\n%s", raw, want)
	}
}

// TestClientUsesApplyPatchSurface exercises the resolved tool-surface policy
// the agent consumes: an explicit compat.apply_patch.enabled value wins over
// model-name inference, and an unconfigured client resolves from the primary
// model ID (gpt-5-and-later family → patch surface).
func TestClientUsesApplyPatchSurface(t *testing.T) {
	t.Run("unconfigured_infers_from_primary_model", func(t *testing.T) {
		c := NewClient(responsesProviderFor(t, config.ProviderConfig{}), &noopProvider{}, "gpt-5.5", 1024, "")
		if !c.UsesApplyPatchSurface() {
			t.Error("unconfigured gpt-5.5 policy = false, want true (name inference)")
		}
		c = NewClient(responsesProviderFor(t, config.ProviderConfig{}), &noopProvider{}, "deepseek-v4-flash", 1024, "")
		if c.UsesApplyPatchSurface() {
			t.Error("unconfigured deepseek policy = true, want false (name inference)")
		}
	})
	t.Run("enabled_true_overrides_non_patch_native", func(t *testing.T) {
		c := NewClient(responsesProviderFor(t, config.ProviderConfig{
			Compat: &config.ProviderCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Enabled: new(true)}},
		}), &noopProvider{}, "deepseek-v4-flash", 1024, "")
		if !c.UsesApplyPatchSurface() {
			t.Error("enabled:true policy = false, want true")
		}
	})
	t.Run("enabled_false_overrides_patch_native", func(t *testing.T) {
		c := NewClient(responsesProviderFor(t, config.ProviderConfig{
			Compat: &config.ProviderCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Enabled: new(false)}},
		}), &noopProvider{}, "gpt-5.5", 1024, "")
		if c.UsesApplyPatchSurface() {
			t.Error("enabled:false policy = true, want false")
		}
	})
	t.Run("model_level_override", func(t *testing.T) {
		c := NewClient(responsesProviderFor(t, config.ProviderConfig{
			Compat: &config.ProviderCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Enabled: new(true)}},
			Models: map[string]config.ModelConfig{
				"gpt-5.5": {Compat: &config.ModelCompatConfig{ApplyPatch: &config.ApplyPatchCompatConfig{Enabled: new(false)}}},
			},
		}), &noopProvider{}, "gpt-5.5", 1024, "")
		if c.UsesApplyPatchSurface() {
			t.Error("model-level enabled:false policy = true, want false")
		}
	})
}

// TestResponsesFreeformReplayPairsInterleavedOutputs guards that apply_patch
// outputs whose calls are not in the trailing run (reasoning interleaved after
// the call) still replay typed to pair with the custom_tool_call the call
// becomes under a freeform target.
func TestResponsesFreeformReplayPairsInterleavedOutputs(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch"
	patchArgsJSON, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "update a.go"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "function_call", CallID: "c_patch", Name: toolname.ApplyPatch, Arguments: string(patchArgsJSON)},
				{Type: "reasoning", ID: "item_r", EncryptedContent: "enc"},
			},
		},
		{Role: message.RoleTool, ToolCallID: "c_patch", Content: "patched"},
	}
	items := convertMessagesToResponsesWithItemIDs("", msgs, false, true)
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (user + call + reasoning + output)", len(items))
	}
	if items[1].Type != "custom_tool_call" || items[1].Input != patchText {
		t.Errorf("apply_patch call = %+v, want custom_tool_call with raw patch", items[1])
	}
	if items[3].Type != "custom_tool_call_output" || items[3].CallID != "c_patch" {
		t.Errorf("interleaved apply_patch output = %+v, want custom_tool_call_output", items[3])
	}
}

// TestResponsesLegacyToolCallsReplayAsFreeformCustomCalls guards that a session
// recorded on a chat-completions provider (ToolCalls only, no ResponsesOutput)
// replays apply_patch history in the freeform shape when switched to a
// freeform target, instead of referencing an undeclared function.
func TestResponsesLegacyToolCallsReplayAsFreeformCustomCalls(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch"
	patchArgsJSON, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "update a.go"},
		{
			Role:      message.RoleAssistant,
			ToolCalls: []message.ToolCall{{ID: "c_patch", Name: toolname.ApplyPatch, Args: patchArgsJSON}},
		},
		{Role: message.RoleTool, ToolCallID: "c_patch", Content: "patched"},
	}
	items := convertMessagesToResponsesWithItemIDs("", msgs, false, true)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (user + call + output)", len(items))
	}
	if items[1].Type != "custom_tool_call" || items[1].Input != patchText {
		t.Errorf("legacy apply_patch call = %+v, want custom_tool_call with raw patch", items[1])
	}
	if items[2].Type != "custom_tool_call_output" || items[2].CallID != "c_patch" {
		t.Errorf("legacy apply_patch output = %+v, want custom_tool_call_output", items[2])
	}
}

// TestFinalizeResponsesToolCallsEmptyCustomInput guards that a freeform custom
// accumulator that saw no input finalizes to the canonical empty patch object
// instead of replaying the "{}" placeholder as literal patch text.
func TestFinalizeResponsesToolCallsEmptyCustomInput(t *testing.T) {
	resp := &message.Response{}
	calls := map[int]*responsesToolAccumulator{
		0: {id: "ct_1", name: toolname.ApplyPatch, custom: true},
	}
	finalizeResponsesToolCalls(calls, resp, nil, false, map[string]bool{})
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	if string(resp.ToolCalls[0].Args) != `{"patch":""}` {
		t.Errorf("empty custom input args = %q, want canonical empty patch object", resp.ToolCalls[0].Args)
	}
}
