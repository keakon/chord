package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// yoloPipelineForRuleset builds the execution gate exactly as MainAgent wires
// it under YOLO: unprotected tools bypass permission entirely, protected ones
// are evaluated against the YOLO-filtered ruleset, and ask decisions relax to
// an implicit allow for every tool except done and compact_context.
func yoloPipelineForRuleset(base permission.Ruleset, loopEnabled bool) toolExecutionPipeline {
	filtered := yoloRuleset(base)
	return toolExecutionPipeline{
		registry:           tools.NewRegistry(),
		currentRuleset:     func() permission.Ruleset { return filtered },
		bypassPermission:   func(name string) bool { return !yoloProtectedPermissionTool(name) },
		yoloDowngradeAsk:   func(name string) bool { return yoloAskDowngradeTool(name) },
		loopExitAuthorized: func() bool { return loopEnabled },
	}
}

// yoloOffPipeline builds the YOLO-off baseline for the same ruleset: the full
// user ruleset is evaluated with no bypass and no ask downgrade.
func yoloOffPipeline(base permission.Ruleset) toolExecutionPipeline {
	return toolExecutionPipeline{currentRuleset: func() permission.Ruleset { return base }}
}

func permissionErrFor(t *testing.T, p toolExecutionPipeline, name string) error {
	t.Helper()
	call := message.ToolCall{Name: name, Args: json.RawMessage(`{}`)}
	return p.applyPermission(context.Background(), &call, &ToolExecutionResult{})
}

// An allowlist role names no protected tool, so its wildcard default is the
// only thing deciding delegate/handoff/cancel. yoloRuleset mirrors that
// default instead of dropping it, so switching YOLO on keeps the denial (and
// never tightens the allowlist's own configured behavior).
func TestYoloAllowlistRoleKeepsCapabilityToolDefaults(t *testing.T) {
	base := permissionRuleset(t, `
"*": deny
read: allow
grep: allow
`)
	on := yoloPipelineForRuleset(base, false)
	off := yoloOffPipeline(base)
	for _, name := range []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel} {
		if err := permissionErrFor(t, on, name); err == nil {
			t.Fatalf("YOLO must not grant %q to an allowlist whose wildcard deny covers it", name)
		}
		if err := permissionErrFor(t, off, name); err == nil {
			t.Fatalf("baseline: %q must be denied with YOLO off too", name)
		}
	}
	// Ordinary tools are still bypassed: that is what YOLO is for.
	for _, name := range []string{tools.NameShell, tools.NameWrite} {
		if err := permissionErrFor(t, on, name); err != nil {
			t.Fatalf("YOLO must still bypass %q, got %v", name, err)
		}
	}
}

// Regression (new adjudication): YOLO may only widen permissions, never take a
// usable mechanism tool away. A role whose wildcard default allows delegation
// (`"*": allow`) could delegate with YOLO off and was denied the moment YOLO
// switched on because the old YOLO filter seeded an unconditional deny instead
// of mirroring the user's allow default.
func TestYoloWildcardAllowKeepsMechanismToolsUsable(t *testing.T) {
	base := permissionRuleset(t, `"*": allow`)
	on := yoloPipelineForRuleset(base, false)
	off := yoloOffPipeline(base)
	for _, name := range []string{tools.NameDelegate, tools.NameHandoff, tools.NameCancel} {
		if err := permissionErrFor(t, on, name); err != nil {
			t.Fatalf("YOLO must keep %q usable when its wildcard default allows it, got %v", name, err)
		}
		if err := permissionErrFor(t, off, name); err != nil {
			t.Fatalf("baseline without YOLO must allow %q under `*: allow`, got %v", name, err)
		}
	}
}

// Naming a mechanism tool directly is how a role opts it in; the other
// capability-granting tools keep whatever default the ruleset gives them.
func TestYoloExplicitAllowGrantsCapabilityTool(t *testing.T) {
	base := permissionRuleset(t, `
"*": deny
delegate: allow
`)
	on := yoloPipelineForRuleset(base, false)
	off := yoloOffPipeline(base)
	if err := permissionErrFor(t, on, tools.NameDelegate); err != nil {
		t.Fatalf("an explicit delegate allow must survive YOLO, got %v", err)
	}
	if err := permissionErrFor(t, off, tools.NameDelegate); err != nil {
		t.Fatalf("baseline: explicit delegate allow must work with YOLO off, got %v", err)
	}
	if err := permissionErrFor(t, on, tools.NameHandoff); err == nil {
		t.Fatal("allowing delegate must not also grant handoff")
	}
	if err := permissionErrFor(t, on, tools.NameCancel); err == nil {
		t.Fatal("allowing delegate must not also grant cancel")
	}
}

// A mechanism deny rule keeps rejecting under YOLO exactly as without it.
func TestYoloMechanismDenyStaysDenied(t *testing.T) {
	base := permissionRuleset(t, `
"*": allow
delegate: deny
`)
	on := yoloPipelineForRuleset(base, false)
	off := yoloOffPipeline(base)
	if err := permissionErrFor(t, on, tools.NameDelegate); err == nil {
		t.Fatal("YOLO must not lift an explicit mechanism deny")
	}
	if err := permissionErrFor(t, off, tools.NameDelegate); err == nil {
		t.Fatal("baseline without YOLO must deny delegate too")
	}
}

// Regression (new adjudication): under YOLO a mechanism ask no longer raises
// the shared confirmation dialog; turning YOLO off restores it.
func TestYoloMechanismAskRelaxesUnderYoloAndRestores(t *testing.T) {
	base := permissionRuleset(t, `delegate: ask`)
	var confirmCalls atomic.Int32
	on := yoloPipelineForRuleset(base, false)
	on.confirm = subYoloConfirmStub(&confirmCalls)
	if err := permissionErrFor(t, on, tools.NameDelegate); err != nil {
		t.Fatalf("YOLO must relax a mechanism ask to allow, got %v", err)
	}
	if got := confirmCalls.Load(); got != 0 {
		t.Fatalf("confirm calls under YOLO = %d, want 0", got)
	}
	off := toolExecutionPipeline{currentRuleset: func() permission.Ruleset { return base }, confirm: subYoloConfirmStub(&confirmCalls)}
	if err := permissionErrFor(t, off, tools.NameDelegate); err == nil {
		t.Fatal("mechanism ask must confirm when YOLO is off")
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls with YOLO off = %d, want 1", got)
	}
}

// done and compact_context only end or shrink the current unit of work, and
// the runtime modes that mount them are the authorization, so YOLO must keep
// them reachable under a wildcard deny and must not seed a denial for them.
func TestYoloKeepsLoopExitAndCompactionUsable(t *testing.T) {
	p := yoloPipelineForRuleset(permissionRuleset(t, `
"*": deny
read: allow
`), true)
	if err := permissionErrFor(t, p, tools.NameDone); err != nil {
		t.Fatalf("YOLO must keep loop exit reachable, got %v", err)
	}
	if err := permissionErrFor(t, p, tools.NameCompactContext); err != nil {
		t.Fatalf("YOLO must keep model-driven compaction reachable, got %v", err)
	}
}

// A rule naming done still wins, under YOLO as everywhere else.
func TestYoloHonorsExplicitDoneDeny(t *testing.T) {
	p := yoloPipelineForRuleset(permissionRuleset(t, `
"*": deny
done: deny
`), true)
	if err := permissionErrFor(t, p, tools.NameDone); err == nil {
		t.Fatal("an explicit done deny must survive YOLO")
	}
}

// With no permission configuration at all there is nothing to enforce, so YOLO
// must not invent restrictions that do not exist without it.
func TestYoloUnconfiguredPermissionsStayUnrestricted(t *testing.T) {
	p := yoloPipelineForRuleset(nil, false)
	for _, name := range []string{tools.NameDelegate, tools.NameHandoff, tools.NameShell} {
		if err := permissionErrFor(t, p, name); err != nil {
			t.Fatalf("unconfigured permissions must stay unrestricted for %q, got %v", name, err)
		}
	}
}

// TestYoloVisibleRulesetMatchesExecutionGate locks the visible/execution
// consistency requirement: the delegate agent-type surface and the execution
// gate both derive from the same yoloRuleset output, so what the LLM is shown
// as available is exactly what executes. The delegate availability check
// mirrors applyPermission's empty-ruleset and deny semantics.
func TestYoloVisibleRulesetMatchesExecutionGate(t *testing.T) {
	for _, src := range []string{
		`"*": deny`,
		`"*": allow`,
		`"*": deny
delegate: allow`,
		`delegate:
  reviewer: allow
  tester: deny`,
	} {
		base := permissionRuleset(t, src)
		filtered := yoloRuleset(base)
		gate := yoloPipelineForRuleset(base, false)
		for _, agentType := range []string{"builder", "reviewer", "tester"} {
			visible := delegateAgentAvailable(filtered, agentType)
			args, err := json.Marshal(map[string]string{"agent_type": agentType})
			if err != nil {
				t.Fatal(err)
			}
			call := message.ToolCall{Name: tools.NameDelegate, Args: args}
			gateErr := gate.applyPermission(context.Background(), &call, &ToolExecutionResult{})
			if visible == (gateErr != nil) {
				t.Fatalf("ruleset %q: delegate(%s) visible=%v but gate err=%v — visible surface and execution disagree", src, agentType, visible, gateErr)
			}
		}
	}
}
