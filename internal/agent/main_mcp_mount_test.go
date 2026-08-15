package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

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
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	firstInput := []message.Message{{Role: message.RoleUser, Content: "first"}}
	first, fallback, fellBack := a.mountRuntimeMCPTools(firstInput, mcpMountKimiDynamic)
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
	second, fallback, fellBack := a.mountRuntimeMCPTools(secondInput, mcpMountKimiDynamic)
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
	disabled, _, fellBack := a.mountRuntimeMCPTools(secondInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("disable should retain the original declaration anchor")
	}
	assertMCPMountAt(t, disabled, 1, "mcp_sample_lookup")

	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	third, _, fellBack := a.mountRuntimeMCPTools(secondInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("new tool should append without moving the original anchor")
	}
	assertMCPMountAt(t, third, 1, "mcp_sample_lookup")
	assertMCPMountAt(t, third, len(third)-1, "mcp_sample_fetch")

	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "updated lookup"})
	fourth, _, fellBack := a.mountRuntimeMCPTools(secondInput, mcpMountKimiDynamic)
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
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})

	_, _, fellBack := a.mountRuntimeMCPTools(
		[]message.Message{{Role: message.RoleUser, Content: "first"}},
		mcpMountResponsesAdditionalTools,
	)
	if fellBack {
		t.Fatal("initial mount unexpectedly fell back")
	}

	messages, fallback, fellBack := a.mountRuntimeMCPTools(
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
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	firstInput := []message.Message{
		{Role: message.RoleUser, Content: "same"},
		{Role: message.RoleUser, Content: "same"},
	}
	first, _, fellBack := a.mountRuntimeMCPTools(firstInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("initial duplicate anchor unexpectedly fell back")
	}
	assertMCPMountAt(t, first, 2, "mcp_sample_lookup")

	nextInput := append(append([]message.Message(nil), firstInput...), message.Message{Role: message.RoleAssistant, Content: "done"})
	next, _, fellBack := a.mountRuntimeMCPTools(nextInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("duplicate anchor occurrence was not restored")
	}
	assertMCPMountAt(t, next, 2, "mcp_sample_lookup")
}

func TestRuntimeMCPMountIgnoresInjectedGitStatusInTextMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.waitGitStatus(context.Background())
	a.setCachedGitStatus("Git branch: main\n")

	firstInput := []message.Message{{Role: message.RoleUser, Content: "first"}}
	a.injectGitStatusIntoFirstUserMessage(firstInput)
	first, _, fellBack := a.mountRuntimeMCPTools(firstInput, mcpMountKimiDynamic)
	if fellBack {
		t.Fatal("initial text mount unexpectedly fell back")
	}
	assertMCPMountAt(t, first, 1, "mcp_sample_lookup")

	next, _, fellBack := a.mountRuntimeMCPTools(
		[]message.Message{{Role: message.RoleUser, Content: "first"}},
		mcpMountKimiDynamic,
	)
	if fellBack {
		t.Fatal("git status changed the text-message anchor")
	}
	assertMCPMountAt(t, next, 1, "mcp_sample_lookup")
}

func TestRuntimeMCPMountIgnoresInjectedGitStatusPart(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.waitGitStatus(context.Background())
	a.setCachedGitStatus("Git branch: main")
	original := []message.ContentPart{
		{Type: message.ContentPartText, Text: "first"},
		{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: "sample.png"},
	}

	firstInput := []message.Message{{Role: message.RoleUser, Parts: append([]message.ContentPart(nil), original...)}}
	a.injectGitStatusIntoFirstUserMessage(firstInput)
	first, _, fellBack := a.mountRuntimeMCPTools(firstInput, mcpMountResponsesAdditionalTools)
	if fellBack {
		t.Fatal("initial multipart mount unexpectedly fell back")
	}
	assertMCPMountAt(t, first, 1, "mcp_sample_lookup")

	next, _, fellBack := a.mountRuntimeMCPTools(
		[]message.Message{{Role: message.RoleUser, Parts: original}},
		mcpMountResponsesAdditionalTools,
	)
	if fellBack {
		t.Fatal("git status changed the multipart-message anchor")
	}
	assertMCPMountAt(t, next, 1, "mcp_sample_lookup")
}

func TestMCPMountIncrementalMessageShapesDetectNestedChange(t *testing.T) {
	state := &mcpToolMountState{}
	messages := []message.Message{{
		Role:      message.RoleAssistant,
		ToolCalls: []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup", Args: json.RawMessage(`{"value":"first"}`)}},
	}}
	first := append([]stableReductionMessageShape(nil), state.incrementalMessageShapes(messages, "")...)
	messages[0].ToolCalls = []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup", Args: json.RawMessage(`{"value":"second"}`)}}
	second := state.incrementalMessageShapes(messages, "")
	if first[0] == second[0] {
		t.Fatal("nested tool-call change reused the previous message shape")
	}
}

func TestRuntimeMCPMountResumeWithHistoricalCallUsesTopLevelTools(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	messages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "mcp_sample_lookup"}}},
	}

	got, fallback, fellBack := a.mountRuntimeMCPTools(messages, mcpMountKimiDynamic)
	if !fellBack || len(fallback) != 1 {
		t.Fatalf("resume fallback = %v, defs = %#v", fellBack, fallback)
	}
	if countMCPMounts(got) != 0 {
		t.Fatalf("resume fallback unexpectedly inserted a declaration: %#v", got)
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

func TestForceFullMCPToolInjectionDisablesDynamicMount(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.llmClient = newResponsesAdditionalToolsClient("custom-model")

	if got := a.mcpToolMountMode(); got != mcpMountResponsesAdditionalTools {
		t.Fatalf("mount mode before force = %v, want responses additional_tools", got)
	}

	a.forceFullMCPToolInjection()
	if got := a.mcpToolMountMode(); got != mcpMountFullInjection {
		t.Fatalf("mount mode after force = %v, want full injection", got)
	}

	// Mirror callLLM: the mount mode is always taken from mcpToolMountMode().
	got, fallback, fellBack := a.mountRuntimeMCPTools(
		[]message.Message{{Role: message.RoleUser, Content: "first"}},
		a.mcpToolMountMode(),
	)
	if fellBack || len(fallback) != 0 {
		t.Fatalf("forced mount returned fallback=%v defs=%#v", fellBack, fallback)
	}
	if countMCPMounts(got) != 0 {
		t.Fatalf("forced full injection should not emit dynamic mounts: %#v", got)
	}
}

func TestModelSwitchForcesFullMCPToolInjection(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.SetProviderModelRef("sample/model-a")

	a.swapLLMClientWithRef(newResponsesAdditionalToolsClient("model-b"), "model-b", 4096, "sample/model-b")

	if got := a.mcpToolMountMode(); got != mcpMountFullInjection {
		t.Fatalf("mount mode after model switch = %v, want full injection", got)
	}
}

func TestRuntimeMCPMountFallbackStopsReturningDefsOnceSurfaceRebuilt(t *testing.T) {
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
	_, fallback, fellBack := a.mountRuntimeMCPTools(messages, mcpMountResponsesAdditionalTools)
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
	_, fallback, fellBack = a.mountRuntimeMCPTools(messages, mcpMountResponsesAdditionalTools)
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

func TestSessionHeadResetRestoresDynamicMount(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.llmClient = newResponsesAdditionalToolsClient("custom-model")

	a.forceFullMCPToolInjection()
	if got := a.mcpToolMountMode(); got != mcpMountFullInjection {
		t.Fatalf("mount mode while forced = %v, want full injection", got)
	}

	// A session-head event starts a fresh run: an empty session has no
	// history to mis-anchor against, so dynamic mounts come back.
	a.resetSessionBuildState()
	if got := a.mcpToolMountMode(); got != mcpMountResponsesAdditionalTools {
		t.Fatalf("mount mode after fresh session head = %v, want responses additional_tools", got)
	}
}

func TestManualMCPEnabledBeforeFirstRequestRidesTopLevelTools(t *testing.T) {
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
	mounted, defs, fellBack := a.mountRuntimeMCPTools(messages, mcpMountResponsesAdditionalTools)
	if fellBack || len(defs) != 0 {
		t.Fatalf("baseline mount fallback = %v, defs = %#v", fellBack, defs)
	}
	if countMCPMounts(mounted) != 0 {
		t.Fatalf("baseline tools must not be declared incrementally: %#v", mounted)
	}

	// A server enabled after the first request uses the incremental
	// declaration channel; the baseline tool stays undeclared.
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	next, defs, fellBack := a.mountRuntimeMCPTools(messages, mcpMountResponsesAdditionalTools)
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
