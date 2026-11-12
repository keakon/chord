package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestMCPMountRetainsHintPrefixAcrossRequests(t *testing.T) {
	t.Parallel()
	for _, mode := range []mcpToolMountMode{mcpMountKimiDynamic, mcpMountResponsesAdditionalTools, mcpMountAnthropicInlineTools} {
		t.Run(strconv.Itoa(int(mode)), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.tools = tools.NewRegistry()
			sealEmptyMCPMountBaseline(a)
			a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
			a.subs.taskRecords["task-1"] = &DurableTaskRecord{TaskID: "task-1", State: string(SubAgentStateWaitingMain)}
			source := []message.Message{{Role: message.RoleUser, Content: "first"}}
			var previous []message.Message
			for attempt := range 3 {
				replayed, _, fallback, _ := a.mountRuntimeMCPTools(source, mode)
				if fallback {
					t.Fatal("unchanged source history must retain its anchors")
				}
				request, _ := applyTurnOverlayMessages(replayed, a.buildTurnOverlayMessages())
				request = a.mountPendingRuntimeMCPDeclarations(request, mode)
				if countMCPMounts(request) != attempt+1 {
					t.Fatalf("request %d: enabled tool was not declared: %#v", attempt, request)
				}
				if previous != nil && !reflect.DeepEqual(request[:len(previous)], previous) {
					t.Fatal("new request changed the prefix preceding the previous thinking")
				}
				if trailingTurnOverlayCount(request) != 0 {
					t.Fatal("captured hints must not be treated as a disposable suffix")
				}
				previous = request
				source = append(source, message.Message{
					Role: message.RoleAssistant, Content: "working",
					ThinkingBlocks: []message.ThinkingBlock{{Thinking: "plan", Signature: "signature"}},
				}, message.Message{Role: message.RoleUser, Content: "continue"})
				a.tools.Register(anchoredManualMCPTool{name: []string{"mcp_sample_fetch", "mcp_sample_render", "mcp_sample_list"}[attempt], description: "sample"})
			}
		})
	}
}

func TestMCPMountReminderExcludedFromBothAnchorProjections(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	a.cachedSessionReminderContent.Store(new("Runtime environment"))
	source := []message.Message{{Role: message.RoleUser, Content: "first"}, {Role: message.RoleUser, Content: "continue"}}
	replayed, _, _, _ := a.mountRuntimeMCPTools(source, mcpMountAnthropicInlineTools)
	first := a.mountPendingRuntimeMCPDeclarations(a.injectSessionContextReminder(replayed), mcpMountAnthropicInlineTools)
	if countMCPMounts(first) != 1 {
		t.Fatal("missing initial declaration")
	}
	source = append(source, message.Message{Role: message.RoleAssistant, Content: "working"})
	next, _, fallback, _ := a.mountRuntimeMCPTools(source, mcpMountAnthropicInlineTools)
	if fallback {
		t.Fatal("request-only reminder polluted the source anchor")
	}
	next = a.injectSessionContextReminder(next)
	if !reflect.DeepEqual(first, next[:len(first)]) {
		t.Fatal("reminder or declaration moved across requests")
	}
}

func TestMCPBoundaryReleasesPendingHintsAndDefinitions(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.llmClient = newMCPMountClient(t, config.ProviderTypeMessages, &config.ProviderCompatConfig{
		Anthropic: &config.AnthropicCompatConfig{MCPInlineTools: new(true)},
	})
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	first, _, _ := runMCPMount(a, []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleUser, Content: "runtime hint", Kind: message.KindTurnOverlay},
	}, mcpMountAnthropicInlineTools)
	pending := a.releasePendingMCPMounts(first)
	merged := a.mergeBoundaryRequestInputs(pending, []message.Message{{Role: message.RoleUser, Content: "next"}}, trailingTurnOverlayCount(pending))
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	rebuilt, err := a.rebuildMainLLMRequestMessages(merged)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := a.finishMCPBoundaryRequest(rebuilt)
	msgs := snapshot.Messages
	if len(msgs) != 4 || msgs[1].Content != "next" || msgs[2].Content != "runtime hint" || len(msgs[3].MCPTools) != 2 {
		t.Fatalf("boundary request = %#v", msgs)
	}
	if len(snapshot.MCPDeclarations) != 2 {
		t.Fatalf("boundary tools = %#v", snapshot.MCPDeclarations)
	}
	again := a.finishMCPBoundaryRequest(msgs)
	if !reflect.DeepEqual(snapshot, again) {
		t.Fatal("retry without new input duplicated or lost pending hints")
	}
}

func TestMCPMountReplaysWithPermissionCheckedFileSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("initial notes")
	a := newTestMainAgent(t, root)
	enableTestCheckpointFileReplay(a)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 120000, 0)
	a.tools = tools.NewRegistry()
	sealEmptyMCPMountBaseline(a)
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	source := []message.Message{
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Files and Evidence\n- notes.md\n"},
		{Role: message.RoleUser, Content: "continue"},
	}
	project := func() ([]message.Message, int) {
		t.Helper()
		mounted, _, fallback, positions := a.mountRuntimeMCPTools(source, mcpMountAnthropicInlineTools)
		if fallback {
			t.Fatal("file snapshots changed a declaration anchor")
		}
		request, _ := injectCompactionFileContextForTest(a, mounted)
		count := 0
		for _, pos := range positions {
			if pos < len(source) {
				count++
			}
		}
		count += compactionFileContextPrefixCount(request, len(source)+count)
		request = append(request, message.Message{Role: message.RoleUser, Content: "runtime hint", Kind: message.KindTurnOverlay})
		return a.mountPendingRuntimeMCPDeclarations(request, mcpMountAnthropicInlineTools), count
	}
	first, _ := project()
	source = append(source, message.Message{Role: message.RoleAssistant, Content: "working"}, message.Message{Role: message.RoleUser, Content: "next"})
	write("updated notes")
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_fetch", description: "fetch"})
	second, _ := project()
	if !reflect.DeepEqual(first, second[:len(first)]) {
		t.Fatal("file update changed the prefix before the first response")
	}
	source = append(source, message.Message{Role: message.RoleAssistant, Content: "working again"}, message.Message{Role: message.RoleUser, Content: "continue again"})
	third, count := project()
	if !reflect.DeepEqual(second, third[:len(second)]) {
		t.Fatal("file snapshot moved across a declaration during replay")
	}
	if boundary := len(source) - 1 + count; third[boundary].Content != "continue again" {
		t.Fatalf("stable cache boundary = %d (%q), want last durable input", boundary, third[boundary].Content)
	}
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}
	revoked, _ := project()
	for _, msg := range revoked {
		if isCompactionFileSnapshot(msg) {
			t.Fatal("declaration snapshot retained a file after read permission was revoked")
		}
	}
}
