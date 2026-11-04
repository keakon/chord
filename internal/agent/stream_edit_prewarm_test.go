package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type recordedPrewarm struct {
	ctx       context.Context
	path      string
	authorize func() bool
}

type prewarmTestTool struct {
	tools.Tool
	requests []recordedPrewarm
}

func (t *prewarmTestTool) PrewarmFile(ctx context.Context, path string, authorize func() bool) {
	t.requests = append(t.requests, recordedPrewarm{ctx, path, authorize})
}

func TestStreamEditPrewarmBeforeArgumentsComplete(t *testing.T) {
	for _, tt := range []struct {
		tool          tools.Tool
		first, second string
		freeform      bool
	}{
		{tools.WriteTool{}, `{"path":"src/`, `sample.go","content":"`, false},
		{tools.EditTool{}, `{"path":"src/`, `sample.go","old_string":"`, false},
		{tools.ApplyPatchTool{}, "*** Begin Patch\n*** Update File: src/", "sample.go\n@@\n", true},
		{tools.ApplyPatchTool{}, `{"patch":"*** Begin Patch\n*** Add File: src/`, `sample.go\n+`, false},
	} {
		t.Run(tt.tool.Name()+tt.first, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			turn := newStreamToolReducerTestTurn()
			turn.Ctx = ctx
			tool := &prewarmTestTool{Tool: tt.tool}
			registry := tools.NewRegistry()
			registry.Register(tool)
			r := streamToolDeltaReducer{
				turn: turn, registry: registry, emit: func(AgentEvent) {},
				ruleset: func() permission.Ruleset {
					return permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
				},
			}
			seed := &message.ToolCallDelta{ID: "edit-1", Name: tt.tool.Name()}
			if tt.freeform {
				seed.InputText = tt.first
			} else {
				seed.Input = tt.first
			}
			r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: seed})
			if len(tool.requests) != 0 {
				t.Fatal("partial path triggered prewarm")
			}
			delta := &message.ToolCallDelta{ID: seed.ID}
			if tt.freeform {
				delta.InputText = tt.second
			} else {
				delta.Input = tt.second
			}
			r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: delta})
			if len(tool.requests) != 1 || tool.requests[0].path != "src/sample.go" || !tool.requests[0].authorize() {
				t.Fatalf("prewarm requests = %+v", tool.requests)
			}
			request := tool.requests[0]
			r.Handle(message.StreamDelta{Type: message.StreamDeltaRollback})
			if request.ctx.Err() == nil {
				t.Fatal("rollback did not cancel queued prewarm")
			}
			r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: seed})
			r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: delta})
			if len(tool.requests) != 2 || tool.requests[1].ctx.Err() != nil {
				t.Fatal("retry did not create a live prewarm context")
			}
			cancel()
			if tool.requests[1].ctx.Err() == nil {
				t.Fatal("turn cancellation did not cancel prewarm")
			}
		})
	}
}

func TestStreamEditPrewarmGates(t *testing.T) {
	for _, tt := range []struct {
		name                                string
		action                              permission.Action
		input                               string
		hook, deferred, prior, deleteDenied bool
		wantQueued, wantAllowed             bool
	}{
		{name: "allowed", action: permission.ActionAllow, wantQueued: true, wantAllowed: true},
		{name: "ask", action: permission.ActionAsk, wantQueued: true},
		{name: "deny", action: permission.ActionDeny, wantQueued: true},
		{name: "hook", action: permission.ActionAllow, hook: true},
		{name: "deferred", action: permission.ActionAllow, deferred: true},
		{name: "prior mutation", action: permission.ActionAllow, prior: true, wantQueued: true},
		{name: "move denied", action: permission.ActionAllow, deleteDenied: true, input: "*** Begin Patch\n*** Update File: from.go\n*** Move to: to.go\n", wantQueued: true},
		{name: "indented move denied", action: permission.ActionAllow, deleteDenied: true, input: "*** Begin Patch\n*** Update File: from.go\n  *** Move to: to.go\n", wantQueued: true},
		{name: "non Go", action: permission.ActionAllow, input: "*** Begin Patch\n*** Add File: sample.txt\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			turn := newStreamToolReducerTestTurn()
			turn.Ctx = t.Context()
			tool := &prewarmTestTool{Tool: tools.ApplyPatchTool{}}
			registry := tools.NewRegistry()
			registry.Register(tool)
			rules := permission.Ruleset{{Permission: "*", Pattern: "*", Action: tt.action}}
			if tt.deleteDenied {
				rules = append(rules, permission.Rule{Permission: tools.NameDelete, Pattern: "*", Action: permission.ActionDeny})
			}
			r := streamToolDeltaReducer{
				turn: turn, registry: registry, emit: func(AgentEvent) {},
				ruleset:      func() permission.Ruleset { return rules },
				syncHookGate: func() bool { return tt.hook },
			}
			if tt.prior {
				r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "prior", Name: tools.NameShell}})
			}
			input := tt.input
			if input == "" {
				input = "*** Begin Patch\n*** Add File: sample.go\n"
			}
			r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
				ID: "edit", Name: tools.NameApplyPatch, InputText: input, DeferExecution: tt.deferred,
			}})
			if (len(tool.requests) > 0) != tt.wantQueued {
				t.Fatalf("queued = %d", len(tool.requests))
			}
			if len(tool.requests) > 0 && tool.requests[0].authorize() != tt.wantAllowed {
				t.Fatal("unexpected authorization decision")
			}
		})
	}
}

func TestStreamEditPrewarmPriorArguments(t *testing.T) {
	for _, tt := range []struct {
		name, toolName, args string
		wantAllowed          bool
	}{
		{name: "complete allowed", toolName: tools.NameRead, args: `{"path":"sample.go"}`, wantAllowed: true},
		{name: "complete ask", toolName: tools.NameRead, args: `{"path":"private/sample.go"}`},
		{name: "complete denied", toolName: tools.NameRead, args: `{"path":"blocked/sample.go"}`},
		{name: "empty", toolName: tools.NameRead},
		{name: "incomplete path", toolName: tools.NameRead, args: `{"path":"`},
		{name: "incomplete object", toolName: tools.NameRead, args: `{"path":"sample.go"`},
		{name: "missing path", toolName: tools.NameRead, args: `{}`},
		{name: "invalid path type", toolName: tools.NameRead, args: `{"path":1}`},
		{name: "null", toolName: tools.NameRead, args: `null`},
		{name: "trailing object", toolName: tools.NameRead, args: `{"path":"sample.go"}{"path":"private/sample.go"}`},
		{name: "encoded complete", toolName: tools.NameRead, args: `"{\"path\":\"sample.go\"}"`, wantAllowed: true},
		{name: "encoded incomplete", toolName: tools.NameRead, args: `"{\"path\":\""`},
		{name: "unknown tool", toolName: "sample_tool", args: `{"path":"sample.go"}`},
		{name: "missing tool name", args: `{"path":"sample.go"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			turn := newStreamToolReducerTestTurn()
			turn.Ctx = t.Context()
			write := &prewarmTestTool{Tool: tools.WriteTool{}}
			registry := tools.NewRegistry()
			registry.Register(tools.ReadTool{})
			registry.Register(write)
			rules := permission.Ruleset{
				{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
				{Permission: tools.NameRead, Pattern: "private/*", Action: permission.ActionAsk},
				{Permission: tools.NameRead, Pattern: "blocked/*", Action: permission.ActionDeny},
			}
			r := streamToolDeltaReducer{
				turn: turn, registry: registry, emit: func(AgentEvent) {},
				ruleset: func() permission.Ruleset { return rules },
			}
			r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
				ID: "prior", Name: tt.toolName, Input: tt.args,
			}})
			r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
				ID: "write", Name: tools.NameWrite, Input: `{"path":"sample.go","content":"`,
			}})
			if len(write.requests) != 1 {
				t.Fatalf("prewarm requests = %+v", write.requests)
			}
			if got := write.requests[0].authorize(); got != tt.wantAllowed {
				t.Fatalf("authorization = %v, want %v", got, tt.wantAllowed)
			}
		})
	}
}

func TestStreamEditPrewarmInterleavedPriorApproval(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	turn.Ctx = t.Context()
	write := &prewarmTestTool{Tool: tools.WriteTool{}}
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(write)
	rules := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameRead, Pattern: "private/*", Action: permission.ActionAsk},
	}
	r := streamToolDeltaReducer{
		turn: turn, registry: registry, emit: func(AgentEvent) {},
		ruleset: func() permission.Ruleset { return rules },
	}
	r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
		ID: "read", Name: tools.NameRead, Input: `{"path":"`,
	}})
	r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
		ID: "write", Name: tools.NameWrite, Input: `{"path":"sample.go","content":"`,
	}})
	if len(write.requests) != 1 {
		t.Fatalf("prewarm requests = %+v", write.requests)
	}
	r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{
		ID: "read", Input: `private/sample.go"}`,
	}})
	r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "read"}})
	prior, ok := turn.getStreamingToolCall("read")
	if !ok || evaluateToolPermissionInDir(rules, tools.NameRead, json.RawMessage(prior.ArgsJSON), r.pathScope).Action != permission.ActionAsk {
		t.Fatal("completed prior call does not require approval")
	}
	if write.requests[0].authorize() {
		t.Fatal("incomplete prior snapshot authorized prewarm before approval")
	}
}

func TestStreamEditPrewarmReadDoesNotStartAndCallsStaySeparate(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	turn.Ctx = t.Context()
	read := &prewarmTestTool{Tool: tools.ReadTool{}}
	write := &prewarmTestTool{Tool: tools.WriteTool{}}
	registry := tools.NewRegistry()
	registry.Register(read)
	registry.Register(write)
	r := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}
	start := func(id, name, input string) {
		r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: id, Name: name, Input: input}})
	}
	start("read", tools.NameRead, `{"path":"read.go"}`)
	start("first", tools.NameWrite, `{"path":"fir`)
	start("second", tools.NameWrite, `{"path":"sec`)
	for _, delta := range []*message.ToolCallDelta{
		{ID: "first", Input: `st.go","content":"`},
		{ID: "second", Input: `ond.go","content":"`},
	} {
		r.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: delta})
	}
	if len(read.requests) != 0 {
		t.Fatal("read triggered prewarm")
	}
	if len(write.requests) != 2 || write.requests[0].path != "first.go" || write.requests[1].path != "second.go" {
		t.Fatalf("interleaved paths = %+v", write.requests)
	}
}
