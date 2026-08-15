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
		readyGen: a.ResetMCPReady(),
		req:      MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual"}},
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
		readyGen: a.ResetMCPReady(),
		req:      MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual"}},
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
		readyGen: a.ResetMCPReady(),
		req:      MCPControlRequest{Action: MCPControlDisable, Servers: []string{"manual"}},
		err:      errors.New("control failed"),
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

func TestMCPControlPartialSuccessAppliesSurfaceAndReportsError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.tools.Register(tools.GlobTool{})
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("initial ensureSessionBuilt: %v", err)
	}

	a.handleMCPControlDoneEvent(Event{Payload: mcpControlDonePayload{
		readyGen: a.ResetMCPReady(),
		req:      MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual", "missing"}},
		result: MCPControlResult{
			Tools:       []tools.Tool{tools.ReadTool{}},
			PromptBlock: "MCP updated prompt",
			Enabled:     []string{"manual"},
		},
		err: fmt.Errorf("unknown MCP server %q", "missing"),
	}})
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt after partial success: %v", err)
	}
	if _, ok := a.tools.Get(tools.NameRead); !ok {
		t.Fatal("successful MCP subset was not applied")
	}
	if got := a.mcpServersPrompt; got != "MCP updated prompt" {
		t.Fatalf("MCP prompt = %q, want updated prompt", got)
	}
	waitForToastEvent(t, a.Events(), "MCP enabled: manual")
	waitForToastEvent(t, a.Events(), "unknown MCP server")
}

func TestMCPControlStaleGenerationReportsPreviousSession(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	stale := a.ResetMCPReady()
	_ = a.ResetMCPReady()
	a.mcpTransitionActive.Store(true)

	a.handleMCPControlDoneEvent(Event{Payload: mcpControlDonePayload{
		readyGen: stale,
		req:      MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual-search"}},
		result:   MCPControlResult{Enabled: []string{"manual-search"}},
	}})

	toast := waitForToastEvent(t, a.Events(), "previous session")
	if toast.Message != "MCP enabled for the previous session: manual-search" {
		t.Fatalf("toast = %q", toast.Message)
	}
}

func TestMCPControlDetectsTopLevelBaselineChangesInDynamicMode(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools = tools.NewRegistry()
	a.llmClient = newResponsesAdditionalToolsClient("model-1")
	a.tools.Register(anchoredManualMCPTool{name: "mcp_sample_lookup", description: "lookup"})
	_ = a.stableVisibleLLMTools()

	if !a.mcpControlChangesTopLevelSurface(MCPControlResult{Disabled: []string{"sample"}}) {
		t.Fatal("disabling a server represented in the top-level baseline must report a cache-affecting change")
	}
	if a.mcpControlChangesTopLevelSurface(MCPControlResult{Enabled: []string{"late-server"}}) {
		t.Fatal("enabling a server outside the top-level baseline should stay on the dynamic mount")
	}

	a.forceFullMCPToolInjection()
	if !a.mcpControlChangesTopLevelSurface(MCPControlResult{Enabled: []string{"late-server"}}) {
		t.Fatal("forced full injection must report every MCP control change as cache-affecting")
	}
}

func TestRuntimeMCPDiscoveryGenerationCannotOverwriteNewerRestore(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	stale := a.ResetMCPReady()
	current := a.ResetMCPReady()
	if a.SetRuntimeMCPDiscoveryForGeneration(stale, []tools.Tool{tools.ReadTool{}}, "stale") {
		t.Fatal("stale MCP discovery unexpectedly applied")
	}
	select {
	case <-stale.ready:
	default:
		t.Fatal("stale readiness waiter was not released")
	}
	if !a.SetRuntimeMCPDiscoveryForGeneration(current, []tools.Tool{tools.GlobTool{}}, "current") {
		t.Fatal("current MCP discovery was rejected")
	}
	if got := a.mcpServersPrompt; got != "current" {
		t.Fatalf("MCP prompt = %q, want current", got)
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
		readyGen: a.ResetMCPReady(),
		req:      MCPControlRequest{Action: MCPControlEnable, Servers: []string{"manual"}},
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

func TestMCPStatusTextKeepsAutomaticServerStates(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.mcpServerListFn = func() []MCPServerDisplay {
		return []MCPServerDisplay{
			// Automatic servers have no manual intent: Enabled is always true
			// for them, which must not suppress pending/error display.
			{Name: "auto-pending", Pending: true, Enabled: true},
			{Name: "auto-error", Enabled: true, Err: "connect refused"},
			{Name: "manual-wanted", Manual: true, Enabled: true},
			{Name: "manual-off", Manual: true, Disabled: true},
			{Name: "auto-ok", OK: true, Enabled: true},
		}
	}

	got := a.mcpStatusText()
	for _, want := range []string{
		"- auto-pending: pending",
		"- auto-error: error (connect refused)",
		"- manual-wanted: enabled (unavailable)",
		"- manual-off: disabled",
		"- auto-ok: enabled",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status text missing %q:\n%s", want, got)
		}
	}
}

func TestMCPControlDoneAfterSessionSwitchDoesNotTouchNewBarrier(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	controlGen := a.ResetMCPReady()
	// A session switch (or MCP startup) replaces the readiness barrier while
	// the control is still running.
	newGen := a.ResetMCPReady()
	a.mcpTransitionActive.Store(true)

	a.handleMCPControlDoneEvent(Event{Type: EventMCPControlDone, Payload: mcpControlDonePayload{
		req:      MCPControlRequest{Action: MCPControlEnable, Servers: []string{"srv"}},
		result:   MCPControlResult{PromptBlock: "stale block", Enabled: []string{"srv"}},
		readyGen: controlGen,
	}})

	if a.mcpTransitionActive.Load() {
		t.Fatal("stale control done must clear the transition flag")
	}
	select {
	case <-controlGen.ready:
	default:
		t.Fatal("superseded generation channel must be closed so its waiters cannot leak")
	}
	select {
	case <-newGen.ready:
		t.Fatal("stale control done must not release the new session's barrier")
	default:
	}
	a.mcpServersPromptMu.Lock()
	pendingReplace := a.pendingMCPReplace
	runtimePrompt := a.mcpRuntimePrompt
	a.mcpServersPromptMu.Unlock()
	if pendingReplace {
		t.Fatal("stale control done must not stage tools onto the new session's surface")
	}
	if runtimePrompt == "stale block" {
		t.Fatal("stale control done must not overwrite the runtime MCP prompt")
	}
}

func TestMCPControlDoneCurrentGenerationAppliesAndReleasesBarrier(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	controlGen := a.ResetMCPReady()
	a.mcpTransitionActive.Store(true)

	a.handleMCPControlDoneEvent(Event{Type: EventMCPControlDone, Payload: mcpControlDonePayload{
		req:      MCPControlRequest{Action: MCPControlEnable, Servers: []string{"srv"}},
		result:   MCPControlResult{PromptBlock: "fresh block", Enabled: []string{"srv"}},
		readyGen: controlGen,
	}})

	select {
	case <-controlGen.ready:
	default:
		t.Fatal("current-generation control done must release its barrier")
	}
	a.mcpServersPromptMu.Lock()
	pendingReplace := a.pendingMCPReplace
	runtimePrompt := a.mcpRuntimePrompt
	a.mcpServersPromptMu.Unlock()
	if !pendingReplace || runtimePrompt != "fresh block" {
		t.Fatalf("current-generation control not staged: replace=%v prompt=%q", pendingReplace, runtimePrompt)
	}
}
