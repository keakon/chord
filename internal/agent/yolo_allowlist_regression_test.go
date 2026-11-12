package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// yoloGateForRuleset builds the execution gate from the real MainAgent wiring
// with the mode forced to the requested state: the pipeline evaluates
// executionRuleset, so every gate test here exercises the same path
// production does.
func yoloGateForRuleset(t *testing.T, permissionYAML string, yolo bool, loopEnabled bool) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permissionRuleset(t, permissionYAML)
	a.yoloEnabled.Store(yolo)
	a.newTurn("")
	if loopEnabled {
		a.loopState.Enabled = true
	}
	return a
}

func permissionErrFor(t *testing.T, a *MainAgent, name string, args string) error {
	t.Helper()
	if args == "" {
		args = `{}`
	}
	pipeline := a.toolExecutionPipeline()
	call := message.ToolCall{Name: name, Args: json.RawMessage(args)}
	return pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{})
}

// Regression guard (kept from the old adjudication, in its new form): YOLO must
// never turn a usable tool into a denied one. An allowlist role's wildcard deny
// is the capability boundary, so everything it covers stays denied — and every
// explicit grant keeps working.
func TestYoloAllowlistRoleKeepsCapabilityBoundaries(t *testing.T) {
	src := `
"*": deny
read: allow
grep: allow
shell:
  "git *": allow
  "rm *": deny
`
	on := yoloGateForRuleset(t, src, true, false)
	off := yoloGateForRuleset(t, src, false, false)

	// The allowlist complement stays denied under YOLO: the wildcard deny is
	// the boundary, and the scoped deny inside the structure is its complement.
	for _, tc := range []struct {
		name string
		args string
	}{
		{tools.NameHandoff, `{}`},
		{tools.NameDelegate, `{"agent_type":"worker"}`},
		{tools.NameCancel, `{}`},
		{tools.NameShell, `{"command":"ls"}`},
		{tools.NameShell, `{"command":"rm -rf build"}`},
		{tools.NameWrite, `{"path":"notes.txt"}`},
	} {
		if err := permissionErrFor(t, on, tc.name, tc.args); err == nil {
			t.Fatalf("YOLO must not grant %s to an allowlist whose wildcard deny covers it", tc.name)
		}
		if err := permissionErrFor(t, off, tc.name, tc.args); err == nil {
			t.Fatalf("baseline: %s must be denied with YOLO off too", tc.name)
		}
	}
	// Explicit grants keep working: YOLO must not take a usable tool away.
	for _, tc := range []struct {
		name string
		args string
	}{
		{tools.NameRead, `{"path":"notes.txt"}`},
		{tools.NameGrep, `{"pattern":"TODO","paths":["docs"]}`},
		{tools.NameShell, `{"command":"git status --short"}`},
	} {
		if err := permissionErrFor(t, on, tc.name, tc.args); err != nil {
			t.Fatalf("YOLO must keep the granted %s usable, got %v", tc.name, err)
		}
		if err := permissionErrFor(t, off, tc.name, tc.args); err != nil {
			t.Fatalf("baseline: granted %s must work with YOLO off too, got %v", tc.name, err)
		}
	}
}

// Regression (kept): YOLO may only widen permissions, never take a usable
// mechanism tool away. A role whose wildcard default allows delegation
// (`"*": allow`) delegates with YOLO on and off alike.
func TestYoloWildcardAllowKeepsMechanismToolsUsable(t *testing.T) {
	on := yoloGateForRuleset(t, `"*": allow`, true, false)
	off := yoloGateForRuleset(t, `"*": allow`, false, false)
	for _, name := range []string{tools.NameDelegate, tools.NameHandoff, tools.NameCancel} {
		if err := permissionErrFor(t, on, name, `{"agent_type":"worker"}`); err != nil {
			t.Fatalf("YOLO must keep %q usable when its wildcard default allows it, got %v", name, err)
		}
		if err := permissionErrFor(t, off, name, `{"agent_type":"worker"}`); err != nil {
			t.Fatalf("baseline without YOLO must allow %q under `*: allow`, got %v", name, err)
		}
	}
}

// Naming a mechanism tool directly is how a role opts it in; the other
// capability-granting tools keep whatever default the ruleset gives them.
func TestYoloExplicitAllowGrantsCapabilityTool(t *testing.T) {
	src := `
"*": deny
delegate: allow
`
	on := yoloGateForRuleset(t, src, true, false)
	off := yoloGateForRuleset(t, src, false, false)
	if err := permissionErrFor(t, on, tools.NameDelegate, `{"agent_type":"worker"}`); err != nil {
		t.Fatalf("an explicit delegate allow must survive YOLO, got %v", err)
	}
	if err := permissionErrFor(t, off, tools.NameDelegate, `{"agent_type":"worker"}`); err != nil {
		t.Fatalf("baseline: explicit delegate allow must work with YOLO off, got %v", err)
	}
	if err := permissionErrFor(t, on, tools.NameHandoff, `{}`); err == nil {
		t.Fatal("allowing delegate must not also grant handoff")
	}
	if err := permissionErrFor(t, on, tools.NameCancel, `{}`); err == nil {
		t.Fatal("allowing delegate must not also grant cancel")
	}
}

// A mechanism wildcard deny keeps rejecting under YOLO exactly as without it.
func TestYoloMechanismDenyStaysDenied(t *testing.T) {
	src := `
"*": allow
delegate: deny
`
	on := yoloGateForRuleset(t, src, true, false)
	off := yoloGateForRuleset(t, src, false, false)
	if err := permissionErrFor(t, on, tools.NameDelegate, `{"agent_type":"worker"}`); err == nil {
		t.Fatal("YOLO must not lift an explicit mechanism deny")
	}
	if err := permissionErrFor(t, off, tools.NameDelegate, `{"agent_type":"worker"}`); err == nil {
		t.Fatal("baseline without YOLO must deny delegate too")
	}
}

// Regression (kept): under YOLO a mechanism ask no longer raises the shared
// confirmation dialog; turning YOLO off restores it.
func TestYoloMechanismAskRelaxesUnderYoloAndRestores(t *testing.T) {
	var confirmCalls atomic.Int32
	on := yoloGateForRuleset(t, `delegate: ask`, true, false)
	on.confirmFn = subYoloConfirmStub(&confirmCalls)
	if err := permissionErrFor(t, on, tools.NameDelegate, `{"agent_type":"worker"}`); err != nil {
		t.Fatalf("YOLO must relax a mechanism ask to allow, got %v", err)
	}
	if got := confirmCalls.Load(); got != 0 {
		t.Fatalf("confirm calls under YOLO = %d, want 0", got)
	}
	off := yoloGateForRuleset(t, `delegate: ask`, false, false)
	off.confirmFn = subYoloConfirmStub(&confirmCalls)
	if err := permissionErrFor(t, off, tools.NameDelegate, `{"agent_type":"worker"}`); err == nil {
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
	a := yoloGateForRuleset(t, `
"*": deny
read: allow
`, true, true)
	if err := permissionErrFor(t, a, tools.NameDone, `{"report":"done"}`); err != nil {
		t.Fatalf("YOLO must keep loop exit reachable, got %v", err)
	}
	if err := permissionErrFor(t, a, tools.NameCompactContext, `{}`); err != nil {
		t.Fatalf("YOLO must keep model-driven compaction reachable, got %v", err)
	}
}

// A rule naming done still wins, under YOLO as everywhere else.
func TestYoloHonorsExplicitDoneDeny(t *testing.T) {
	a := yoloGateForRuleset(t, `
"*": deny
done: deny
`, true, true)
	if err := permissionErrFor(t, a, tools.NameDone, `{"report":"done"}`); err == nil {
		t.Fatal("an explicit done deny must survive YOLO")
	}
}

// With no permission configuration at all there is nothing to enforce, so YOLO
// must not invent restrictions that do not exist without it.
func TestYoloUnconfiguredPermissionsStayUnrestricted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.yoloEnabled.Store(true)
	a.newTurn("")
	for _, name := range []string{tools.NameDelegate, tools.NameHandoff, tools.NameShell} {
		args := `{}`
		if name == tools.NameDelegate {
			args = `{"agent_type":"worker"}`
		}
		if err := permissionErrFor(t, a, name, args); err != nil {
			t.Fatalf("unconfigured permissions must stay unrestricted for %q, got %v", name, err)
		}
	}
}

// TestYoloDelegateCatalogMatchesExecutionGate locks the consistency contract
// in its new shape: the delegate agent-type surface derives from the configured
// ruleset (directory membership, YOLO-independent), and the execution gate —
// evaluating the YOLO-transformed ruleset — agrees with it, because scoped
// delegate denies stay bound under YOLO.
func TestYoloDelegateCatalogMatchesExecutionGate(t *testing.T) {
	for _, src := range []string{
		`"*": deny`,
		`"*": allow`,
		`"*": deny
delegate: allow`,
		`delegate:
  reviewer: allow
  tester: deny`,
		`"*": allow
delegate:
  reviewer: deny`,
	} {
		base := permissionRuleset(t, src)
		gate := yoloGateForRuleset(t, src, true, false)
		for _, agentType := range []string{"builder", "reviewer", "tester"} {
			visible := delegateAgentAvailable(base, agentType)
			args, err := json.Marshal(map[string]string{"agent_type": agentType})
			if err != nil {
				t.Fatal(err)
			}
			gateErr := permissionErrFor(t, gate, tools.NameDelegate, string(args))
			if visible != (gateErr == nil) {
				t.Fatalf("ruleset %q: delegate(%s) visible=%v but gate err=%v — catalog and execution disagree", src, agentType, visible, gateErr)
			}
		}
	}
}
