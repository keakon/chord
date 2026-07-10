package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/tools"
)

func TestSummarizeMCPControlErrorFlattensJoinedErrorsForToast(t *testing.T) {
	err := errors.Join(
		fmt.Errorf(`unknown MCP server %q`, "missing"),
		fmt.Errorf(`MCP server %q is not manual`, "auto-empty"),
		fmt.Errorf(`enable MCP %q: %w`, "manual-empty", fmt.Errorf("must specify either command or url")),
	)

	got := summarizeMCPControlError(err)
	if strings.Contains(got, "\n") {
		t.Fatalf("summary contains newline: %q", got)
	}
	for _, want := range []string{
		`unknown MCP server "missing"`,
		`MCP server "auto-empty" is not manual`,
		`enable MCP "manual-empty": must specify either command or url`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
	if !strings.Contains(got, "; ") {
		t.Fatalf("summary %q should use semicolon separators", got)
	}
}

func TestSummarizeMCPControlErrorCollapsesSingleErrorWhitespace(t *testing.T) {
	got := summarizeMCPControlError(fmt.Errorf("  first line\nsecond line\tthird line  "))
	if got != "first line second line third line" {
		t.Fatalf("summary = %q, want %q", got, "first line second line third line")
	}
}

func TestSummarizeMCPControlErrorIgnoresCanceledBranches(t *testing.T) {
	if got := summarizeMCPControlError(context.Canceled); got != "" {
		t.Fatalf("canceled summary = %q, want empty", got)
	}
	got := summarizeMCPControlError(errors.Join(context.Canceled, fmt.Errorf("other failure")))
	if got != "other failure" {
		t.Fatalf("mixed summary = %q, want %q", got, "other failure")
	}
}

func TestMCPControlEnableRejectsServerDeniedByActiveRole(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = &config.Config{MCP: config.MCPConfig{
		"exa": {AllowedTools: []string{"web_search_exa"}},
	}}
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}
	called := false
	a.SetMCPControlFunc(func(context.Context, MCPControlRequest) (MCPControlResult, error) {
		called = true
		return MCPControlResult{}, nil
	})

	a.handleMCPControlEvent(Event{Payload: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"exa"}}})
	if called {
		t.Fatal("MCP control function should not run for a server denied by the active role")
	}
	select {
	case raw := <-a.Events():
		event, ok := raw.(ErrorEvent)
		if !ok || !strings.Contains(event.Err.Error(), "denies all tools") {
			t.Fatalf("event = %#v, want active-role permission rejection", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for active-role permission rejection")
	}
}

func TestMCPControlEnableAcceptsExactAllowedLazyServer(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "mcp_exa_search", Pattern: "*", Action: permission.ActionAllow},
	}
	called := make(chan MCPControlRequest, 1)
	a.SetMCPControlFunc(func(_ context.Context, req MCPControlRequest) (MCPControlResult, error) {
		called <- req
		return MCPControlResult{}, nil
	})

	a.handleMCPControlEvent(Event{Payload: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"exa"}}})
	select {
	case req := <-called:
		if len(req.Servers) != 1 || req.Servers[0] != "exa" {
			t.Fatalf("MCP control request = %#v, want exa", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exact-allowed lazy MCP control")
	}
}

func TestMCPControlEnableRejectsShorterOverlappingLazyServer(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetMCPStatusFunc(func() []MCPServerDisplay {
		return []MCPServerDisplay{{Name: "search", Manual: true}, {Name: "search_api", Manual: true}}
	})
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "mcp_search_api_query", Pattern: "*", Action: permission.ActionAllow},
	}
	called := false
	a.SetMCPControlFunc(func(context.Context, MCPControlRequest) (MCPControlResult, error) {
		called = true
		return MCPControlResult{}, nil
	})

	a.handleMCPControlEvent(Event{Payload: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"search"}}})
	if called {
		t.Fatal("MCP control function should not run for the overlapping denied server")
	}
	select {
	case raw := <-a.Events():
		event, ok := raw.(ErrorEvent)
		if !ok || !strings.Contains(event.Err.Error(), "denies all tools") {
			t.Fatalf("event = %#v, want active-role permission rejection", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for overlapping server permission rejection")
	}
}

func TestMCPControlEnableAcceptsLongerOverlappingLazyServer(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetMCPStatusFunc(func() []MCPServerDisplay {
		return []MCPServerDisplay{{Name: "search", Manual: true}, {Name: "search_api", Manual: true}}
	})
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "mcp_search_api_query", Pattern: "*", Action: permission.ActionAllow},
	}
	called := make(chan MCPControlRequest, 1)
	a.SetMCPControlFunc(func(_ context.Context, req MCPControlRequest) (MCPControlResult, error) {
		called <- req
		return MCPControlResult{}, nil
	})

	a.handleMCPControlEvent(Event{Payload: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"search_api"}}})
	select {
	case req := <-called:
		if len(req.Servers) != 1 || req.Servers[0] != "search_api" {
			t.Fatalf("MCP control request = %#v, want search_api", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for longer overlapping MCP server control")
	}
}

func TestMCPControlWhileBusyDefersToolsAndPromptUntilNextRequest(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.tools.Register(tools.GlobTool{})
	a.SetMCPControlFunc(func(context.Context, MCPControlRequest) (MCPControlResult, error) {
		return MCPControlResult{
			Tools:       []tools.Tool{tools.ReadTool{}},
			PromptBlock: "MCP updated prompt",
		}, nil
	})
	a.sessionBuilt.Store(true)
	a.freezeToolSurface()
	a.newTurn()
	beforePrompt := a.installedSysPrompt

	a.handleMCPControlEvent(Event{Payload: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual"}}})
	if !a.mcpTransitionActive.Load() {
		t.Fatal("MCP transition should start while busy")
	}

	a.handleMCPControlDoneEvent(Event{Payload: mcpControlDonePayload{
		req: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual"}},
		result: MCPControlResult{
			Tools:       []tools.Tool{tools.ReadTool{}},
			PromptBlock: "MCP updated prompt",
		},
	}})

	if a.mcpTransitionActive.Load() {
		t.Fatal("MCP transition should clear after control done")
	}
	if _, ok := a.tools.Get(tools.NameGlob); !ok {
		t.Fatal("existing tool should remain before rebuild")
	}
	if _, ok := a.tools.Get(tools.NameRead); ok {
		t.Fatal("new MCP tool should not register before next request rebuild")
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed before next request: %q", got)
	}
	if a.sessionBuilt.Load() {
		t.Fatal("sessionBuilt should be reset after MCP control done")
	}
	if a.turn == nil {
		t.Fatal("MCP control done while busy should preserve the active turn")
	}

	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt: %v", err)
	}
	if _, ok := a.tools.Get(tools.NameRead); !ok {
		t.Fatal("new MCP tool should register during next request rebuild")
	}
	if !strings.Contains(a.installedSysPrompt, "MCP updated prompt") {
		t.Fatalf("rebuilt system prompt missing MCP block: %q", a.installedSysPrompt)
	}
}

func TestMCPControlReturningToSameSurfaceKeepsFrozenContext(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.mcpServersPrompt = "MCP original prompt"
	a.tools.Register(tools.ReadTool{})

	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("initial ensureSessionBuilt: %v", err)
	}
	beforePrompt := a.installedSysPrompt
	beforeReminder := a.cachedSessionReminderContent.Load()
	beforeDefs := a.frozenToolDefs.Load()
	if beforeReminder == nil || beforeDefs == nil {
		t.Fatal("initial context surface should be frozen")
	}

	a.handleMCPControlDoneEvent(Event{Payload: mcpControlDonePayload{
		req: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual"}},
		result: MCPControlResult{
			Tools:       []tools.Tool{tools.ReadTool{}},
			PromptBlock: "MCP original prompt",
		},
	}})
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt after unchanged MCP surface: %v", err)
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed for unchanged MCP surface: %q", got)
	}
	if got := a.cachedSessionReminderContent.Load(); got != beforeReminder {
		t.Fatalf("session reminder pointer changed for unchanged MCP surface")
	}
	if got := a.frozenToolDefs.Load(); got != beforeDefs {
		t.Fatalf("frozen tool surface pointer changed for unchanged MCP surface")
	}
	if a.surfaceDirty.Load() {
		t.Fatal("surface dirty flag should clear after unchanged surface comparison")
	}
}

func TestMCPControlErrorKeepsExistingSurface(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.mcpServersPrompt = "MCP original prompt"
	a.tools.Register(tools.ReadTool{})

	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("initial ensureSessionBuilt: %v", err)
	}
	beforePrompt := a.installedSysPrompt

	a.handleMCPControlDoneEvent(Event{Payload: mcpControlDonePayload{
		req: MCPControlRequest{Action: MCPControlDisable, Servers: []string{"manual"}},
		err: errors.New("control failed"),
	}})
	if _, ok := a.tools.Get(tools.NameRead); !ok {
		t.Fatal("existing MCP tool should remain registered after failed runtime control")
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed after failed runtime control: %q", got)
	}
	if !a.sessionBuilt.Load() {
		t.Fatal("sessionBuilt should remain valid after failed runtime control")
	}
	if a.surfaceDirty.Load() {
		t.Fatal("surfaceDirty should remain false after failed runtime control")
	}
	if a.mcpTransitionActive.Load() {
		t.Fatal("MCP transition should clear after failed runtime control")
	}
	toast := waitForToastEvent(t, a.Events(), "control failed")
	if toast.Level != "error" {
		t.Fatalf("toast level = %q, want error", toast.Level)
	}
}

func TestMCPLowQuotaCodexKeepsPromptAndToolSurfaceFrozen(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.projectConfig = &config.Config{Providers: map[string]config.ProviderConfig{"codex": {Preset: config.ProviderPresetCodex}}}
	a.providerModelRef = "codex/gpt-5.5"
	a.llmMu.Lock()
	a.runningModelRef = "codex/gpt-5.5"
	a.llmMu.Unlock()
	a.rateLimitSnaps = map[string]*ratelimit.KeyRateLimitSnapshot{"codex": {
		Primary: &ratelimit.RateLimitWindow{UsedPct: 95},
	}}
	a.tools.Register(tools.GlobTool{})
	a.sessionBuilt.Store(true)
	a.freezeToolSurface()
	a.newTurn()
	beforePrompt := a.installedSysPrompt

	a.handleMCPControlDoneEvent(Event{Payload: mcpControlDonePayload{
		req: MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual"}},
		result: MCPControlResult{
			Tools:       []tools.Tool{tools.ReadTool{}},
			PromptBlock: "MCP updated prompt",
		},
	}})
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt: %v", err)
	}
	if _, ok := a.tools.Get(tools.NameRead); !ok {
		t.Fatal("new MCP runtime tool should still register under low-quota codex")
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed under low-quota codex: %q", got)
	}
	defs := a.mainLLMToolDefinitions()
	if got := len(defs); got != 1 || defs[0].Name != tools.NameGlob {
		t.Fatalf("tool surface changed under low-quota codex loop: %#v", defs)
	}
}
