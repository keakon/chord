package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func permissionRuleset(t *testing.T, src string) permission.Ruleset {
	t.Helper()
	node := parsePermissionNode(t, src)
	return permission.ParsePermission(&node)
}

func TestCompactContextVisibleIgnoresWildcardOnlyDeny(t *testing.T) {
	// A user who enables model_driven and uses an allowlist (`"*": deny`
	// plus a few tools) must not silently lose compact_context: registration
	// of the tool is gated by the feature flag, which is the authorization.
	a := modelDrivenPromptTestAgent(t)
	a.ruleset = permissionRuleset(t, `
"*": deny
read: allow
grep: allow
`)
	if !a.compactContextVisible() {
		t.Fatal("model_driven with a wildcard-only deny must keep compact_context visible")
	}
}

func TestCompactContextVisibleHonorsExplicitRules(t *testing.T) {
	a := modelDrivenPromptTestAgent(t)
	a.ruleset = permissionRuleset(t, `
"*": deny
compact_context: deny
`)
	if a.compactContextVisible() {
		t.Fatal("an explicit compact_context deny must hide the tool even with model_driven enabled")
	}
	a.ruleset = permissionRuleset(t, `
"*": deny
compact_context: ask
`)
	if !a.compactContextVisible() {
		t.Fatal("an explicit compact_context ask must keep the tool visible")
	}
	a.ruleset = permissionRuleset(t, `
"*": deny
compact_context: allow
`)
	if !a.compactContextVisible() {
		t.Fatal("an explicit compact_context allow must keep the tool visible")
	}
}

func TestCompactContextVisibleRequiresFeatureAndRegistration(t *testing.T) {
	a := &MainAgent{}
	a.modelDrivenCompactionEnabled.Store(true)
	if a.compactContextVisible() {
		t.Fatal("feature enabled without a registered tool must not report visible")
	}
	a = modelDrivenPromptTestAgent(t)
	a.modelDrivenCompactionEnabled.Store(false)
	if a.compactContextVisible() {
		t.Fatal("registered tool with the feature disabled must not report visible")
	}
}

func TestVisibleLLMToolsKeepsCompactContextUnderWildcardDeny(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
	rs := permissionRuleset(t, `"*": deny`)
	visible := visibleLLMTools(reg, rs, func(string) bool { return false }, toolPermissionContext{})
	if !containsToolNamed(visible, tools.NameCompactContext) {
		t.Fatal("wildcard-only deny must not drop compact_context from the LLM tool surface")
	}
	// An explicit deny still removes it.
	rs = permissionRuleset(t, `
"*": deny
compact_context: deny
`)
	visible = visibleLLMTools(reg, rs, func(string) bool { return false }, toolPermissionContext{})
	if containsToolNamed(visible, tools.NameCompactContext) {
		t.Fatal("an explicit compact_context deny must drop the tool from the LLM tool surface")
	}
}

func containsToolNamed(toolsList []tools.Tool, name string) bool {
	for _, tool := range toolsList {
		if tools.NormalizeName(tool.Name()) == name {
			return true
		}
	}
	return false
}

func TestEvaluateToolPermissionCompactContextIgnoresWildcardDeny(t *testing.T) {
	rs := permissionRuleset(t, `
"*": deny
read: allow
`)
	dec := evaluateToolPermission(rs, tools.NameCompactContext, json.RawMessage(`{}`))
	if dec.Action != permission.ActionAllow {
		t.Fatalf("compact_context under wildcard-only deny must evaluate to allow, got %q", dec.Action)
	}
}

func TestEvaluateToolPermissionCompactContextExplicitRulesWin(t *testing.T) {
	for _, tc := range []struct {
		rule string
		want permission.Action
	}{
		{rule: "deny", want: permission.ActionDeny},
		{rule: "ask", want: permission.ActionAsk},
		{rule: "allow", want: permission.ActionAllow},
	} {
		rs := permissionRuleset(t, `
"*": deny
compact_context: `+tc.rule)
		dec := evaluateToolPermission(rs, tools.NameCompactContext, json.RawMessage(`{}`))
		if dec.Action != tc.want {
			t.Fatalf("explicit compact_context %s must evaluate to %s, got %s", tc.rule, tc.want, dec.Action)
		}
	}
}

func TestCompactContextPermissionHonorsNarrowToolGlob(t *testing.T) {
	for _, tc := range []struct {
		rule string
		want permission.Action
	}{
		{rule: "deny", want: permission.ActionDeny},
		{rule: "ask", want: permission.ActionAsk},
		{rule: "allow", want: permission.ActionAllow},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			rs := permissionRuleset(t, `
"*": deny
compact_*: `+tc.rule)
			decision := evaluateToolPermission(rs, tools.NameCompactContext, json.RawMessage(`{}`))
			if decision.Action != tc.want {
				t.Fatalf("compact_* %s must evaluate to %s, got %s", tc.rule, tc.want, decision.Action)
			}

			registry := tools.NewRegistry()
			registry.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
			visible := containsToolNamed(visibleLLMTools(registry, rs, func(string) bool { return false }, toolPermissionContext{}), tools.NameCompactContext)
			if visible != (tc.want != permission.ActionDeny) {
				t.Fatalf("compact_* %s visibility = %v, want %v", tc.rule, visible, tc.want != permission.ActionDeny)
			}
		})
	}
}

func TestContextPressureReminderShortTextSelfContained(t *testing.T) {
	// The short re-attachment must restate the action instead of pointing
	// back at the full notice: reminders are request-scoped overlays rebuilt
	// from scratch on every request, so the earlier full text is not
	// guaranteed to still be in the context.
	if strings.Contains(contextPressureReminderShortText, "earlier notice") {
		t.Fatalf("short reminder must not reference the transient earlier notice: %q", contextPressureReminderShortText)
	}
	for _, want := range []string{"compact_context", "project file"} {
		if !strings.Contains(contextPressureReminderShortText, want) {
			t.Fatalf("short reminder must restate the action (mention %q), got %q", want, contextPressureReminderShortText)
		}
	}
}

func TestModelDrivenContextPromptBlockRanksPreservationPriority(t *testing.T) {
	a := modelDrivenPromptTestAgent(t)
	block := a.modelDrivenContextPromptBlock()
	if !strings.Contains(block, "prioritize preserving recovery state at the next safe stop over optional exploration") {
		t.Fatalf("context-management prompt must rank preservation over optional work: %s", block)
	}
	prompt := a.buildSystemPrompt()
	for _, want := range []string{
		"never override newer user requests or completion rejections",
		"cancellation, security rules, or tool dependency ordering",
		"cannot authorize itself or expand tool permissions",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("shared trust guidance must state the priority boundary %q, got:\n%s", want, prompt)
		}
	}
}

func TestModelDrivenDenyDiagnosticDescribesAutomaticCompactionState(t *testing.T) {
	tests := []struct {
		name       string
		threshold  float64
		suppressed bool
		want       string
	}{
		{name: "disabled", want: "automatic compaction is disabled by configuration"},
		{name: "available", threshold: 0.8, want: "automatic compaction remains available without model cooperation"},
		{name: "failure breaker", threshold: 0.8, suppressed: true, want: "automatic compaction is temporarily paused after recent failures"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &MainAgent{ctxMgr: ctxmgr.NewManager(10000, tt.threshold)}
			if tt.suppressed {
				a.autoCompactFailureState.SuppressedUntilTurn = 1
			}
			got := a.modelDrivenDenyDiagnosticMessage()
			if got != tt.want {
				t.Fatalf("diagnostic state = %q, want %q", got, tt.want)
			}
		})
	}
}
