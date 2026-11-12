package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestYoloMainGatePreservesCapabilityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		tool string
		args string
		loop bool
	}{
		{
			name: "global deny and no write grant",
			yaml: "\"*\": deny\nwrite:\n  \"drafts/*\": deny",
			tool: tools.NameWrite,
			args: `{"path":"drafts/notes.txt","content":"sample"}`,
		},
		{
			name: "implicit default deny",
			yaml: "shell:\n  \"git *\": allow\n  \"rm *\": deny",
			tool: tools.NameShell,
			args: `{"command":"rm build.txt"}`,
		},
		{
			name: "glob tool grant",
			yaml: "\"*\": deny\n\"sh*\":\n  \"git *\": allow\nshell:\n  \"rm *\": deny",
			tool: tools.NameShell,
			args: `{"command":"rm build.txt"}`,
		},
		{
			name: "glob tool default",
			yaml: "\"sh*\": deny\nshell:\n  \"git *\": allow\n  \"rm *\": deny",
			tool: tools.NameShell,
			args: `{"command":"rm build.txt"}`,
		},
		{
			name: "edit counterpart",
			yaml: "edit: deny\napply_patch:\n  \"src/*\": allow\n  \"private/*\": deny",
			tool: tools.NameApplyPatch,
			args: `{"patch":"*** Begin Patch\n*** Update File: private/notes.txt\n@@\n-old\n+new\n*** End Patch"}`,
		},
		{
			name: "parameterized done deny",
			yaml: "done:\n  report: deny",
			tool: tools.NameDone,
			args: `{"report":"Complete."}`,
			loop: true,
		},
		{
			name: "parameterized compact deny",
			yaml: "compact_context:\n  checkpoint: deny",
			tool: tools.NameCompactContext,
			args: `{}`,
		},
		{
			name: "glob delegate membership",
			yaml: "\"*\": allow\n\"del*\":\n  worker: deny",
			tool: tools.NameDelegate,
			args: `{"agent_type":"worker"}`,
		},
		{
			name: "glob skill membership",
			yaml: "\"*\": allow\n\"sk*\":\n  sample: deny",
			tool: tools.NameSkill,
			args: `{"name":"sample"}`,
		},
		{
			name: "glob compact disable over grant",
			yaml: "compact_context: allow\n\"compact_*\":\n  checkpoint: deny",
			tool: tools.NameCompactContext,
			args: `{}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := yoloGateForRuleset(t, tc.yaml, false, tc.loop)
			for _, enabled := range []bool{false, true, false} {
				a.SetInitialYoloMode(enabled)
				if err := permissionErrFor(t, a, tc.tool, tc.args); err == nil {
					t.Fatalf("YOLO=%v granted %s outside the role's capabilities", enabled, tc.tool)
				}
			}
		})
	}
}

func TestYoloCachedApprovalDoesNotSurviveToggle(t *testing.T) {
	a := yoloGateForRuleset(t, "shell:\n  \"*\": allow\n  \"rm *\": deny", true, false)
	tc := message.ToolCall{ID: "call-1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"rm build.txt"}`)}
	pipeline := a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), &tc, &ToolExecutionResult{}); err != nil {
		t.Fatal(err)
	}
	a.recordPermissionApproval(a.currentTurn(), tc.ID, tc.Name, string(tc.Args), pipeline.effectiveToolBaseDir())
	if !a.permissionApprovalMatches(tc.ID, tc.Name, string(tc.Args), pipeline.effectiveToolBaseDir(), toolPermissionContext{}) {
		t.Fatal("the unchanged decision should be reusable")
	}
	a.SetInitialYoloMode(false)
	if err := pipeline.applyPermission(context.Background(), &tc, &ToolExecutionResult{}); err == nil {
		t.Fatal("YOLO approval survived turning the mode off")
	}
}

func TestYoloConcurrentSnapshotsKeepSurfaceAndBoundaries(t *testing.T) {
	a := yoloGateForRuleset(t, "\"*\": deny\nread:\n  \"*\": allow\n  \"private/*\": deny", false, false)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.WriteTool{})
	surface := mustJSONText(t, a.mainLLMToolDefinitions())
	prompt := a.buildSystemPrompt()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for enabled := true; ctx.Err() == nil; enabled = !enabled {
			a.SetInitialYoloMode(enabled)
			runtime.Gosched()
		}
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("YOLO toggle goroutine did not stop")
		}
	}()
	for range 100 {
		if ctx.Err() != nil {
			t.Fatal("concurrent snapshot test timed out")
		}
		if got := a.executionRuleset().Evaluate(tools.NameWrite, "private/notes.txt"); got != permission.ActionDeny {
			t.Fatalf("write boundary changed during toggle: %s", got)
		}
		if got := mustJSONText(t, a.mainLLMToolDefinitions()); got != surface {
			t.Fatal("tool definitions changed during toggle")
		}
		if got := a.buildSystemPrompt(); got != prompt {
			t.Fatal("system prompt changed during toggle")
		}
	}
}
