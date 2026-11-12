package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// runMCPMount runs the two-phase mount the request builder uses: anchor replay
// first, then clean-tail first emission over the replayed projection.
func runMCPMount(a *MainAgent, messages []message.Message, mode mcpToolMountMode) ([]message.Message, []message.ToolDefinition, bool) {
	replayed, defs, fellBack, _ := a.mountRuntimeMCPTools(messages, mode)
	out := a.mountPendingRuntimeMCPDeclarations(replayed, mode)
	return out, defs, fellBack
}

func TestLoopDoneLateMountAppendsDynamicToolWhenMissingFromFrozenSurface(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
done: allow
`)}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopDoneLateMount.Store(true)
	a.freezeToolSurfaceFromDefinitions(nil)

	msgs := []message.Message{{Role: message.RoleUser, Content: "continue"}}
	mounted, _, fellBack := runMCPMount(a, msgs, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("done late mount must not fall back")
	}
	if len(mounted) != 2 {
		t.Fatalf("mounted len = %d, want 2", len(mounted))
	}
	last := mounted[1]
	if len(last.MCPTools) != 1 || last.MCPTools[0].Name != tools.NameDone {
		t.Fatalf("late mounted tools = %#v", last.MCPTools)
	}
	if defs := a.mainLLMToolDefinitions(); len(defs) != 0 {
		t.Fatalf("frozen tool defs changed: %#v", defs)
	}

	// The anchored declaration replays on the next request instead of being
	// appended again: done enters the same snapshot bookkeeping as MCP tools.
	replayed, _, fellBack := runMCPMount(a, msgs, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("done replay must not fall back")
	}
	if countMCPMounts(replayed) != 1 {
		t.Fatalf("replay mount count = %d, want 1: %#v", countMCPMounts(replayed), replayed)
	}
	assertMCPMountAt(t, replayed, 1, tools.NameDone)
}

func TestLoopDoneLateMountSkipsWhenDoneAlreadyInFrozenSurface(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
done: allow
`)}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopDoneLateMount.Store(true)
	a.freezeToolSurfaceFromDefinitions(llmToolDefinitionsFromVisibleTools([]tools.Tool{tools.NewDoneTool()}))

	msgs := []message.Message{{Role: message.RoleUser, Content: "continue"}}
	mounted, _, _ := runMCPMount(a, msgs, mcpMountKimiDynamic)
	if len(mounted) != 1 {
		t.Fatalf("mounted len = %d, want 1", len(mounted))
	}
	if a.loopDoneLateMount.Load() {
		t.Fatal("late mount flag should clear when Done already exists")
	}
}

func TestMountDoneForLoopEntryRequiresDynamicCapability(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
done: allow
`)}
	a.rebuildRuleset()
	a.freezeToolSurfaceFromDefinitions(nil)
	a.surfaceDirty.Store(false)
	a.mountDoneForLoopEntry()
	if a.loopDoneLateMount.Load() {
		t.Fatal("late mount should stay disabled without dynamic capability")
	}
	// Without a late-mount path the tool surface must be rebuilt instead, or
	// the loop would run without the exit signal it requires.
	if !a.surfaceDirty.Load() {
		t.Fatal("loop entry without a late-mount path must mark the tool surface dirty")
	}

	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1",
		Compat: &config.ProviderCompatConfig{ChatCompletions: &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(true)}},
		Models: map[string]config.ModelConfig{"model-1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(provider, nil, "model-1", 512, "")
	a.surfaceDirty.Store(false)
	a.mountDoneForLoopEntry()
	if !a.loopDoneLateMount.Load() {
		t.Fatal("late mount should enable with dynamic capability")
	}
	if a.surfaceDirty.Load() {
		t.Fatal("a late mount must not cost a tool-surface rebuild")
	}
}

func TestMountDoneForLoopEntrySkipsWhenRealFrozenSurfaceAlreadyContainsDone(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
done: allow
`)}
	a.rebuildRuleset()

	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1",
		Compat: &config.ProviderCompatConfig{ChatCompletions: &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(true)}},
		Models: map[string]config.ModelConfig{"model-1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(provider, nil, "model-1", 512, "")

	// done joins the tool surface only while a loop is active, so the surface
	// has to be frozen with the loop already running for it to contain done.
	a.loopState.enableWithTarget("finish current task")
	a.freezeToolSurface()
	a.mountDoneForLoopEntry()
	if a.loopDoneLateMount.Load() {
		t.Fatal("late mount should stay disabled when frozen surface already contains done")
	}
	if !a.doneToolVisibleNow() {
		t.Fatal("done should remain available from the frozen visible surface")
	}
}

func TestMountDoneForNonCacheFriendlyModeStaysOffDeclarations(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
done: allow
`)}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopDoneLateMount.Store(true)
	// Without a frozen surface the visible tool set already carries done while
	// the loop is active, so the late mount clears on sight; freeze an empty
	// surface to exercise the late-mount path itself.
	a.freezeToolSurfaceFromDefinitions(nil)

	msgs := []message.Message{{Role: message.RoleUser, Content: "continue"}}
	mounted, _, _ := runMCPMount(a, msgs, mcpMountFullInjection)
	if len(mounted) != 1 {
		t.Fatalf("mounted len = %d, want 1", len(mounted))
	}
	// The full-injection contract carries done through the effective
	// declaration set instead of a declaration message.
	if defs := a.effectiveRuntimeMCPDeclarations(); len(defs) != 1 || defs[0].Name != tools.NameDone {
		t.Fatalf("effective declarations = %#v, want done", defs)
	}
	// Once the rebuilt surface carries done, the late mount clears.
	a.freezeToolSurfaceFromDefinitions(llmToolDefinitionsFromVisibleTools([]tools.Tool{tools.NewDoneTool()}))
	if defs := a.effectiveRuntimeMCPDeclarations(); len(defs) != 0 {
		t.Fatalf("effective declarations with done on the surface = %#v, want none", defs)
	}
	if a.loopDoneLateMount.Load() {
		t.Fatal("late mount flag should clear when the surface carries done")
	}
}

type anchoredManualMCPTool struct {
	name        string
	description string
}

func (t anchoredManualMCPTool) Name() string               { return t.name }
func (t anchoredManualMCPTool) Description() string        { return t.description }
func (t anchoredManualMCPTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t anchoredManualMCPTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
func (t anchoredManualMCPTool) IsReadOnly() bool { return false }
func (t anchoredManualMCPTool) IsManual() bool   { return true }

// sealEmptyMCPMountBaseline freezes an empty top-level manual baseline,
// simulating the ordinary sequence where the first request was built before
// any manual MCP server was enabled, so tools registered afterwards use the
// incremental declarations.
func sealEmptyMCPMountBaseline(a *MainAgent) {
	a.mcpMountState.mu.Lock()
	a.mcpMountState.topLevelManualLocked(nil)
	a.mcpMountState.mu.Unlock()
}

func TestRuntimeMCPMountReplaysAnchorAndOnlyAppendsChanges(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	firstInput := []message.Message{{Role: message.RoleUser, Content: "first"}}
	first, fallback, fellBack := runMCPMount(a, firstInput, mcpMountKimiDynamic)
	if fellBack || len(fallback) != 0 {
		t.Fatalf("first mount fallback = %v, defs = %#v", fellBack, fallback)
	}
	assertMCPMountAt(t, first, 1, "mcp_sample_lookup")

	secondInput := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup"}}},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "result"},
		{Role: message.RoleUser, Content: "second"},
	}
	second, fallback, fellBack := runMCPMount(a, secondInput, mcpMountKimiDynamic)
	if fellBack || len(fallback) != 0 {
		t.Fatalf("second mount fallback = %v, defs = %#v", fellBack, fallback)
	}
	assertMCPMountAt(t, second, 1, "mcp_sample_lookup")
	if got := countMCPMounts(second); got != 1 {
		t.Fatalf("second request mount count = %d, want 1", got)
	}

	if removed := a.tools.UnregisterPrefix("mcp_"); removed != 1 {
		t.Fatalf("disabled tool count = %d, want 1", removed)
	}
	disabled, _, fellBack := runMCPMount(a, secondInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("disable should retain the original declaration anchor")
	}
	assertMCPMountAt(t, disabled, 1, "mcp_sample_lookup")

	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	third, _, fellBack := runMCPMount(a, secondInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("new tool should append without moving the original anchor")
	}
	assertMCPMountAt(t, third, 1, "mcp_sample_lookup")
	assertMCPMountAt(t, third, len(third)-1, "mcp_sample_fetch")

	// The fetch emission anchors at secondInput's clean tail; the model's
	// response plus the next user turn pin that snapshot before the schema
	// change arrives — without them the unresponded declaration rolls back.
	fetchedInput := append(append([]message.Message(nil), secondInput...),
		message.Message{Role: message.RoleAssistant, Content: "fetched"},
		message.Message{Role: message.RoleUser, Content: "third"},
	)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "updated lookup"})
	fourth, _, fellBack := runMCPMount(a, fetchedInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("schema change should append a replacement declaration")
	}
	if got := countMCPMounts(fourth); got != 3 {
		t.Fatalf("schema change mount count = %d, want 3", got)
	}
	last := fourth[len(fourth)-1].MCPTools
	if len(last) != 1 || last[0].Name != "mcp_sample_lookup" || last[0].Description != "updated lookup" {
		t.Fatalf("replacement declaration = %#v", last)
	}
}

func TestRuntimeMCPMountFallsBackWhenAnchorCannotBeRebuilt(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	_, _, fellBack := runMCPMount(a,
		[]message.Message{{Role: message.RoleUser, Content: "first"}},
		mcpMountResponsesAdditionalTools,
	)
	if fellBack {
		t.Fatal("initial mount unexpectedly fell back")
	}

	messages, fallback, fellBack := runMCPMount(a,
		[]message.Message{{Role: message.RoleUser, Content: "replacement"}},
		mcpMountResponsesAdditionalTools,
	)
	if !fellBack || len(fallback) != 1 {
		t.Fatalf("missing anchor fallback = %v, defs = %#v", fellBack, fallback)
	}
	if countMCPMounts(messages) != 0 {
		t.Fatalf("fallback request should not contain request-only mounts: %#v", messages)
	}
}

func TestRuntimeMCPMountUsesDuplicateAnchorOccurrence(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	firstInput := []message.Message{
		{Role: message.RoleUser, Content: "same"},
		{Role: message.RoleUser, Content: "same"},
	}
	first, _, fellBack := runMCPMount(a, firstInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("initial duplicate anchor unexpectedly fell back")
	}
	assertMCPMountAt(t, first, 2, "mcp_sample_lookup")

	nextInput := append(append([]message.Message(nil), firstInput...), message.Message{Role: message.RoleAssistant, Content: "done"})
	next, _, fellBack := runMCPMount(a, nextInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("duplicate anchor occurrence was not restored")
	}
	assertMCPMountAt(t, next, 2, "mcp_sample_lookup")
}

func TestMCPMountIncrementalMessageShapesDetectNestedChange(t *testing.T) {
	state := &mcpToolMountState{}
	messages := []message.Message{{
		Role:      message.RoleAssistant,
		ToolCalls: []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup", Args: json.RawMessage(`{"value":"first"}`)}},
	}}
	first := append([]requestshape.Shape(nil), state.incrementalMessageShapes(messages)...)
	messages[0].ToolCalls = []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup", Args: json.RawMessage(`{"value":"second"}`)}}
	second := state.incrementalMessageShapes(messages)
	if first[0] == second[0] {
		t.Fatal("nested tool-call change reused the previous message shape")
	}
}

func TestRuntimeMCPMountResumeWithHistoricalCallUsesTopLevelTools(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	messages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup"}}},
	}

	got, fallback, fellBack := runMCPMount(a, messages, mcpMountKimiDynamic)
	if !fellBack || len(fallback) != 1 {
		t.Fatalf("resume fallback = %v, defs = %#v", fellBack, fallback)
	}
	if countMCPMounts(got) != 0 {
		t.Fatalf("resume fallback unexpectedly inserted a declaration: %#v", got)
	}
}

// A reset run (model switch, restore, compaction) keeps the history that
// called tools declared by the previous run. Once the new run has declared
// something of its own, a re-enabled tool with historical calls must still fall
// back rather than get its first declaration behind those calls.
func TestRuntimeMCPMountFallsBackForUndeclaredToolWithHistoricalCallAfterOtherDeclarations(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	messages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup"}}},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "result"},
		{Role: message.RoleUser, Content: "second"},
	}
	first, _, fellBack := runMCPMount(a, messages, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("a tool without historical calls must mount dynamically")
	}
	assertMCPMountAt(t, first, len(first)-1, "mcp_sample_fetch")

	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	got, fallback, fellBack := runMCPMount(a, messages, mcpMountKimiDynamic)
	if !fellBack || len(fallback) != 2 {
		t.Fatalf("undeclared tool with historical calls: fallback = %v, defs = %#v", fellBack, fallback)
	}
	if countMCPMounts(got) != 0 {
		t.Fatalf("fallback request should not contain request-only mounts: %#v", got)
	}
}

func newResponsesAdditionalToolsClient(model string) *llm.Client {
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: "https://example.invalid/v1/responses",
		Models: map[string]config.ModelConfig{
			model: {
				Limit:  config.ModelLimit{Context: 128000, Output: 4096},
				Compat: &config.ModelCompatConfig{Responses: &config.ResponsesCompatConfig{MCPAdditionalTools: new(true)}},
			},
		},
	}, []string{"test-key"})
	return llm.NewClient(provider, stubProvider{}, model, 4096, "")
}

func TestModelSwitchRebaselinesManualToolsAndKeepsDynamicMount(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.SetProviderModelRef("sample/model-a")

	a.swapLLMClientWithRef(newResponsesAdditionalToolsClient("model-b"), "model-b", 4096, "sample/model-b")

	if got := a.mcpToolMountMode(); got != mcpMountResponsesAdditionalTools {
		t.Fatalf("mount mode after model switch = %v, want responses additional_tools", got)
	}
	// The switched run rebuilds its baseline: a manual tool enabled before the
	// switch rides in the top-level array again instead of staying a dynamic
	// declaration relative to the previous run's baseline.
	topLevel := false
	for _, tool := range a.stableVisibleLLMTools() {
		if tool.Name() == "mcp_sample_lookup" {
			topLevel = true
		}
	}
	if !topLevel {
		t.Fatal("model switch must re-baseline enabled manual MCP tools at top level")
	}
}

// A resume (resetSessionBuildState) and a model switch both reset the mount
// bookkeeping: each rebuild re-baselines every currently enabled manual tool
// at top level — the boundary cache is cold — and dynamic mounts stay
// available for enablements after the boundary.
func TestModelSwitchAfterResumeRebaselinesAndResumesDynamicMount(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.llmClient = newResponsesAdditionalToolsClient("model-a")
	a.SetProviderModelRef("sample/model-a")
	a.resetSessionBuildState()

	a.swapLLMClientWithRef(newResponsesAdditionalToolsClient("model-b"), "model-b", 4096, "sample/model-b")

	if got := a.mcpToolMountMode(); got != mcpMountResponsesAdditionalTools {
		t.Fatalf("mount mode after a post-resume model switch = %v, want responses additional_tools", got)
	}
	topLevel := false
	for _, tool := range a.stableVisibleLLMTools() {
		if tool.Name() == "mcp_sample_lookup" {
			topLevel = true
		}
	}
	if !topLevel {
		t.Fatal("the switched run must re-baseline enabled manual MCP tools at top level")
	}
}

func TestModelSwitchToTargetWithoutDynamicMountUsesTopLevelTools(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.SetProviderModelRef("sample/model-a")

	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1",
		Models: map[string]config.ModelConfig{"model-c": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}},
	}, []string{"test-key"})
	a.swapLLMClientWithRef(llm.NewClient(provider, stubProvider{}, "model-c", 4096, ""), "model-c", 4096, "sample/model-c")

	if got := a.mcpToolMountMode(); got != mcpMountFullInjection {
		t.Fatalf("mount mode after switch to a plain chat target = %v, want full injection", got)
	}
}

func TestMCPToolMountModeFollowsSelectedTargetInMixedPool(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())

	responsesProvider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: "https://example.invalid/v1/responses",
		Models: map[string]config.ModelConfig{"model-a": {
			Limit:  config.ModelLimit{Context: 128000, Output: 4096},
			Compat: &config.ModelCompatConfig{Responses: &config.ResponsesCompatConfig{MCPAdditionalTools: new(true)}},
		}},
	}, []string{"test-key"})
	chatProvider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1/chat/completions",
		Models: map[string]config.ModelConfig{"model-b": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}},
	}, []string{"test-key"})

	client := llm.NewClient(responsesProvider, stubProvider{}, "model-a", 4096, "")
	client.SetModelPool([]llm.FallbackModel{
		{ProviderConfig: responsesProvider, ProviderImpl: stubProvider{}, ModelID: "model-a", MaxTokens: 4096},
		{ProviderConfig: chatProvider, ProviderImpl: stubProvider{}, ModelID: "model-b", MaxTokens: 4096},
	}, 0)
	a.llmClient = client

	a.SetProviderModelRef("sample/model-a")
	if got := a.mcpToolMountMode(); got != mcpMountResponsesAdditionalTools {
		t.Fatalf("mount mode for the responses target = %v, want responses additional_tools", got)
	}
	a.SetProviderModelRef("sample/model-b")
	if got := a.mcpToolMountMode(); got != mcpMountFullInjection {
		t.Fatalf("mount mode for the plain chat target = %v, want full injection", got)
	}
}

func TestRuntimeMCPMountFallbackStopsReturningDefsOnceSurfaceRebuilt(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.llmClient = newResponsesAdditionalToolsClient("custom-model")

	messages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup"}}},
	}

	// The triggering request still needs the defs: its frozen surface was
	// built stable-only before the fallback was detected.
	_, fallback, fellBack := runMCPMount(a, messages, mcpMountResponsesAdditionalTools)
	if !fellBack || len(fallback) != 1 {
		t.Fatalf("fallback trigger = %v, defs = %#v", fellBack, fallback)
	}

	// After the fallback the surface is rebuilt with every manual tool, so
	// later requests must not receive extra defs on top of it.
	manualVisible := false
	for _, tool := range a.stableVisibleLLMTools() {
		if tool.Name() == "mcp_sample_lookup" {
			manualVisible = true
		}
	}
	if !manualVisible {
		t.Fatal("fallback surface should include manual MCP tools")
	}
	_, fallback, fellBack = runMCPMount(a, messages, mcpMountResponsesAdditionalTools)
	if fellBack {
		t.Fatal("sticky fallback should not report a new fallback")
	}
	if len(fallback) != 0 {
		t.Fatalf("sticky fallback returned defs = %#v, want none (frozen surface already carries them)", fallback)
	}
}

func assertMCPMountAt(t *testing.T, messages []message.Message, index int, toolName string) {
	t.Helper()
	if index < 0 || index >= len(messages) {
		t.Fatalf("mount index %d outside %d messages", index, len(messages))
	}
	defs := messages[index].MCPTools
	if len(defs) != 1 || defs[0].Name != toolName {
		t.Fatalf("messages[%d].MCPTools = %#v, want %s", index, defs, toolName)
	}
}

func countMCPMounts(messages []message.Message) int {
	count := 0
	for _, msg := range messages {
		if len(msg.MCPTools) > 0 {
			count++
		}
	}
	return count
}

// Durable compaction rewrites the message prefix, so anchors recorded before
// it cannot be replayed. The apply path resets the mount bookkeeping without
// pinning the run: the next build re-baselines enabled manual tools at top
// level (the boundary cache is cold) and a later toggle of a fresh server
// appends a dynamic declaration again.
func TestCompactionResetRebaselinesAndResumesDynamicMount(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.llmClient = newResponsesAdditionalToolsClient("custom-model")

	// Pre-compaction run: baseline sealed empty, so the enabled tool rides as
	// a dynamic declaration anchored at the conversation tail.
	sealEmptyMCPMountBaseline(a)
	messages := []message.Message{{Role: message.RoleUser, Content: "first"}}
	mounted, defs, fellBack := runMCPMount(a, messages, mcpMountResponsesAdditionalTools)
	if fellBack || len(defs) != 0 {
		t.Fatalf("pre-compaction mount fallback = %v, defs = %#v", fellBack, defs)
	}
	if countMCPMounts(mounted) != 1 {
		t.Fatalf("pre-compaction mount count = %d, want 1: %#v", countMCPMounts(mounted), mounted)
	}

	// Compaction apply resets the bookkeeping against the rewritten history.
	a.resetMCPMountSurface()

	if got := a.mcpToolMountMode(); got != mcpMountResponsesAdditionalTools {
		t.Fatalf("mount mode after compaction reset = %v, want responses additional_tools", got)
	}
	topLevel := false
	for _, tool := range a.stableVisibleLLMTools() {
		if tool.Name() == "mcp_sample_lookup" {
			topLevel = true
		}
	}
	if !topLevel {
		t.Fatal("compaction must re-baseline enabled manual MCP tools at top level")
	}

	// A server enabled after the boundary uses the incremental declaration
	// channel again; the re-baselined tool stays undeclared.
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	next, defs, fellBack := runMCPMount(a, messages, mcpMountResponsesAdditionalTools)
	if fellBack || len(defs) != 0 {
		t.Fatalf("post-compaction mount fallback = %v, defs = %#v", fellBack, defs)
	}
	if countMCPMounts(next) != 1 {
		t.Fatalf("post-compaction mount count = %d, want 1: %#v", countMCPMounts(next), next)
	}
	assertMCPMountAt(t, next, len(next)-1, "mcp_sample_fetch")
}

func TestManualMCPEnabledBeforeFirstRequestRidesTopLevelTools(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.llmClient = newResponsesAdditionalToolsClient("custom-model")

	// First surface build: no request has been sent yet, so the already
	// enabled manual tool rides in the top-level tools array.
	topLevel := false
	for _, tool := range a.stableVisibleLLMTools() {
		if tool.Name() == "mcp_sample_lookup" {
			topLevel = true
		}
	}
	if !topLevel {
		t.Fatal("manual tool enabled before the first request should ride top-level tools")
	}

	messages := []message.Message{{Role: message.RoleUser, Content: "first"}}
	mounted, defs, fellBack := runMCPMount(a, messages, mcpMountResponsesAdditionalTools)
	if fellBack || len(defs) != 0 {
		t.Fatalf("baseline mount fallback = %v, defs = %#v", fellBack, defs)
	}
	if countMCPMounts(mounted) != 0 {
		t.Fatalf("baseline tools must not be declared incrementally: %#v", mounted)
	}

	// A server enabled after the first request uses the incremental
	// declaration channel; the baseline tool stays undeclared.
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	next, defs, fellBack := runMCPMount(a, messages, mcpMountResponsesAdditionalTools)
	if fellBack || len(defs) != 0 {
		t.Fatalf("incremental mount fallback = %v, defs = %#v", fellBack, defs)
	}
	if got := countMCPMounts(next); got != 1 {
		t.Fatalf("incremental mount count = %d, want 1", got)
	}
	decl := next[len(next)-1].MCPTools
	if len(decl) != 1 || decl[0].Name != "mcp_sample_fetch" {
		t.Fatalf("declared tools = %#v, want only mcp_sample_fetch", decl)
	}
}

func newMCPMountClient(t *testing.T, providerType string, compat *config.ProviderCompatConfig) *llm.Client {
	t.Helper()
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   providerType,
		APIURL: "https://example.invalid/v1",
		Compat: compat,
		Models: map[string]config.ModelConfig{"model-1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}},
	}, []string{"test-key"})
	return llm.NewClient(provider, nil, "model-1", 512, "")
}

func TestMCPMountFirstEmissionWithRequestTail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		tail  message.Message
		clean bool
	}{
		{"turn overlay", message.Message{Role: message.RoleUser, Content: "runtime hint", Kind: message.KindTurnOverlay}, true},
		{"thinking replay prefix", message.Message{Role: message.RoleAssistant, Content: "replayed thinking", Kind: message.KindThinkingReplayPrefix}, false},
		{"mailbox", message.Message{Role: message.RoleUser, Content: "worker report", Kind: message.KindSubAgentMailbox}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newTestMainAgent(t, t.TempDir())
			a.tools = tools.NewRegistry()
			sealEmptyMCPMountBaseline(a)
			a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

			mounted, _, fellBack := runMCPMount(a, []message.Message{
				{Role: message.RoleUser, Content: "first"},
				tc.tail,
			}, mcpMountKimiDynamic)
			if fellBack {
				t.Fatal("a transient tail must not fall back")
			}
			got := countMCPMounts(mounted)
			if tc.clean && got != 1 {
				t.Fatalf("durable tail mount count = %d, want 1", got)
			}
			if !tc.clean && got != 0 {
				t.Fatalf("transient tail mount count = %d, want 0 (deferred): %#v", got, mounted)
			}
			if !tc.clean && len(a.mcpMountState.snapshots) != 0 {
				t.Fatalf("deferred emission recorded %d snapshots, want none", len(a.mcpMountState.snapshots))
			}
		})
	}

	// An assistant prefill defers emission until its continuation completes.
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	mounted, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, Content: "working", Kind: message.KindThinkingReplayPrefix},
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("the deferred request must not fall back")
	}
	if got := countMCPMounts(mounted); got != 0 {
		t.Fatalf("deferred mount count = %d, want 0: %#v", got, mounted)
	}
	mounted, _, fellBack = runMCPMount(a, []message.Message{{Role: message.RoleUser, Content: "first"}}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("clean-tail emission must not fall back")
	}
	assertMCPMountAt(t, mounted, 1, "mcp_sample_lookup")
}

// TestMCPMountReplaysDuringPendingAnthropicNativeTurn locks the pending-turn
// rule: declarations already sent replay ahead of the pending turn, while a new
// declaration defers until the native turn completes with a fresh user turn.
func TestMCPMountReplaysDuringPendingAnthropicNativeTurn(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	first, _, fellBack := runMCPMount(a, []message.Message{{Role: message.RoleUser, Content: "first"}}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("initial emission must not fall back")
	}
	assertMCPMountAt(t, first, 1, "mcp_sample_lookup")

	pendingNative := message.Message{
		Role:    message.RoleAssistant,
		Content: "searching",
		NativeTools: &message.NativeToolHistory{
			Protocol: config.ProviderTypeMessages,
			Calls:    []message.HostedCall{{ID: "srv-1", Name: "web_search"}},
		},
	}
	replayed, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		pendingNative,
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("replay during a pending native turn must not fall back")
	}
	assertMCPMountAt(t, replayed, 1, "mcp_sample_lookup")

	// A tool enabled while the native turn is pending stays deferred.
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	deferred, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		pendingNative,
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("deferred emission must not fall back")
	}
	if got := countMCPMounts(deferred); got != 1 {
		t.Fatalf("pending-turn mount count = %d, want 1 (replay only): %#v", got, deferred)
	}
	if len(a.mcpMountState.snapshots) != 1 {
		t.Fatalf("pending turn recorded %d snapshots, want 1 (the sent declaration)", len(a.mcpMountState.snapshots))
	}

	// Once the native turn completes and the next user turn lands, the deferred
	// declaration anchors at the new clean tail.
	completed := pendingNative
	completed.NativeTools.Calls[0].Result = json.RawMessage(`"ok"`)
	emitted, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		completed,
		{Role: message.RoleUser, Content: "next"},
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("post-turn emission must not fall back")
	}
	if got := countMCPMounts(emitted); got != 2 {
		t.Fatalf("post-turn mount count = %d, want 2: %#v", got, emitted)
	}
	assertMCPMountAt(t, emitted, 1, "mcp_sample_lookup")
	assertMCPMountAt(t, emitted, len(emitted)-1, "mcp_sample_fetch")
}

// TestMCPMountRollsBackUnrespondedDeclaration locks the failed-emission path: a
// declaration anchored at a clean tail whose request never produced an
// assistant output rolls back instead of replaying ahead of later user input,
// then re-declares at the next clean tail.
func TestMCPMountRollsBackUnrespondedDeclaration(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	first, _, fellBack := runMCPMount(a, []message.Message{{Role: message.RoleUser, Content: "first"}}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("initial emission must not fall back")
	}
	assertMCPMountAt(t, first, 1, "mcp_sample_lookup")

	// The request behind the emission failed: the durable history only gained
	// the next user turn, so the unresponded declaration rolls back and
	// re-declares at the new tail instead of replaying ahead of it.
	redeclared, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleUser, Content: "second"},
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("rollback must not fall back")
	}
	if got := countMCPMounts(redeclared); got != 1 {
		t.Fatalf("re-declaration count = %d, want 1: %#v", got, redeclared)
	}
	assertMCPMountAt(t, redeclared, 2, "mcp_sample_lookup")

	// The model's response pins the re-declared anchor for later requests.
	pinned, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleUser, Content: "second"},
		{Role: message.RoleAssistant, Content: "done"},
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("pinned replay must not fall back")
	}
	if got := countMCPMounts(pinned); got != 1 {
		t.Fatalf("pinned mount count = %d, want 1: %#v", got, pinned)
	}
	assertMCPMountAt(t, pinned, 2, "mcp_sample_lookup")
}

// TestMCPMountAnthropicInlineModeLifecycle walks the inline-tools mount mode
// through first emission, response-pinned replay, and a later append, checking
// the declaration placement the Anthropic wire contract requires.
func TestMCPMountAnthropicInlineModeLifecycle(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.llmClient = newMCPMountClient(t, config.ProviderTypeMessages, &config.ProviderCompatConfig{
		Anthropic: &config.AnthropicCompatConfig{MCPInlineTools: new(true)},
	})
	if mode := a.mcpToolMountMode(); mode != mcpMountAnthropicInlineTools {
		t.Fatalf("mount mode = %d, want anthropic inline tools", mode)
	}
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	first, _, fellBack := runMCPMount(a, []message.Message{{Role: message.RoleUser, Content: "first"}}, mcpMountAnthropicInlineTools)
	if fellBack {
		t.Fatal("inline first emission must not fall back")
	}
	assertMCPMountAt(t, first, 1, "mcp_sample_lookup")

	// A response pins the declaration; the next request replays it in place.
	replayed, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, Content: "done"},
	}, mcpMountAnthropicInlineTools)
	if fellBack {
		t.Fatal("inline replay must not fall back")
	}
	if got := countMCPMounts(replayed); got != 1 {
		t.Fatalf("inline replay count = %d, want 1: %#v", got, replayed)
	}
	assertMCPMountAt(t, replayed, 1, "mcp_sample_lookup")

	// A second tool appends at the new clean tail; the group stays legal
	// (declaration after user input, followed by end of request).
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	appended, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, Content: "done"},
		{Role: message.RoleUser, Content: "second"},
	}, mcpMountAnthropicInlineTools)
	if fellBack {
		t.Fatal("inline append must not fall back")
	}
	if got := countMCPMounts(appended); got != 2 {
		t.Fatalf("inline append count = %d, want 2: %#v", got, appended)
	}
	assertMCPMountAt(t, appended, 1, "mcp_sample_lookup")
	assertMCPMountAt(t, appended, len(appended)-1, "mcp_sample_fetch")
}

// TestLoopDoneLateMountRetiresAfterLoopExit locks done's declaration lifecycle:
// the late mount anchors like any declaration, loop exit drops it from the
// effective set (folding targets stop advertising it), and the sent declaration
// stays replayed in history for prefix stability.
func TestLoopDoneLateMountRetiresAfterLoopExit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
done: allow
`)}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopDoneLateMount.Store(true)
	a.freezeToolSurfaceFromDefinitions(nil)

	first, _, fellBack := runMCPMount(a, []message.Message{{Role: message.RoleUser, Content: "start"}}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("done late mount must not fall back")
	}
	assertMCPMountAt(t, first, 1, tools.NameDone)
	if defs := a.effectiveRuntimeMCPDeclarations(); len(defs) != 1 || defs[0].Name != tools.NameDone {
		t.Fatalf("effective declarations = %#v, want done", defs)
	}

	// Loop exit retires done: the declaration stays anchored in history and
	// replays, but the effective set drops it.
	a.loopState.disable()
	if defs := a.effectiveRuntimeMCPDeclarations(); len(defs) != 0 {
		t.Fatalf("effective declarations after loop exit = %#v, want none", defs)
	}
	replayed, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "start"},
		{Role: message.RoleAssistant, Content: "working"},
		{Role: message.RoleUser, Content: "continue"},
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("retired done replay must not fall back")
	}
	assertMCPMountAt(t, replayed, 1, tools.NameDone)
}

// Inputs absorbed before any response can move pending declarations, but the
// resulting request must keep them behind all new user input and hints.
func TestBoundaryRebuildReevaluatesDeferredDeclaration(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	// The rebuild path re-derives the mount mode from the client snapshot, so
	// the test client must carry the same dynamic capability the state uses.
	a.llmClient = newMCPMountClient(t, config.ProviderTypeChatCompletions, &config.ProviderCompatConfig{
		ChatCompletions: &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(true)},
	})
	if mode := a.mcpToolMountMode(); mode != mcpMountKimiDynamic {
		t.Fatalf("mount mode = %d, want kimi dynamic", mode)
	}
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	// The in-flight request declared the first tool at its clean tail.
	inFlight, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("the in-flight request must not fall back")
	}
	assertMCPMountAt(t, inFlight, 1, "mcp_sample_lookup")

	// Mid-flight a second tool appears; the retry boundary absorbs the next
	// user turn (durable before the merge) and rebuilds the request, which
	// re-evaluates the pending declaration there.
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	merged := a.mergeBoundaryRequestInputs(inFlight, []message.Message{{Role: message.RoleUser, Content: "next"}}, 0)
	rebuilt, err := a.rebuildMainLLMRequestMessages(merged)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	snapshot := a.finishMCPBoundaryRequest(rebuilt)
	rebuilt = snapshot.Messages
	if got := countMCPMounts(rebuilt); got != 1 {
		t.Fatalf("rebuilt mount count = %d, want 1: %#v", got, rebuilt)
	}
	if len(snapshot.MCPDeclarations) != 2 || len(rebuilt[len(rebuilt)-1].MCPTools) != 2 {
		t.Fatalf("boundary snapshot = %#v", snapshot)
	}

	// Neither boundary declaration was answered (the request behind them is
	// still retrying), so the next build rolls both back and re-declares the
	// current effective set at its clean tail.
	replayed, _, fellBack := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleUser, Content: "next"},
	}, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("the rolled-back re-declaration must not fall back")
	}
	if got := countMCPMounts(replayed); got != 1 {
		t.Fatalf("re-declaration count = %d, want 1: %#v", got, replayed)
	}
	declared := replayed[len(replayed)-1].MCPTools
	names := make(map[string]bool, len(declared))
	for _, def := range declared {
		names[def.Name] = true
	}
	if !names["mcp_sample_lookup"] || !names["mcp_sample_fetch"] || len(declared) != 2 {
		t.Fatalf("re-declared tools = %#v, want lookup and fetch", declared)
	}

	pending := a.releasePendingMCPMounts(rebuilt)
	pending = append(pending, message.Message{Role: message.RoleUser, Content: "runtime hint", Kind: message.KindTurnOverlay})
	deferred := a.mergeBoundaryRequestInputs(pending, []message.Message{{Role: message.RoleUser, Content: "later"}}, 1)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_render", description: "render"})
	rebuiltOverlay, err := a.rebuildMainLLMRequestMessages(deferred)
	if err != nil {
		t.Fatalf("rebuild with overlay: %v", err)
	}
	rebuiltOverlay = a.finishMCPBoundaryRequest(rebuiltOverlay).Messages
	if got := countMCPMounts(rebuiltOverlay); got != 1 || len(rebuiltOverlay[len(rebuiltOverlay)-1].MCPTools) != 3 {
		t.Fatalf("rebuilt declaration behind the overlay = %#v", rebuiltOverlay)
	}
}
