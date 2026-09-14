package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// gateTestHookEngine counts Fire calls per point and can synthesize verdicts,
// so tests can observe which goroutine path fires the sync tool hooks.
type gateTestHookEngine struct {
	mu        sync.Mutex
	hasHooks  bool
	fireCalls map[string]int
	modify    func(env hook.Envelope) *hook.Result
}

func (e *gateTestHookEngine) Fire(_ context.Context, env hook.Envelope) (*hook.Result, error) {
	e.mu.Lock()
	e.fireCalls[env.Point]++
	e.mu.Unlock()
	if e.modify != nil {
		if result := e.modify(env); result != nil {
			return result, nil
		}
	}
	return &hook.Result{Action: hook.ActionContinue}, nil
}

func (e *gateTestHookEngine) FireBackground(context.Context, hook.Envelope) {}

func (e *gateTestHookEngine) RunAutomation(context.Context, hook.Envelope) ([]hook.AutomationJobResult, error) {
	return nil, nil
}

func (e *gateTestHookEngine) HasSyncHooks(point string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hasHooks && (point == hook.OnToolCall || point == hook.OnBeforeToolResultAppend)
}

func (e *gateTestHookEngine) fireCount(point string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fireCalls[point]
}

// gateTestReadOnlyTool is a read-only probe tool the speculative policy will
// actually execute; its Execute reports through the test channel.
type gateTestReadOnlyTool struct {
	started chan<- string
}

func (t gateTestReadOnlyTool) Name() string        { return tools.NameRead }
func (t gateTestReadOnlyTool) Description() string { return "gate probe" }
func (t gateTestReadOnlyTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
	}
}
func (t gateTestReadOnlyTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}
func (t gateTestReadOnlyTool) IsReadOnly() bool { return true }
func (t gateTestReadOnlyTool) ConcurrencySafeReadOnly(json.RawMessage) bool {
	return true
}

func newGateTestMainAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.hookEngine = &gateTestHookEngine{fireCalls: map[string]int{}}
	return a
}

func gateTestHookEngineOf(a *MainAgent) *gateTestHookEngine {
	tap := a.hookEngine.(*gateTestHookEngine)
	return tap
}

func gateTestExec(turn *Turn, started chan<- string) *StreamingToolExecutor {
	return NewStreamingToolExecutor(turn.ID, turn.Ctx, func(AgentEvent) {}, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		if started != nil {
			started <- tc.ID
		}
		return ToolExecutionResult{Result: "ok"}, nil
	})
}

// With sync tool hooks configured, speculative execution must stand down: the
// speculative result would otherwise be finalized through a hook that can only
// run on the event loop. Without hooks, speculative execution starts normally.
func TestSyncToolHookGateDisablesSpeculativeExecution(t *testing.T) {
	a := newGateTestMainAgent(t)
	eng := gateTestHookEngineOf(a)
	eng.hasHooks = true

	started := make(chan string, 1)
	a.tools.Register(gateTestReadOnlyTool{started: started})
	turn := &Turn{ID: 77, Ctx: context.Background()}
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, turn.Ctx, func(AgentEvent) {}, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		started <- tc.ID
		return ToolExecutionResult{Result: "ok"}, nil
	})
	reducer := streamToolDeltaReducer{
		turn:         turn,
		registry:     a.tools,
		syncHookGate: a.syncToolHooksConfigured,
		emit:         func(AgentEvent) {},
	}
	end := message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "call-gate-1", Name: tools.NameRead}}
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "call-gate-1", Name: tools.NameRead, Input: `{"file_path":"/tmp/chord-gate-probe.txt"}`}})
	reducer.Handle(end)
	select {
	case id := <-started:
		t.Fatalf("speculative execution started for %v despite sync hooks", id)
	case <-time.After(150 * time.Millisecond):
	}

	eng.hasHooks = false
	reducer.Handle(end)
	select {
	case id := <-started:
		if id != "call-gate-1" {
			t.Fatalf("speculative execution started for %v, want call-gate-1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("speculative execution did not start without hooks")
	}
}

// With sync tool hooks configured, promote must not fire the finalize hook on
// the event loop; every call goes to the pipeline instead. Without hooks the
// loop-side finalize hook stays (a no-op fast path).
func TestSyncToolHookGateSkipsSpeculativeReuseInPromote(t *testing.T) {
	a := newGateTestMainAgent(t)
	eng := gateTestHookEngineOf(a)
	eng.hasHooks = true
	turn := &Turn{ID: 78, Ctx: context.Background()}
	turn.streamingToolExec = gateTestExec(turn, nil)

	batch := toolExecutionBatch{Calls: []message.ToolCall{{ID: "call-gate-2", Name: tools.NameRead, Args: json.RawMessage(`{"file_path":"/tmp/chord-gate-probe.txt"}`)}}}
	// The gate does not change the batch-fully-handled contract; it only stops
	// the loop-side finalize hook and speculative reuse.
	_ = a.promoteStreamingToolBatch(turn, batch)
	if got := eng.fireCount(hook.OnToolCall); got != 0 {
		t.Fatalf("OnToolCall fired %d times on the event loop, want 0", got)
	}

	eng.hasHooks = false
	turn2 := &Turn{ID: 79, Ctx: context.Background()}
	turn2.streamingToolExec = gateTestExec(turn2, nil)
	batch2 := toolExecutionBatch{Calls: []message.ToolCall{{ID: "call-gate-3", Name: tools.NameRead, Args: json.RawMessage(`{"file_path":"/tmp/chord-gate-probe.txt"}`)}}}
	_ = a.promoteStreamingToolBatch(turn2, batch2)
	if got := eng.fireCount(hook.OnToolCall); got != 1 {
		t.Fatalf("OnToolCall fired %d times without hooks, want 1 (loop-side fast path)", got)
	}
}

// The off-loop finalize applies the hook's modify verdict to the composed
// texts and books exactly one hook run per result.
func TestFinalizeToolResultTextsAppliesHookModify(t *testing.T) {
	a := newGateTestMainAgent(t)
	eng := gateTestHookEngineOf(a)
	eng.modify = func(env hook.Envelope) *hook.Result {
		if env.Point != hook.OnBeforeToolResultAppend {
			return nil
		}
		return &hook.Result{Action: hook.ActionModify, Data: map[string]any{
			"display_result": "hooked display",
			"context_result": "hooked result",
		}}
	}
	turn := &Turn{ID: 80, Ctx: context.Background()}
	composed := finalizeToolResultTexts(turn.Ctx, turn, a.fireHook, "call-gate-4", tools.NameRead, "{}", "raw output", nil, nil, nil)
	if composed.Display != "hooked display" || composed.Context != "hooked result" {
		t.Fatalf("composed texts = %q/%q, want hooked modifications", composed.Display, composed.Context)
	}
	if got := eng.fireCount(hook.OnBeforeToolResultAppend); got != 1 {
		t.Fatalf("OnBeforeToolResultAppend fired %d times, want 1", got)
	}
}

// A synthetic tool result (persistence-barrier failure, batch cancellation)
// reaches the loop without composed texts. With sync hooks configured, the
// handler must not run the append hook on the event loop: it composes on a
// goroutine and re-delivers the finished payload.
func TestSyntheticToolResultComposesAppendHookOffLoop(t *testing.T) {
	a := newGateTestMainAgent(t)
	eng := gateTestHookEngineOf(a)
	eng.hasHooks = true
	a.eventCh = make(chan Event, 4)

	hookStarted := make(chan struct{})
	hookRelease := make(chan struct{})
	eng.modify = func(env hook.Envelope) *hook.Result {
		if env.Point != hook.OnBeforeToolResultAppend {
			return nil
		}
		close(hookStarted)
		<-hookRelease
		return &hook.Result{Action: hook.ActionModify, Data: map[string]any{"display_result": "hooked display"}}
	}

	turn := &Turn{ID: 90, Ctx: context.Background()}
	a.turnMu.Lock()
	a.turn = turn
	a.turnMu.Unlock()
	defer func() {
		a.turnMu.Lock()
		a.turn = nil
		a.turnMu.Unlock()
	}()

	payload := &ToolResultPayload{CallID: "call-synth-1", Name: tools.NameRead, ArgsJSON: "{}", Error: errors.New("synthetic failure"), TurnID: turn.ID}

	// Stand in for the event loop: the handler must return while the hook is
	// still blocked, otherwise dispatch would stall behind the hook timeout.
	handlerDone := make(chan struct{})
	go func() {
		a.handleToolResult(Event{Type: EventToolResult, TurnID: turn.ID, Payload: payload})
		close(handlerDone)
	}()

	select {
	case <-hookStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("append hook never ran for the synthetic result")
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleToolResult blocked on the sync append hook")
	}
	if payload.composedTexts != nil {
		t.Fatal("composition finished before the hook was released")
	}

	close(hookRelease)
	select {
	case evt := <-a.eventCh:
		if evt.Type != EventToolResult || evt.Payload != payload {
			t.Fatalf("re-delivered event = %+v, want the composed tool result", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("composed tool result was not re-delivered to the loop")
	}
	if payload.composedTexts == nil || payload.composedTexts.Display != "hooked display" {
		t.Fatalf("composed texts = %+v, want the hook's modification", payload.composedTexts)
	}
	if got := eng.fireCount(hook.OnBeforeToolResultAppend); got != 1 {
		t.Fatalf("OnBeforeToolResultAppend fired %d times, want 1", got)
	}
}

// A prefilter-recorded allow with identical evaluation inputs skips the
// finalize re-evaluation even though the ruleset denies; any input drift
// (ruleset replaced, loop-mode pctx, changed args) falls back to evaluating.
func TestPermissionApprovalCacheSkipsRedundantFinalizeEvaluation(t *testing.T) {
	a := newGateTestMainAgent(t)
	deny := permission.Ruleset{{Permission: "RequiredValue", Pattern: "*", Action: permission.ActionDeny}}
	a.ruleset = deny
	turn := &Turn{ID: 81, Ctx: context.Background()}
	args := json.RawMessage(`{"value":"old"}`)
	tc := message.ToolCall{ID: "call-approval-1", Name: "RequiredValue", Args: args}

	a.recordPermissionApproval(turn, tc.ID, tc.Name, string(tc.Args), a.projectRoot)
	a.turnMu.Lock()
	a.turn = turn
	a.turnMu.Unlock()
	defer func() {
		a.turnMu.Lock()
		a.turn = nil
		a.turnMu.Unlock()
	}()
	p := a.toolExecutionPipeline()
	exec := ToolExecutionResult{}
	if err := p.applyPermission(context.Background(), &tc, &exec); err != nil {
		t.Fatalf("applyPermission err = %v, want cached allow to skip the deny evaluation", err)
	}

	// A replaced ruleset invalidates the cached decision.
	a.ruleset = permission.Ruleset{
		{Permission: "RequiredValue", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "Other", Pattern: "*", Action: permission.ActionAllow},
	}
	exec = ToolExecutionResult{}
	err := p.applyPermission(context.Background(), &tc, &exec)
	if err == nil || !strings.Contains(err.Error(), "denied by permission policy") {
		t.Fatalf("applyPermission err = %v, want deny after ruleset change", err)
	}

	// A changed args spelling invalidates the cached decision.
	a.ruleset = deny
	changed := message.ToolCall{ID: "call-approval-1", Name: "RequiredValue", Args: json.RawMessage(`{"value":"new"}`)}
	exec = ToolExecutionResult{}
	if err := p.applyPermission(context.Background(), &changed, &exec); err == nil || !strings.Contains(err.Error(), "denied by permission policy") {
		t.Fatalf("applyPermission err = %v, want deny after args change", err)
	}

	// Loop-mode pctx differs from the recorded zero pctx.
	p.loopExitAuthorized = func() bool { return true }
	exec = ToolExecutionResult{}
	if err := p.applyPermission(context.Background(), &tc, &exec); err == nil || !strings.Contains(err.Error(), "denied by permission policy") {
		t.Fatalf("applyPermission err = %v, want deny under loop-authorized pctx", err)
	}
}
