package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// yoloPipelineForRuleset builds the execution gate exactly as MainAgent wires
// it under YOLO: unprotected tools are bypassed, protected ones are evaluated
// against the YOLO-filtered ruleset.
func yoloPipelineForRuleset(base permission.Ruleset, loopEnabled bool) toolExecutionPipeline {
	filtered := yoloRuleset(base)
	return toolExecutionPipeline{
		registry:           tools.NewRegistry(),
		currentRuleset:     func() permission.Ruleset { return filtered },
		bypassPermission:   func(name string) bool { return !yoloProtectedPermissionTool(name) },
		loopExitAuthorized: func() bool { return loopEnabled },
	}
}

func permissionErrFor(t *testing.T, p toolExecutionPipeline, name string) error {
	t.Helper()
	call := message.ToolCall{Name: name, Args: json.RawMessage(`{}`)}
	return p.applyPermission(context.Background(), &call, &ToolExecutionResult{})
}

// An allowlist role names no protected tool, so the YOLO filter used to reduce
// its ruleset to nothing — and an empty ruleset means "no permission config at
// all" to the execution gate, which allows everything. Switching YOLO on then
// handed the role every capability-granting control tool it had denied.
func TestYoloAllowlistRoleKeepsCapabilityToolsDenied(t *testing.T) {
	base := permissionRuleset(t, `
"*": deny
read: allow
grep: allow
`)
	p := yoloPipelineForRuleset(base, false)
	for _, name := range []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel} {
		if err := permissionErrFor(t, p, name); err == nil {
			t.Fatalf("YOLO must not grant %q to a role whose wildcard deny covers it", name)
		}
	}
	// Ordinary tools are still bypassed: that is what YOLO is for.
	for _, name := range []string{tools.NameShell, tools.NameWrite} {
		if err := permissionErrFor(t, p, name); err != nil {
			t.Fatalf("YOLO must still bypass %q, got %v", name, err)
		}
	}
}

// The same hole existed for a trusted-workspace baseline, contradicting the
// documented promise that a broad `"*": allow` does not grant these tools.
func TestYoloWildcardAllowDoesNotGrantCapabilityTools(t *testing.T) {
	p := yoloPipelineForRuleset(permissionRuleset(t, `"*": allow`), false)
	for _, name := range []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel} {
		if err := permissionErrFor(t, p, name); err == nil {
			t.Fatalf("a broad wildcard allow must not grant %q under YOLO", name)
		}
	}
}

// Naming the tool directly is how a role opts back in.
func TestYoloExplicitAllowGrantsCapabilityTool(t *testing.T) {
	p := yoloPipelineForRuleset(permissionRuleset(t, `
"*": deny
delegate: allow
`), false)
	if err := permissionErrFor(t, p, tools.NameDelegate); err != nil {
		t.Fatalf("an explicit delegate allow must survive YOLO, got %v", err)
	}
	if err := permissionErrFor(t, p, tools.NameHandoff); err == nil {
		t.Fatal("allowing delegate must not also grant handoff")
	}
}

// done and compact_context only end or shrink the current unit of work, and
// the runtime modes that mount them are the authorization, so YOLO must not
// seed a denial that would break loop exit and model-driven compaction.
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
