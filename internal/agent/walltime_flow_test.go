package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// TestWalltimeMainLLMCallSettlesModelTime verifies that a completed main LLM
// request settles a non-zero Model segment for the main agent, so the TIME
// sidebar section updates at every response end.
func TestWalltimeMainLLMCallSettlesModelTime(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	provider := &captureMessagesProvider{}
	a.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	stats := a.walltime.statsForAgent(identity.MainAgentID)
	if stats.Model <= 0 {
		t.Fatalf("main walltime model = %v, want > 0 after a completed LLM call", stats.Model)
	}
	if stats.Tool != 0 || stats.UserWait != 0 || stats.Cooldown != 0 {
		t.Fatalf("main walltime = %#v, want only model time", stats)
	}
}

// TestWalltimeMainToolResultSettlesToolTime verifies that a completed tool
// result settles the tool's execution duration for the main agent.
func TestWalltimeMainToolResultSettlesToolTime(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	callID := "read-1"
	a.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameRead,
			Args: []byte(`{"path":"a.txt"}`),
		}},
	})
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: callID, Name: tools.NameRead, ArgsJSON: `{"path":"a.txt"}`})

	target := a.captureMainWalltimeTarget()
	a.handleToolResult(Event{Type: EventToolResult, TurnID: turnID, Payload: &ToolResultPayload{
		CallID:         callID,
		Name:           tools.NameRead,
		ArgsJSON:       `{"path":"a.txt"}`,
		Result:         "ok",
		TurnID:         turnID,
		Duration:       1200 * time.Millisecond,
		walltimeTarget: target,
	}})

	stats := a.walltime.statsForAgent(identity.MainAgentID)
	if stats.Tool != 1200*time.Millisecond {
		t.Fatalf("main walltime tool = %v, want 1.2s after a completed tool result", stats.Tool)
	}
}

func TestWalltimeToolResultKeepsAgentAndSessionAttribution(t *testing.T) {
	oldLedger := newWalltimeTestLedger(t)
	recorder := newWalltimeRecorder(oldLedger, nil, nil)
	mainTarget := recorder.captureAt(identity.MainAgentID, "main-role", 7)
	workerTarget := recorder.captureAt("worker-1", "builder", 8)

	newLedger := newWalltimeTestLedger(t)
	recorder.repointLedger(newLedger)
	recorder.restoreStats(nil)
	recorder.recordTarget(mainTarget, analytics.WalltimePurposeTool, time.Second)
	recorder.recordTarget(workerTarget, analytics.WalltimePurposeTool, 2*time.Second)

	_, _, _, oldStats, err := oldLedger.BuildSessionEvidence()
	if err != nil {
		t.Fatalf("BuildSessionEvidence(old): %v", err)
	}
	if got := oldStats[identity.MainAgentID].Tool; got != time.Second {
		t.Fatalf("old main tool time = %v, want 1s", got)
	}
	if got := oldStats["worker-1"].Tool; got != 2*time.Second {
		t.Fatalf("old worker tool time = %v, want 2s", got)
	}
	if current := recorder.statsForAgent(identity.MainAgentID); !current.IsZero() {
		t.Fatalf("current session main stats = %#v, want zero", current)
	}
	if current := recorder.statsForAgent("worker-1"); !current.IsZero() {
		t.Fatalf("current session worker stats = %#v, want zero", current)
	}
}

// TestRequestWallclockFinishIsIdempotent verifies the settle-before-flush
// contract that keeps the TIME sidebar fresh: finish() must be safe to call
// early (before the flush events that trigger a TUI re-render) and then again
// via defer, without double-counting the segment.
func TestRequestWallclockFinishIsIdempotent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	req := a.walltime.startRequestAt(identity.MainAgentID, "", 0)
	req.finish()
	before := a.walltime.statsForAgent(identity.MainAgentID).Model
	if before <= 0 {
		t.Fatalf("model walltime after first finish = %v, want > 0", before)
	}
	// The deferred second finish() (and any racing duplicate) must be a no-op.
	req.finish()
	time.Sleep(5 * time.Millisecond)
	after := a.walltime.statsForAgent(identity.MainAgentID).Model
	if after != before {
		t.Fatalf("double finish double-counted model time: before=%v after=%v", before, after)
	}
}

// TestWalltimeConfirmRejectionClassifiesAsUserWaitOnly verifies the
// end-to-end classification for a permission confirmation that the user
// rejects: the wait settles into UserWait and the tool contributes no Tools
// time, because the execution anchor is never set for a denied call.
func TestWalltimeConfirmRejectionClassifiesAsUserWaitOnly(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(requiredValueTool{})
	parent.tools = reg
	// Mirror the production wiring (common_runtime_setup.go): confirmFn routes
	// through the interaction broker so the user wait settles into UserWait.
	parent.SetConfirmFunc(func(ctx context.Context, toolName, args string, needsApproval, alreadyAllowed, needsApprovalRules, alreadyAllowedRules []string) (ConfirmResponse, error) {
		return parent.AwaitConfirmWithRuleContext(ctx, toolName, args, 0, needsApproval, alreadyAllowed, needsApprovalRules, alreadyAllowedRules)
	})
	parent.ruleset = permission.Ruleset{{Permission: "RequiredValue", Pattern: "*", Action: permission.ActionAsk}}
	a := parent
	a.newTurn()
	turnID := a.turn.ID
	callID := "ask-deny-1"
	a.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: "RequiredValue",
			Args: []byte(`{"value":"x"}`),
		}},
	})
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: callID, Name: "RequiredValue", ArgsJSON: `{"value":"x"}`})

	execDone := make(chan struct{})
	var result ToolExecutionResult
	var execErr error
	go func() {
		defer close(execDone)
		result, execErr = a.executeToolCallWithHook(a.turn.Ctx, message.ToolCall{ID: callID, Name: "RequiredValue", Args: json.RawMessage(`{"value":"x"}`)}, false)
	}()

	// Let the confirm wait span a measurable interval, then reject it through
	// the broker exactly as the TUI does.
	var confirmRequestID string
	for confirmRequestID == "" {
		select {
		case e := <-a.Events():
			if c, ok := e.(ConfirmRequestEvent); ok {
				confirmRequestID = c.RequestID
			}
		case <-time.After(2 * time.Second):
			t.Fatal("confirm request event not emitted")
		}
	}
	time.Sleep(30 * time.Millisecond)
	waitBefore := a.walltime.statsForAgent(identity.MainAgentID).UserWait
	a.ResolveConfirm("deny", "", "", "not allowed", confirmRequestID)
	<-execDone

	if execErr == nil {
		t.Fatal("denied tool call should fail")
	}
	if !result.ExecStartedAt.IsZero() {
		t.Fatalf("ExecStartedAt = %v, want zero for a call rejected before execution", result.ExecStartedAt)
	}
	if d := toolExecDuration("RequiredValue", result, time.Now()); d != 0 {
		t.Fatalf("denied tool duration = %v, want 0 (no execution anchor)", d)
	}

	a.handleToolResult(Event{Type: EventToolResult, TurnID: turnID, Payload: &ToolResultPayload{
		CallID:   callID,
		Name:     "RequiredValue",
		ArgsJSON: `{"value":"x"}`,
		Error:    execErr,
		TurnID:   turnID,
		Duration: toolExecDuration("RequiredValue", result, time.Now()),
	}})

	stats := a.walltime.statsForAgent(identity.MainAgentID)
	if stats.Tool != 0 {
		t.Fatalf("main walltime tool = %v, want 0 for a rejected confirmation", stats.Tool)
	}
	if stats.UserWait <= waitBefore {
		t.Fatalf("main walltime user wait = %v, want > %v after the rejection wait", stats.UserWait, waitBefore)
	}
}

// TestWalltimeQuestionAnswerClassifiesAsUserWaitOnly verifies the
// end-to-end classification for an answered Question tool: the wait settles
// into UserWait via the interaction broker while Tools stays zero, because
// Question is interaction-only.
func TestWalltimeQuestionAnswerClassifiesAsUserWaitOnly(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	turnID := a.turn.ID
	a.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "ask-q-1",
			Name: tools.NameQuestion,
			Args: []byte(`{"questions":[{"question":"Pick one","header":"Choice","options":[{"label":"a","description":"first"}]}]}`),
		}},
	})
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "ask-q-1", Name: tools.NameQuestion, ArgsJSON: `{}`})

	answers := make(chan []tools.QuestionAnswer, 1)
	a.tools.Register(tools.NewQuestionTool(func(ctx context.Context, questions []tools.QuestionItem) ([]tools.QuestionAnswer, error) {
		// Resolve the question through the production interaction path so the
		// user-wait settlement runs exactly as it does for a real dialog.
		got, err := a.AskQuestions(ctx, questions, 0)
		if err != nil {
			return nil, err
		}
		answers <- got
		return got, nil
	}))

	execDone := make(chan struct{})
	var result ToolExecutionResult
	var execErr error
	go func() {
		defer close(execDone)
		result, execErr = a.executeToolCall(a.turn.Ctx, message.ToolCall{
			ID:   "ask-q-1",
			Name: tools.NameQuestion,
			Args: json.RawMessage(`{"questions":[{"question":"Pick one","header":"Choice","options":[{"label":"a","description":"first"}]}]}`),
		})
	}()

	// Answer the dialog once the question event arrives.
	var requestID string
	deadline := time.After(2 * time.Second)
	for requestID == "" {
		select {
		case e := <-a.Events():
			if q, ok := e.(QuestionRequestEvent); ok {
				requestID = q.RequestID
			}
		case <-deadline:
			t.Fatal("question request event not emitted")
		}
	}
	time.Sleep(30 * time.Millisecond)
	waitBefore := a.walltime.statsForAgent(identity.MainAgentID).UserWait
	a.ResolveQuestion([]string{"a"}, false, requestID)
	<-execDone

	if execErr != nil {
		t.Fatalf("question tool failed: %v", execErr)
	}
	select {
	case got := <-answers:
		if len(got) != 1 || len(got[0].Selected) != 1 || got[0].Selected[0] != "a" {
			t.Fatalf("answers = %+v, want [a]", got)
		}
	default:
		t.Fatal("question answers not returned")
	}

	a.handleToolResult(Event{Type: EventToolResult, TurnID: turnID, Payload: &ToolResultPayload{
		CallID:   "ask-q-1",
		Name:     tools.NameQuestion,
		ArgsJSON: `{}`,
		Result:   `[{"header":"Choice","selected":["a"]}]`,
		TurnID:   turnID,
		Duration: toolExecDuration(tools.NameQuestion, result, time.Now()),
	}})

	stats := a.walltime.statsForAgent(identity.MainAgentID)
	if stats.Tool != 0 {
		t.Fatalf("main walltime tool = %v, want 0 (Question is interaction-only)", stats.Tool)
	}
	if stats.UserWait <= waitBefore {
		t.Fatalf("main walltime user wait = %v, want > %v after answering the question", stats.UserWait, waitBefore)
	}
}

func TestNewTurnPreservesCompletedSpeculativeResultWithoutWalltimeDeadlock(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	oldTurn := a.turn
	call := message.ToolCall{ID: "spec-read-replacement", Name: tools.NameRead, Args: []byte(`{"path":"README.md"}`)}
	a.ctxMgr.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{call}})
	oldTurn.PendingToolCalls.Store(1)
	oldTurn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})

	completedAt := time.Now()
	oldTurn.streamingToolExec.mu.Lock()
	oldTurn.streamingToolExec.entries[call.ID] = &streamingToolEntry{
		call:        call,
		state:       streamingToolCompleted,
		completedAt: completedAt,
		startedAt:   completedAt.Add(-time.Millisecond),
		result: ToolExecutionResult{
			Result:            "ok",
			EffectiveArgsJSON: string(call.Args),
			ExecStartedAt:     completedAt.Add(-time.Millisecond),
		},
		done: func() chan struct{} {
			ch := make(chan struct{})
			close(ch)
			return ch
		}(),
	}
	oldTurn.streamingToolExec.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.newTurn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("newTurn blocked while settling a completed speculative tool result")
	}
	if a.turn == oldTurn {
		t.Fatal("newTurn did not replace the interrupted turn")
	}
}
