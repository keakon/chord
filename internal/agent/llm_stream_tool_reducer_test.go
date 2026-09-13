package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func newStreamToolReducerTestTurn() *Turn {
	return &Turn{ID: 1, Epoch: 1, Ctx: context.Background()}
}

func TestStreamToolDeltaReducerFreeformInputTextAccumulatesCanonicalArgs(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	var events []AgentEvent
	reducer := streamToolDeltaReducer{turn: turn, emit: func(evt AgentEvent) { events = append(events, evt) }}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-p", Name: tools.NameApplyPatch}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-p", Name: tools.NameApplyPatch, InputText: "*** Begin Patch\n"}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-p", Name: tools.NameApplyPatch, InputText: "*** Update File: src/demo.go\n"}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-p"}})

	// Streaming updates carry the growing raw text but not the canonical
	// envelope: the envelope is a full copy of the patch, so rebuilding it per
	// fragment is quadratic in the patch size, and nothing reads it before the
	// arguments complete. Updates are coalesced to the flush cadence, so this
	// two-fragment burst lands as a single update carrying the first fragment.
	var texts []string
	for _, evt := range events {
		update, ok := evt.(ToolCallUpdateEvent)
		if !ok || update.ArgsStreamingDone {
			continue
		}
		texts = append(texts, update.InputText)
		if strings.Contains(update.ArgsJSON, `"patch"`) {
			t.Fatalf("streaming update ArgsJSON = %q, want no per-fragment envelope", update.ArgsJSON)
		}
	}
	if len(texts) != 1 || texts[0] != "*** Begin Patch\n" {
		t.Fatalf("InputText streaming updates = %#v, want one coalesced update", texts)
	}
	// The completion event is where the envelope has to be exact: speculative
	// validation, tool execution and finalize all read it from there.
	var doneArgs string
	for _, evt := range events {
		update, ok := evt.(ToolCallUpdateEvent)
		if ok && update.ArgsStreamingDone {
			doneArgs = update.ArgsJSON
		}
	}
	if doneArgs != `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n"}` {
		t.Fatalf("args-end ArgsJSON = %q, want canonical {patch} envelope", doneArgs)
	}

	call, ok := turn.getStreamingToolCall("call-p")
	if !ok {
		t.Fatal("streaming tool call not recorded")
	}
	if call.InputText != "*** Begin Patch\n*** Update File: src/demo.go\n" {
		t.Fatalf("stored InputText = %q", call.InputText)
	}
	if call.ArgsJSON != `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n"}` {
		t.Fatalf("stored ArgsJSON = %q, want canonical", call.ArgsJSON)
	}
}

func TestStreamToolDeltaReducerFreeformInputTextSkipsJSONBranch(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	reducer := streamToolDeltaReducer{turn: turn, emit: func(AgentEvent) {}}

	// A mixed provider sending both JSON fragments and freeform text for the
	// same call must not corrupt the canonical args: InputText deltas re-derive
	// the envelope from raw text, ignoring the JSON fragment channel.
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-q", Name: tools.NameApplyPatch}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-q", Name: tools.NameApplyPatch, Input: `{"patch":"*** Begin`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-q", Name: tools.NameApplyPatch, InputText: "*** Begin Patch\n"}})
	call, _ := turn.getStreamingToolCall("call-q")
	if call.ArgsJSON != `{"patch":"*** Begin Patch\n"}` {
		t.Fatalf("mixed-channel ArgsJSON = %q, want text-derived canonical", call.ArgsJSON)
	}
	if call.InputText != "*** Begin Patch\n" {
		t.Fatalf("mixed-channel InputText = %q", call.InputText)
	}
}

// A freeform patch is the largest streamed payload in the suite's coverage:
// each delta must append to the builder (linear) and only the flush cadence may
// emit the accumulated text, or the reducer is quadratic in the patch size.
func TestStreamToolFreeformInputTextCoalescesToFlushCadence(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	var events []AgentEvent
	reducer := streamToolDeltaReducer{turn: turn, emit: func(evt AgentEvent) { events = append(events, evt) }}

	const fragCount = 40
	var want strings.Builder
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-patch", Name: tools.NameApplyPatch}})
	for range fragCount {
		fragment := "+" + strings.Repeat("x", 200) + "\n"
		want.WriteString(fragment)
		reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-patch", Name: tools.NameApplyPatch, InputText: fragment}})
	}
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-patch"}})

	var updates, done int
	for _, evt := range events {
		update, ok := evt.(ToolCallUpdateEvent)
		if !ok {
			continue
		}
		if update.ArgsStreamingDone {
			done++
			continue
		}
		updates++
	}
	// The whole burst lands well inside the flush window, so only the first
	// fragment flushes a streaming update.
	if updates != 1 {
		t.Fatalf("streaming updates = %d, want 1 coalesced update", updates)
	}
	if done != 1 {
		t.Fatalf("args-end updates = %d, want 1", done)
	}
	call, ok := turn.getStreamingToolCall("call-patch")
	if !ok {
		t.Fatal("streaming tool call not recorded")
	}
	if call.InputText != want.String() {
		t.Fatalf("stored InputText length = %d, want %d", len(call.InputText), want.Len())
	}
	if wantJSON := canonicalApplyPatchArgsJSON(want.String()); call.ArgsJSON != wantJSON {
		t.Fatalf("stored ArgsJSON length = %d, want %d", len(call.ArgsJSON), len(wantJSON))
	}
}

func TestStreamToolDeltaReducerReconcilesToolUseAndStartsSpeculativeExecution(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		started <- tc
		return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: "ok"}, nil
	})

	var events []AgentEvent
	var flushes int
	var promotions []string
	var tracedCallID, tracedName, tracedAgent string
	reducer := streamToolDeltaReducer{
		agentID:                  "worker-1",
		turn:                     turn,
		registry:                 registry,
		emit:                     func(evt AgentEvent) { events = append(events, evt) },
		flushBeforeTool:          func() { flushes++ },
		promoteStreamingActivity: func(source string) { promotions = append(promotions, source) },
		recordToolUseEnd: func(callID, callName, agentID string, at time.Time) {
			tracedCallID, tracedName, tracedAgent = callID, callName, agentID
		},
	}

	if !reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-1", Name: tools.NameRead, Input: `{"path":`}}) {
		t.Fatal("tool_use_start was not handled")
	}
	if !reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-1", Input: `"README.md"}`}}) {
		t.Fatal("tool_use_delta was not handled")
	}
	select {
	case got := <-started:
		if got.ID != "call-1" || got.Name != tools.NameRead || string(got.Args) != `{"path":"README.md"}` {
			t.Fatalf("early started call = %+v args=%s", got, got.Args)
		}
	case <-time.After(time.Second):
		t.Fatal("early speculative execution was not started")
	}
	if !reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-1"}}) {
		t.Fatal("tool_use_end was not handled")
	}

	select {
	case got := <-started:
		t.Fatalf("unexpected duplicate speculative start: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	if flushes != 1 {
		t.Fatalf("flushes = %d, want 1", flushes)
	}
	if len(promotions) != 2 || promotions[0] != "tool_use_start" || promotions[1] != "tool_use_delta" {
		t.Fatalf("promotions = %#v, want start and delta", promotions)
	}
	if tracedCallID != "call-1" || tracedName != tools.NameRead || tracedAgent != "worker-1" {
		t.Fatalf("trace = %q/%q/%q", tracedCallID, tracedName, tracedAgent)
	}
	if len(events) != 3 {
		t.Fatalf("events len = %d, want 3", len(events))
	}
	start, ok := events[0].(ToolCallStartEvent)
	if !ok || start.ID != "call-1" || start.ArgsJSON != `{"path":` || start.AgentID != "worker-1" {
		t.Fatalf("start event = %#v", events[0])
	}
	update, ok := events[1].(ToolCallUpdateEvent)
	if !ok || update.ArgsJSON != `{"path":"README.md"}` || update.ArgsStreamingDone {
		t.Fatalf("delta update event = %#v", events[1])
	}
	final, ok := events[2].(ToolCallUpdateEvent)
	if !ok || !final.ArgsStreamingDone || final.ArgsJSON != `{"path":"README.md"}` || final.Name != tools.NameRead {
		t.Fatalf("final update event = %#v", events[2])
	}
}

func TestStreamToolDeltaReducerRejectsSpeculativeExecutionWhenPolicyBlocks(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.NewQuestionTool(nil))
	var started bool
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		started = true
		return ToolExecutionResult{}, nil
	})

	var events []AgentEvent
	reducer := streamToolDeltaReducer{
		turn:     turn,
		registry: registry,
		emit:     func(evt AgentEvent) { events = append(events, evt) },
	}
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-2", Name: tools.NameQuestion, Input: `{"questions":[]}`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-2"}})

	if started {
		t.Fatal("interactive tool was started speculatively")
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want start and final update", len(events))
	}
	final, ok := events[1].(ToolCallUpdateEvent)
	if !ok || !final.ArgsStreamingDone || final.Name != tools.NameQuestion {
		t.Fatalf("final update = %#v", events[1])
	}
}

func TestStreamToolDeltaReducerRejectsMutationToolSpeculativeExecution(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		started <- tc
		return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-w", Name: tools.NameWrite, Input: `{"path":"README.md","content":"x"}`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-w"}})

	select {
	case got := <-started:
		t.Fatalf("unexpected speculative start for mutation tool: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestStreamToolDeltaReducerReadOnlyShellWaitsForPriorMutatingShell(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	started := make(chan message.ToolCall, 2)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		started <- tc
		return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{
		turn:     turn,
		registry: registry,
		emit:     func(AgentEvent) {},
		ruleset: func() permission.Ruleset {
			return permission.Ruleset{{Permission: tools.NameShell, Pattern: "git commit *", Action: permission.ActionAsk}, {Permission: tools.NameShell, Pattern: "git status *", Action: permission.ActionAllow}}
		},
	}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-1", Name: tools.NameShell, Input: `{"command":"git commit -m fix"}`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-1"}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-2", Name: tools.NameShell, Input: `{"command":"git status --short"}`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-2"}})

	select {
	case got := <-started:
		t.Fatalf("unexpected speculative start: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestStreamToolDeltaReducerAllowsReadOnlyShellPrefix(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	started := make(chan message.ToolCall, 2)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		started <- tc
		return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-1", Name: tools.NameShell, Input: `{"command":"git status --short"}`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-1"}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-2", Name: tools.NameShell, Input: `{"command":"git diff HEAD"}`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-2"}})

	seen := map[string]bool{}
	deadline := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case got := <-started:
			seen[got.ID] = true
		case <-deadline:
			t.Fatalf("speculative starts = %d, want 2", len(seen))
		}
	}
	if !seen["call-1"] || !seen["call-2"] {
		t.Fatalf("started calls missing expected ids: %#v", seen)
	}
}

func TestStreamToolDeltaReducerEarlyStartsLocalReadOnlyToolsOnCompleteArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool tools.Tool
		args string
	}{
		{name: tools.NameRead, tool: tools.ReadTool{}, args: `{"path":"README.md"}`},
		{name: tools.NameGlob, tool: tools.GlobTool{}, args: `{"patterns":["**/*.go"],"path":"internal"}`},
		{name: tools.NameGrep, tool: tools.GrepTool{}, args: `{"pattern":"TODO","paths":["internal"]}`},
		{name: tools.NameReadArtifact, tool: tools.ReadArtifactTool{}, args: `{"path":"logs/out.txt"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := newStreamToolReducerTestTurn()
			registry := tools.NewRegistry()
			registry.Register(tc.tool)
			started := make(chan message.ToolCall, 1)
			turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
				started <- call
				return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
			})
			reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}

			reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call", Name: tc.name, Input: tc.args}})

			select {
			case got := <-started:
				if got.ID != "call" || got.Name != tc.name || string(got.Args) != tc.args {
					t.Fatalf("started call = %+v args=%s", got, got.Args)
				}
			case <-time.After(time.Second):
				t.Fatal("local read-only tool did not early start")
			}
		})
	}
}

func TestStreamToolDeltaReducerEarlyStartDoesNotRequireEmit(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{turn: turn, registry: registry}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call", Name: tools.NameRead, Input: `{"path":"README.md"}`}})

	select {
	case got := <-started:
		if got.ID != "call" || got.Name != tools.NameRead || string(got.Args) != `{"path":"README.md"}` {
			t.Fatalf("started call = %+v args=%s", got, got.Args)
		}
	case <-time.After(time.Second):
		t.Fatal("local read-only tool did not early start without emit")
	}
	if call, ok := turn.getStreamingToolCall("call"); !ok || call.Name != tools.NameRead || call.ArgsJSON != `{"path":"README.md"}` {
		t.Fatalf("streaming call not recorded without emit: %+v ok=%v", call, ok)
	}
}

func TestStreamToolDeltaReducerEarlyStartsWithUnknownFieldArgs(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
		ID:    "call",
		Name:  tools.NameRead,
		Input: `{"path":"README.md","extra":1}`,
	}})

	select {
	case got := <-started:
		if got.ID != "call" || got.Name != tools.NameRead {
			t.Fatalf("started call = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("read with an unrecognized field did not early start")
	}
}

// readOnlyStrictArgsTool opts into per-invocation read-only batching so the
// speculative allowlist admits it; strictArgsTool alone classifies exclusive.
type readOnlyStrictArgsTool struct{ strictArgsTool }

func (readOnlyStrictArgsTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func TestStreamToolDeltaReducerStartsSpeculativeWithUnknownFieldArgsAtEnd(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(readOnlyStrictArgsTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
		ID:    "call",
		Name:  "StrictArgs",
		Input: `{"value":"ok","extra":1}`,
	}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call"}})

	select {
	case got := <-started:
		if got.Name != "StrictArgs" {
			t.Fatalf("started call = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("call with an unrecognized field did not start speculatively at tool_use_end")
	}
	select {
	case got := <-started:
		t.Fatalf("unexpected duplicate speculative start: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestStreamToolDeltaReducerDoesNotEarlyStartIncompleteArgs(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call", Name: tools.NameRead, Input: `{"path":`}})

	select {
	case got := <-started:
		t.Fatalf("unexpected early start for incomplete args: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestStreamToolDeltaReducerDoesNotStartMalformedArgsAtToolUseEnd(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.GrepTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	var events []AgentEvent
	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(event AgentEvent) { events = append(events, event) }}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
		ID:    "call",
		Name:  tools.NameGrep,
		Input: `{"pattern":"TODO"} trailing garbage`,
	}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call"}})

	select {
	case got := <-started:
		t.Fatalf("unexpected speculative start for malformed final args: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want start and final update", len(events))
	}
	final, ok := events[1].(ToolCallUpdateEvent)
	if !ok || !final.ArgsStreamingDone || final.ArgsJSON != `{"pattern":"TODO"} trailing garbage` {
		t.Fatalf("final update = %#v", events[1])
	}
}

func TestStreamToolDeltaReducerAcceptsWrappedArgsForSpeculativeExecution(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	started := make(chan message.ToolCall, 2)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	wrapped, err := json.Marshal(`{"path":"README.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	reducer := streamToolDeltaReducer{turn: turn, registry: registry}
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{
		ID: "wrapped", Name: tools.NameRead, Input: string(wrapped),
	}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "wrapped"}})

	select {
	case got := <-started:
		if string(got.Args) != string(wrapped) {
			t.Fatalf("started args = %s, want %s", got.Args, wrapped)
		}
	case <-time.After(time.Second):
		t.Fatal("wrapped read did not start speculatively")
	}
	select {
	case got := <-started:
		t.Fatalf("wrapped read started twice: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestStreamToolDeltaReducerDoesNotEarlyStartWebFetch(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.WebFetchTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call", Name: tools.NameWebFetch, Input: `{"url":"https://example.com"}`}})

	select {
	case got := <-started:
		t.Fatalf("unexpected early start for web_fetch: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestStreamToolDeltaReducerEarlyStartRespectsPermissionPolicy(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	started := make(chan message.ToolCall, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call
		return ToolExecutionResult{EffectiveArgsJSON: string(call.Args), Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{
		turn:     turn,
		registry: registry,
		emit:     func(AgentEvent) {},
		ruleset: func() permission.Ruleset {
			return permission.Ruleset{{Permission: tools.NameRead, Pattern: "README.md", Action: permission.ActionAsk}}
		},
	}

	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call", Name: tools.NameRead, Input: `{"path":"README.md"}`}})

	select {
	case got := <-started:
		t.Fatalf("unexpected early start when permission asks: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}
func TestStreamToolDeltaReducerRollbackDrainsPartialTextAndEmitsEvent(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	turn.appendPartialText("visible text")
	turn.recordStreamingToolCall(PendingToolCall{CallID: "call-3", Name: tools.NameRead, ArgsJSON: `{"path":"README.md"}`})

	var discardedReason string
	var discardedTurn *Turn
	var events []AgentEvent
	reducer := streamToolDeltaReducer{
		agentID:                "agent-1",
		turn:                   turn,
		emit:                   func(evt AgentEvent) { events = append(events, evt) },
		drainPartialOnRollback: true,
		discardSpeculativeOnRollback: func(t *Turn, reason string) {
			discardedTurn = t
			discardedReason = reason
		},
	}

	if !reducer.Handle(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Reason: "provider_retry"}}) {
		t.Fatal("rollback was not handled")
	}
	if got := turn.drainPartialText(); got != "" {
		t.Fatalf("partial text after rollback = %q, want drained", got)
	}
	if discardedTurn != turn || discardedReason != "rollback" {
		t.Fatalf("discard callback = %p/%q, want turn/rollback", discardedTurn, discardedReason)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	rollback, ok := events[0].(StreamRollbackEvent)
	if !ok || rollback.Reason != "provider_retry" || rollback.AgentID != "agent-1" {
		t.Fatalf("rollback event = %#v", events[0])
	}
}

func TestStreamToolDeltaReducerIgnoresNonToolDelta(t *testing.T) {
	reducer := streamToolDeltaReducer{}
	if reducer.Handle(message.StreamDelta{Type: message.StreamDeltaText, Text: "hi"}) {
		t.Fatal("text delta should not be handled by tool reducer")
	}
}

func TestStreamToolDeltaReducerDeltaCanCreateMissingStartMetadata(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	startedArgs := make(chan json.RawMessage, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		startedArgs <- tc.Args
		return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: "ok"}, nil
	})

	reducer := streamToolDeltaReducer{turn: turn, registry: registry, emit: func(AgentEvent) {}}
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-4", Name: tools.NameRead, Input: `{"path":"README.md"}`}})
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-4"}})

	select {
	case got := <-startedArgs:
		if string(got) != `{"path":"README.md"}` {
			t.Fatalf("started args = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("speculative execution did not start from delta-created metadata")
	}

	snapshot := turn.snapshotStreamingToolCalls()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snapshot))
	}
	if snapshot[0].CallID != "call-4" || snapshot[0].ArgsJSON != `{"path":"README.md"}` {
		t.Fatalf("snapshot[0] = %#v", snapshot[0])
	}
	before := turn.streamingToolCallsBefore("call-4")
	if len(before) != 0 {
		t.Fatalf("streamingToolCallsBefore(call-4) len = %d, want 0", len(before))
	}

	drained := turn.drainStreamingToolCalls()
	if len(drained) != 1 {
		t.Fatalf("drain len = %d, want 1", len(drained))
	}
	if drained[0].CallID != "call-4" || drained[0].ArgsJSON != `{"path":"README.md"}` {
		t.Fatalf("drained[0] = %#v", drained[0])
	}
}

// Argument fragments must coalesce to the flush cadence: the first fragment
// flushes immediately, further fragments inside the window are suppressed, and
// the args-end event always carries the exact accumulated args. The final args
// must equal a one-shot accumulation — fragments are the protocol, not a
// lossy preview.
func TestStreamToolArgsUpdatesCoalesceToFlushCadence(t *testing.T) {
	turn := newStreamToolReducerTestTurn()
	var events []AgentEvent
	reducer := streamToolDeltaReducer{turn: turn, emit: func(evt AgentEvent) { events = append(events, evt) }}

	const fragCount = 40
	frag := `{"path":"notes.md","content":"` + strings.Repeat("x", 250)
	var want strings.Builder
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-1", Name: tools.NameWrite}})
	for i := range fragCount {
		fragment := frag
		if i == fragCount-1 {
			fragment = `"}` // close the JSON object on the last fragment
		}
		want.WriteString(fragment)
		reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-1", Name: tools.NameWrite, Input: fragment}})
	}
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-1"}})

	var updates, done int
	var lastArgs string
	for _, evt := range events {
		update, ok := evt.(ToolCallUpdateEvent)
		if !ok {
			continue
		}
		if update.ArgsStreamingDone {
			done++
			lastArgs = update.ArgsJSON
			continue
		}
		updates++
	}
	// The whole burst lands well inside the flush window, so only the first
	// fragment flushes a streaming update.
	if updates != 1 {
		t.Fatalf("streaming updates = %d, want 1 coalesced update", updates)
	}
	if done != 1 {
		t.Fatalf("args-end updates = %d, want 1", done)
	}
	if lastArgs != want.String() {
		t.Fatalf("args-end ArgsJSON length = %d, want %d (content mismatch)", len(lastArgs), want.Len())
	}
}

// BenchmarkStreamToolArgsReduceLarge guards the fragment accumulation path:// reducing a large streamed tool call must stay linear in the args size, not
// re-send or re-copy the accumulated args per fragment.
func BenchmarkStreamToolArgsReduceLarge(b *testing.B) {
	turn := newStreamToolReducerTestTurn()
	var events int
	reducer := streamToolDeltaReducer{turn: turn, emit: func(AgentEvent) { events++ }}
	fragment := strings.Repeat("x", 250)
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-1", Name: tools.NameWrite}})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: "call-1", Name: tools.NameWrite, Input: fragment}})
	}
}
