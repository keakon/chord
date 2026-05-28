package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/tools"
)

func TestYoloRulesetKeepsProtectedRulesAndDropsOthers(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: tools.NameShell, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameHandoff, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionAsk},
		{Permission: tools.NameCancel, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameDone, Pattern: "*", Action: permission.ActionAllow},
	}
	filtered := yoloRuleset(ruleset)

	for _, rule := range filtered {
		if !yoloProtectedPermissionTool(rule.Permission) {
			t.Fatalf("non-protected rule %+v should be filtered in YOLO ruleset: %+v", rule, filtered)
		}
	}

	// Default deny: Shell allow rule was filtered out, so Shell evaluates to deny.
	if got := evaluateToolPermission(filtered, tools.NameShell, json.RawMessage(`{"command":"rm -rf /"}`)); got.Action != permission.ActionDeny {
		t.Fatalf("Shell action under YOLO = %v, want deny (allow rule should be filtered)", got.Action)
	}
	// Protected rules survive with their original action (Handoff allow, Delegate ask, Cancel deny, Done allow).
	if got := evaluateToolPermission(filtered, tools.NameHandoff, json.RawMessage(`{"agent":"planner"}`)); got.Action != permission.ActionAllow {
		t.Fatalf("Handoff action = %v, want allow", got.Action)
	}
	if got := evaluateToolPermission(filtered, tools.NameDelegate, json.RawMessage(`{"agent":"builder"}`)); got.Action != permission.ActionAsk {
		t.Fatalf("Delegate action = %v, want ask", got.Action)
	}
	if got := evaluateToolPermission(filtered, tools.NameCancel, json.RawMessage(`{}`)); got.Action != permission.ActionDeny {
		t.Fatalf("Cancel action = %v, want deny", got.Action)
	}
	if got := evaluateToolPermission(filtered, tools.NameDone, json.RawMessage(`{"report":"done"}`)); got.Action != permission.ActionAllow {
		t.Fatalf("Done action = %v, want allow", got.Action)
	}
}

func TestYoloRulesetEmptyInputReturnsNil(t *testing.T) {
	if got := yoloRuleset(nil); got != nil {
		t.Fatalf("yoloRuleset(nil) = %v, want nil", got)
	}
	if got := yoloRuleset(permission.Ruleset{}); got != nil {
		t.Fatalf("yoloRuleset(empty) = %v, want nil", got)
	}
}

func TestYoloBypassesOnlyUnprotectedToolPermissions(t *testing.T) {
	pipeline := toolExecutionPipeline{
		registry: tools.NewRegistry(),
		currentRuleset: func() permission.Ruleset {
			return permission.Ruleset{
				{Permission: tools.NameShell, Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameHandoff, Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameCancel, Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameDone, Pattern: "*", Action: permission.ActionDeny},
			}
		},
		bypassPermission: func(name string) bool {
			return !yoloProtectedPermissionTool(name)
		},
	}

	for _, toolName := range []string{tools.NameShell, tools.NameWrite, tools.NameRead} {
		t.Run(toolName+" bypassed", func(t *testing.T) {
			call := message.ToolCall{Name: toolName, Args: json.RawMessage(`{}`)}
			if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err != nil {
				t.Fatalf("applyPermission(%s) err = %v, want nil", toolName, err)
			}
		})
	}

	for _, toolName := range []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel, tools.NameDone} {
		t.Run(toolName+" protected", func(t *testing.T) {
			call := message.ToolCall{Name: toolName, Args: json.RawMessage(`{}`)}
			err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{})
			if err == nil || !strings.Contains(err.Error(), "denied by permission policy") {
				t.Fatalf("applyPermission(%s) err = %v, want permission denied", toolName, err)
			}
		})
	}
}

func TestYoloBusyToggleDefersPromptAndToolSurfaceUntilNextRequest(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.tools.Register(tools.GlobTool{})
	a.ruleset = permission.Ruleset{{Permission: tools.NameGlob, Pattern: "*", Action: permission.ActionDeny}}
	a.sessionBuilt.Store(true)
	a.freezeToolSurface()
	beforePrompt := a.installedSysPrompt
	if _, ok := a.tools.Get(tools.NameGlob); !ok {
		t.Fatal("expected Glob registered")
	}
	if len(a.mainLLMToolDefinitions()) != 0 {
		t.Fatal("initial frozen surface should hide Glob under deny rule")
	}

	a.handleYoloCommand("/yolo on", true)

	if !a.YoloEnabled() {
		t.Fatal("YOLO should enable while busy")
	}
	decision := evaluateToolPermission(a.effectiveRuleset(), tools.NameGlob, json.RawMessage(`{"pattern":"*"}`))
	if decision.Action != permission.ActionDeny {
		t.Fatalf("effective Glob action after YOLO = %v, want deny via empty YOLO ruleset", decision.Action)
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed immediately after busy YOLO toggle")
	}
	if frozen := a.frozenToolDefs.Load(); frozen != nil && len(*frozen) != 0 {
		t.Fatalf("frozen tool surface changed immediately: %#v", *frozen)
	}
	if a.sessionBuilt.Load() {
		t.Fatal("sessionBuilt should be reset so next request rebuilds context")
	}

	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt: %v", err)
	}
	defs := a.mainLLMToolDefinitions()
	if got := len(defs); got != 1 {
		t.Fatalf("rebuilt tool definitions count = %d, want 1", got)
	}
	if got := defs[0].Name; got != tools.NameGlob {
		t.Fatalf("rebuilt tool = %q, want %q", got, tools.NameGlob)
	}
}

func TestYoloToggleReturningToSameStateKeepsFrozenContext(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.tools.Register(tools.GlobTool{})

	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("initial ensureSessionBuilt: %v", err)
	}
	beforePrompt := a.installedSysPrompt
	beforeReminder := a.cachedSessionReminderContent.Load()
	beforeDefs := a.frozenToolDefs.Load()
	if beforeReminder == nil || beforeDefs == nil {
		t.Fatal("initial context surface should be frozen")
	}

	a.handleYoloCommand("/yolo on", true)
	a.handleYoloCommand("/yolo off", true)
	if a.YoloEnabled() {
		t.Fatal("YOLO should be off after toggling back")
	}
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt after unchanged YOLO surface: %v", err)
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed after YOLO returned to original state: %q", got)
	}
	if got := a.cachedSessionReminderContent.Load(); got != beforeReminder {
		t.Fatalf("session reminder pointer changed after YOLO returned to original state")
	}
	if got := a.frozenToolDefs.Load(); got != beforeDefs {
		t.Fatalf("frozen tool surface pointer changed after YOLO returned to original state")
	}
	if a.surfaceDirty.Load() {
		t.Fatal("surface dirty flag should clear after unchanged surface comparison")
	}
}

func TestYoloLowQuotaCodexKeepsPromptAndToolSurfaceFrozen(t *testing.T) {
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
	a.ruleset = permission.Ruleset{{Permission: tools.NameGlob, Pattern: "*", Action: permission.ActionDeny}}
	a.sessionBuilt.Store(true)
	a.freezeToolSurface()
	beforePrompt := a.installedSysPrompt

	a.handleYoloCommand("/yolo on", true)
	if !a.YoloEnabled() {
		t.Fatal("YOLO should enable while busy")
	}
	decision := evaluateToolPermission(a.effectiveRuleset(), tools.NameGlob, json.RawMessage(`{"pattern":"*"}`))
	if decision.Action != permission.ActionDeny {
		t.Fatalf("effective Glob action after YOLO = %v, want deny via empty YOLO ruleset", decision.Action)
	}
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt: %v", err)
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed under low-quota codex")
	}
	if defs := a.mainLLMToolDefinitions(); len(defs) != 0 {
		t.Fatalf("tool surface changed under low-quota codex: %#v", defs)
	}
}
