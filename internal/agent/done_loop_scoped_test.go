package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func doneVisibleForLoopState(t *testing.T, rs permission.Ruleset, loopEnabled bool) bool {
	t.Helper()
	registry := tools.NewRegistry()
	registry.Register(tools.NewDoneTool())
	visible := visibleLLMTools(registry, rs, func(string) bool { return false }, toolPermissionContext{LoopExitAuthorized: loopEnabled})
	return containsToolNamed(visible, tools.NameDone)
}

// done exists to signal loop exit, so it stays off the tool surface until a
// loop is running. That keeps its definition out of every ordinary request and
// spares the model the "reply directly or call done?" decision.
func TestDoneIsMountedOnlyWhileLoopIsActive(t *testing.T) {
	rs := permissionRuleset(t, `"*": allow`)
	if doneVisibleForLoopState(t, rs, false) {
		t.Fatal("done must not be on the tool surface outside loop mode, even under a wildcard allow")
	}
	if !doneVisibleForLoopState(t, rs, true) {
		t.Fatal("done must join the tool surface once loop mode is active")
	}
}

// An allowlist role must be able to run a loop without separately allowing the
// loop's own exit tool: entering loop mode is the authorization.
func TestDoneVisibilityIgnoresWildcardOnlyDenyInLoop(t *testing.T) {
	rs := permissionRuleset(t, `
"*": deny
read: allow
`)
	if !doneVisibleForLoopState(t, rs, true) {
		t.Fatal("a wildcard-only deny must not strip done from a running loop")
	}
}

func TestDoneVisibilityHonorsExplicitRulesInLoop(t *testing.T) {
	for _, tc := range []struct {
		rule string
		want bool
	}{
		{rule: "deny", want: false},
		{rule: "ask", want: true},
		{rule: "allow", want: true},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			rs := permissionRuleset(t, `
"*": deny
done: `+tc.rule)
			if got := doneVisibleForLoopState(t, rs, true); got != tc.want {
				t.Fatalf("done visibility under `done: %s` = %v, want %v", tc.rule, got, tc.want)
			}
		})
	}
}

// Narrow globs are specific rules, the same way they are for compact_context.
func TestDoneVisibilityHonorsNarrowToolGlob(t *testing.T) {
	rs := permissionRuleset(t, `
"*": deny
don*: deny
`)
	if doneVisibleForLoopState(t, rs, true) {
		t.Fatal("a narrow glob naming done must hide it even inside a loop")
	}
}

func TestEvaluateToolPermissionDoneRequiresLoopAuthorization(t *testing.T) {
	rs := permissionRuleset(t, `
"*": deny
read: allow
`)
	inLoop := evaluateToolPermissionInDirWithContext(rs, tools.NameDone, json.RawMessage(`{}`), "", toolPermissionContext{LoopExitAuthorized: true})
	if inLoop.Action != permission.ActionAllow {
		t.Fatalf("done inside a loop under a wildcard-only deny = %q, want allow", inLoop.Action)
	}
	// Outside a loop nothing requires done, so it keeps plain wildcard
	// semantics rather than a standing exemption.
	outsideLoop := evaluateToolPermission(rs, tools.NameDone, json.RawMessage(`{}`))
	if outsideLoop.Action != permission.ActionDeny {
		t.Fatalf("done outside a loop under a wildcard-only deny = %q, want deny", outsideLoop.Action)
	}
}

func TestEvaluateToolPermissionDoneExplicitRulesWin(t *testing.T) {
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
done: `+tc.rule)
		got := evaluateToolPermissionInDirWithContext(rs, tools.NameDone, json.RawMessage(`{}`), "", toolPermissionContext{LoopExitAuthorized: true})
		if got.Action != tc.want {
			t.Fatalf("explicit done %s = %q, want %q", tc.rule, got.Action, tc.want)
		}
	}
}

// Loop entry is gated on whether done *could* be mounted, never on whether it
// is mounted right now — done only joins the surface once the loop starts, so
// asking about present visibility would make loop mode unreachable.
func TestDoneToolPermittedIsIndependentOfCurrentVisibility(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if !a.doneToolPermitted() {
		t.Fatal("a registered, unrestricted done must be permitted outside a loop")
	}
	if a.MainToolVisible(tools.NameDone) {
		t.Fatal("done must not be on the tool surface before a loop starts")
	}
	if !a.canUseLoopMode() {
		t.Fatal("loop entry must not depend on done already being visible")
	}
}
