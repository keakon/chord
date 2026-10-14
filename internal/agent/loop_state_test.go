package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Assessment only reads loop policy, the tool surface, and worker state.
// Keep persistence and worker lifecycle coverage in the integration tests.
func newLoopAssessmentTestAgent() *MainAgent {
	registry := tools.NewRegistry()
	registry.Register(tools.NewDoneTool())
	return &MainAgent{tools: registry, subs: newSubAgentRegistry(), ctxMgr: ctxmgr.NewManager(8192, 0), outputCh: make(chan AgentEvent, 32)}
}

func TestNextLoopAssessmentFromAssistantMarksCompletedOnStop(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()
	a.loopState.markProgress()
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "implemented and verified",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if assessment.Message != "Loop continuing: end this round with a `done` tool call to request loop exit." {
		t.Fatalf("assessment.Message = %q, want missing-done guidance", assessment.Message)
	}
	if a.loopState.State != LoopStateAssessing {
		t.Fatalf("loopState.State = %q, want %q", a.loopState.State, LoopStateAssessing)
	}
	found := slices.Contains(assessment.Reasons, "missing_done_tool")
	if !found {
		t.Fatalf("assessment.Reasons = %v, want missing_done_tool", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresDoneToolWhenEnabled(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "finished",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "done") {
		t.Fatalf("assessment.Message = %q, want done guard", assessment.Message)
	}
	found := slices.Contains(assessment.Reasons, "missing_done_tool")
	if !found {
		t.Fatalf("assessment.Reasons = %v, want missing_done_tool", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantAcceptsDoneToolWhenEnabled(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "all good",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "done") {
		t.Fatalf("assessment.Message = %q, want missing-done guidance", assessment.Message)
	}
}

func TestRepeatedToolCallInLoopAutoRejectsOnThirdAndFourthAttempt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.tools.Register(tools.ReadTool{})
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
read: allow
`),
	}
	a.rebuildRuleset()
	a.newTurn("")

	call := message.ToolCall{ID: "read-repeat-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md","limit":10,"offset":0}`)}

	for i := range 2 {
		result, err := a.executeToolCall(context.Background(), call)
		if err == nil {
			t.Fatalf("attempt %d err = nil, want file-not-found or execution error before interception", i+1)
		}
		if result.Result != "" {
			t.Fatalf("attempt %d result = %q, want empty result before interception", i+1, result.Result)
		}
	}

	result3, err := a.executeToolCall(context.Background(), call)
	if err != nil {
		t.Fatalf("third attempt err = %v, want nil auto-reject result", err)
	}
	if !strings.Contains(result3.Result, "Tool call rejected automatically") {
		t.Fatalf("third attempt result = %q, want automatic rejection", result3.Result)
	}
	if !strings.Contains(result3.Result, "detected 3 consecutive identical `read` tool calls") {
		t.Fatalf("third attempt result = %q, want repeated-call explanation", result3.Result)
	}
	if a.loopState.Iteration != 1 {
		t.Fatalf("loop iteration after third attempt = %d, want 1", a.loopState.Iteration)
	}

	result4, err := a.executeToolCall(context.Background(), call)
	if err != nil {
		t.Fatalf("fourth attempt err = %v, want nil auto-reject result", err)
	}
	if !strings.Contains(result4.Result, "Tool call rejected automatically") {
		t.Fatalf("fourth attempt result = %q, want automatic rejection", result4.Result)
	}
	if a.loopState.Iteration != 2 {
		t.Fatalf("loop iteration after fourth attempt = %d, want 2", a.loopState.Iteration)
	}
}

func TestRepeatedToolCallInLoopRequiresConfirmationAtIterationLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 1
	a.loopState.MaxIterationsSet = true
	a.loopState.Iteration = 1
	a.newTurn("")
	a.confirmFn = func(ctx context.Context, toolName string, args string, needsApproval []string, alreadyAllowed []string, needsApprovalRules []string, alreadyAllowedRules []string) (ConfirmResponse, error) {
		return a.AwaitConfirmWithRuleContext(ctx, toolName, args, 0, needsApproval, alreadyAllowed, needsApprovalRules, alreadyAllowedRules)
	}

	call := message.ToolCall{ID: "read-repeat-confirm", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md","limit":10,"offset":0}`)}
	for i := range 2 {
		if _, ok := a.maybeInterceptRepeatedToolCall(context.Background(), call); ok {
			t.Fatalf("attempt %d intercepted early, want no intercept before third repeated call", i+1)
		}
	}

	type confirmPayload struct {
		Reason string `json:"reason"`
		Report string `json:"report"`
	}
	handled := make(chan struct{})
	var gotResult ToolExecutionResult
	var gotErr error
	go func() {
		gotResult, gotErr = a.executeToolCall(context.Background(), call)
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	confirmed := false
	for !confirmed {
		select {
		case evt := <-a.outputCh:
			req, ok := evt.(ConfirmRequestEvent)
			if !ok {
				continue
			}
			if req.ToolName != tools.NameDone {
				t.Fatalf("confirm tool = %q, want %q", req.ToolName, tools.NameDone)
			}
			var payload confirmPayload
			if err := json.Unmarshal([]byte(req.ArgsJSON), &payload); err != nil {
				t.Fatalf("unmarshal confirm args: %v", err)
			}
			if !strings.Contains(payload.Reason, "automatic loop interception limit reached (1)") {
				t.Fatalf("confirm reason = %q, want iteration-limit explanation", payload.Reason)
			}
			if !strings.Contains(payload.Report, "Tool call rejected automatically") {
				t.Fatalf("confirm report = %q, want rejection guidance", payload.Report)
			}
			if !req.ForceDenyReason {
				t.Fatal("confirmation should require denial with a reason")
			}
			if strings.Contains(payload.Reason, "Approve") || strings.Contains(payload.Reason, "continue") {
				t.Fatalf("confirm reason = %q, should not offer approve/continue", payload.Reason)
			}
			confirmed = true
			a.ResolveConfirm("deny", req.ArgsJSON, "", "I am stuck repeating the same read; inspect previous output instead.", req.RequestID)
		case <-deadline:
			t.Fatal("timed out waiting for repeated-tool-call confirmation request")
		}
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for repeated-tool-call handling to finish")
	}
	if gotErr != nil {
		t.Fatalf("executeToolCall err = %v, want nil after deny/continue", gotErr)
	}
	if !strings.Contains(gotResult.Result, "Tool call rejected automatically") {
		t.Fatalf("executeToolCall result = %q, want auto rejection guidance", gotResult.Result)
	}
	if a.loopState.Iteration != 0 {
		t.Fatalf("loop iteration after deny/continue = %d, want 0 reset", a.loopState.Iteration)
	}
	if len(a.loopState.RepeatedToolCallStreak) != 0 {
		t.Fatalf("repeated tool call streak after deny/continue len = %d, want 0 reset", len(a.loopState.RepeatedToolCallStreak))
	}
	if _, ok := a.maybeInterceptRepeatedToolCall(context.Background(), call); ok {
		t.Fatal("next identical tool call after deny/continue intercepted immediately, want fresh streak")
	}
}

func TestRepeatedToolCallInLoopRejectsApprovalAtIterationLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 1
	a.loopState.MaxIterationsSet = true
	a.loopState.Iteration = 1
	a.newTurn("")
	a.confirmFn = func(ctx context.Context, toolName string, args string, needsApproval []string, alreadyAllowed []string, needsApprovalRules []string, alreadyAllowedRules []string) (ConfirmResponse, error) {
		return a.AwaitConfirmWithRuleContext(ctx, toolName, args, 0, needsApproval, alreadyAllowed, needsApprovalRules, alreadyAllowedRules)
	}

	call := message.ToolCall{ID: "read-repeat-confirm-approve", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md","limit":10,"offset":0}`)}
	for i := range 2 {
		if _, ok := a.maybeInterceptRepeatedToolCall(context.Background(), call); ok {
			t.Fatalf("attempt %d intercepted early, want no intercept before third repeated call", i+1)
		}
	}

	handled := make(chan struct{})
	var gotResult ToolExecutionResult
	var gotErr error
	go func() {
		gotResult, gotErr = a.executeToolCall(context.Background(), call)
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	confirmed := false
	for !confirmed {
		select {
		case evt := <-a.outputCh:
			req, ok := evt.(ConfirmRequestEvent)
			if !ok {
				continue
			}
			if !req.ForceDenyReason {
				t.Fatal("confirmation should require denial with a reason")
			}
			confirmed = true
			a.ResolveConfirm("allow", req.ArgsJSON, "", "", req.RequestID)
		case <-deadline:
			t.Fatal("timed out waiting for repeated-tool-call confirmation request")
		}
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for repeated-tool-call handling to finish")
	}
	if gotErr == nil {
		t.Fatal("executeToolCall err = nil, want approval rejected")
	}
	if !strings.Contains(strings.ToLower(gotErr.Error()), "rejected by user") {
		t.Fatalf("executeToolCall err = %v, want rejection error", gotErr)
	}
	if gotResult.Result != "" {
		t.Fatalf("executeToolCall result = %q, want empty result after invalid approval", gotResult.Result)
	}
	if a.loopState.Iteration != 1 {
		t.Fatalf("loop iteration after invalid approval = %d, want unchanged", a.loopState.Iteration)
	}
	if len(a.loopState.RepeatedToolCallStreak) != repeatedToolCallInterceptThreshold {
		t.Fatalf("repeated tool call streak after invalid approval len = %d, want retained threshold", len(a.loopState.RepeatedToolCallStreak))
	}
}

func TestNextLoopAssessmentFromAssistantAllowsDoneToolRequestBeforeCompleted(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "done",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if assessment.Message != "Loop continuing: end this round with a `done` tool call to request loop exit." {
		t.Fatalf("assessment.Message = %q, want missing-done guidance", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresNoActiveSubAgentsBeforeCompleted(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recoveryManager(),
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.subs.mu.Lock()
	a.subs.subAgents["agent-1"] = sub
	a.subs.mu.Unlock()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:    "assistant",
		Content: "finished",
		ToolCalls: []message.ToolCall{{
			ID:   "done-subagent-guard-1",
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"implemented and verified"}`),
		}},
		StopReason: "tool_calls",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "active subagents must finish before completion") {
		t.Fatalf("assessment.Message = %q, want active-subagent completion guard", assessment.Message)
	}
	found := slices.Contains(assessment.Reasons, "subagents_active")
	if !found {
		t.Fatalf("assessment.Reasons = %v, want subagents_active", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRejectsDoneMixedWithOtherToolCalls(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.tools.Register(tools.ReadTool{})
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
read: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:    "assistant",
		Content: "finished",
		ToolCalls: []message.ToolCall{
			{ID: "done-1", Name: tools.NameDone, Args: json.RawMessage(`{"report":"**Completion status**: done\n**What changed**: validated\n**Verification**: tested\n**Remaining issues**: none"}`)},
			{ID: "read-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"go.mod"}`)},
		},
		StopReason: "tool_calls",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "done") || !strings.Contains(assessment.Message, "only tool call") {
		t.Fatalf("assessment.Message = %q, want done mixed-batch guidance", assessment.Message)
	}
	found := slices.Contains(assessment.Reasons, "done_mixed_with_other_tools")
	if !found {
		t.Fatalf("assessment.Reasons = %v, want done_mixed_with_other_tools", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRepeatedSignatureContinues(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "same status", StopReason: "interrupted"}
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue", second)
	}
}

func TestNextLoopAssessmentFromAssistantResetsNoProgressAfterObservableProgress(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "same status", StopReason: "interrupted"}
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	a.loopState.markProgress()
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue after progress", second)
	}
	if a.loopState.ConsecutiveNoProgress != 0 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 0 after observable progress", a.loopState.ConsecutiveNoProgress)
	}
}

func TestStallDetectorSuspectedStallAfterTwoConsecutive(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Round 1: signature set, ConsecutiveNoProgress=1 → continue
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	// Round 2: same signature, ConsecutiveNoProgress=2 → suspected_stall
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue (suspected_stall)", second)
	}
	hasSuspectedStall := false
	for _, r := range second.Reasons {
		if r == "suspected_stall" {
			hasSuspectedStall = true
		}
	}
	if !hasSuspectedStall {
		t.Fatalf("second assessment reasons = %v, want suspected_stall", second.Reasons)
	}
}

func TestStallDetectorBudgetExhaustedAfterThreeConsecutive(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Round 1: ConsecutiveNoProgress=1
	a.nextLoopAssessmentFromAssistant(msg)
	// Round 2: ConsecutiveNoProgress=2 → suspected_stall
	a.nextLoopAssessmentFromAssistant(msg)
	// Round 3: ConsecutiveNoProgress=3 → budget_exhausted
	third := a.nextLoopAssessmentFromAssistant(msg)
	if third == nil || third.Action != LoopAssessmentActionBudgetExhausted {
		t.Fatalf("third assessment = %#v, want budget_exhausted", third)
	}
}

func TestStallDetectorResetsAfterProgress(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Round 1: ConsecutiveNoProgress=1
	a.nextLoopAssessmentFromAssistant(msg)
	// Mark hard progress
	a.loopState.markProgress()
	// Round 2: ProgressVersion != LastAssessmentVersion → resets to 0
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue", second)
	}
	if a.loopState.ConsecutiveNoProgress != 0 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 0 after progress", a.loopState.ConsecutiveNoProgress)
	}
	hasSuspectedStall := false
	for _, r := range second.Reasons {
		if r == "suspected_stall" {
			hasSuspectedStall = true
		}
	}
	if hasSuspectedStall {
		t.Fatalf("second assessment should not have suspected_stall after progress, got %v", second.Reasons)
	}
}

func TestStallDetectorBudgetExhaustedWithTerminalStop(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Build up ConsecutiveNoProgress=3 (round 1 → 1, round 2 → 2, round 3 → 3)
	a.nextLoopAssessmentFromAssistant(msg)
	a.nextLoopAssessmentFromAssistant(msg)
	third := a.nextLoopAssessmentFromAssistant(msg)
	if third == nil || third.Action != LoopAssessmentActionBudgetExhausted {
		t.Fatalf("third assessment = %#v, want budget_exhausted", third)
	}
	// Now test that even a subsequent terminal stop can't override budget_exhausted
	// (this tests the ordering: stall detector runs before terminal stop check)
	msgStop := message.Message{Role: "assistant", Content: "just summarizing", StopReason: "stop"}
	a.loopState.ConsecutiveNoProgress = 3                           // Simulate already stalled state
	a.loopState.LastAssessmentVersion = a.loopState.ProgressVersion // No new progress
	fourth := a.nextLoopAssessmentFromAssistant(msgStop)
	if fourth == nil || fourth.Action != LoopAssessmentActionBudgetExhausted {
		t.Fatalf("fourth assessment = %#v, want budget_exhausted even with terminal stop", fourth)
	}
}

func TestStallDetectorContinuationNoteIncludesSuspectedStall(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enableWithTarget("finish current task")

	note := a.buildLoopContinuationNote(&LoopAssessment{
		Action:  LoopAssessmentActionContinue,
		Reasons: []string{"suspected_stall", "context_continue"},
	})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "no hard progress detected") {
		t.Fatalf("continuation note = %q, want suspected_stall guidance", note.Text)
	}
	if !strings.Contains(note.Text, "gather distinguishing evidence for analysis") {
		t.Fatalf("continuation note = %q, want stalling warning instruction", note.Text)
	}
}

func TestStallDetectorDifferentContentDoesNotReset(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "status A", StopReason: "interrupted"}
	a.nextLoopAssessmentFromAssistant(msg)
	// Different content = different signature, but still NOT hard progress
	msg2 := message.Message{Role: "assistant", Content: "status B", StopReason: "interrupted"}
	second := a.nextLoopAssessmentFromAssistant(msg2)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue", second)
	}
	// Counter should still be 2 because different assistant text is not hard progress
	if a.loopState.ConsecutiveNoProgress != 2 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 2 (different text is not hard progress)", a.loopState.ConsecutiveNoProgress)
	}
	hasSuspectedStall := false
	for _, r := range second.Reasons {
		if r == "suspected_stall" {
			hasSuspectedStall = true
		}
	}
	if !hasSuspectedStall {
		t.Fatalf("should flag suspected_stall with ConsecutiveNoProgress=2, got reasons %v", second.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresTodoSyncBeforeCompleted(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enable()
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship loop", Status: "in_progress"}}
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "all done",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if assessment.Message == "" || assessment.Message == "Loop completed: assistant reached a terminal stop after observable progress." {
		t.Fatalf("assessment.Message = %q, want todo sync guard message", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresDoneToolBeforeCompleted(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "done for now",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "done") {
		t.Fatalf("assessment.Message = %q, want missing-done guidance", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantReturnsBlockedForBlockedTag(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "<blocked>dependency_unavailable: upstream service returned 503 repeatedly</blocked>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want blocked assessment")
	}
	if assessment.Action != LoopAssessmentActionBlocked {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionBlocked)
	}
	if !strings.Contains(assessment.Message, "dependency_unavailable") {
		t.Fatalf("assessment.Message = %q, want blocker category", assessment.Message)
	}
}

func TestIsVerificationLikeToolResultDetectsShellValidationOutput(t *testing.T) {
	payload := &ToolResultPayload{Name: "shell", ArgsJSON: `{"command":"go test ./..."}`}
	if !isVerificationLikeToolResult(payload, "go test ./...\nok") {
		t.Fatal("expected go test output to be treated as verification-like progress")
	}
}

func TestIsVerificationLikeToolResultRejectsNonValidationShellOutput(t *testing.T) {
	payload := &ToolResultPayload{Name: "shell", ArgsJSON: `{"command":"echo hello"}`}
	if isVerificationLikeToolResult(payload, "hello") {
		t.Fatal("unexpected verification classification for non-validation shell output")
	}
}

func TestIsVerificationLikeToolResultDetectsVerificationFromShellCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		result  string
	}{
		{name: "pytest command", command: "pytest -q", result: "2 passed in 0.10s"},
		{name: "npm test command", command: "npm test -- --runInBand", result: "PASS src/app.test.ts"},
		{name: "cargo test command", command: "cargo test", result: "Finished test profile"},
	} {
		payload := &ToolResultPayload{Name: "shell", ArgsJSON: `{"command":"` + tc.command + `"}`}
		if !isVerificationLikeToolResult(payload, tc.result) {
			t.Fatalf("%s should be treated as verification-like progress", tc.name)
		}
	}
}

func TestIsVerificationLikeToolResultDoesNotMisclassifyShortPatternSubstrings(t *testing.T) {
	cases := []struct {
		name    string
		command string
		result  string
	}{
		{name: "ava substring in java result", command: "echo hello", result: "java is available"},
		{name: "tox substring in available result", command: "echo hello", result: "environment available"},
		{name: "nox substring in innocuous result", command: "echo hello", result: "knoxville"},
		{name: "ava substring in command token", command: "java -version", result: "ok"},
	}
	for _, tc := range cases {
		payload := &ToolResultPayload{Name: "shell", ArgsJSON: `{"command":"` + tc.command + `"}`}
		if isVerificationLikeToolResult(payload, tc.result) {
			t.Fatalf("%s should not be treated as verification-like progress", tc.name)
		}
	}
}

func TestIsVerificationLikeToolResultMatchesShortVerificationCommandsWithWordBoundaries(t *testing.T) {
	for _, command := range []string{"tox -q", "nox -s tests", "npx ava"} {
		payload := &ToolResultPayload{Name: "shell", ArgsJSON: `{"command":"` + command + `"}`}
		if !isVerificationLikeToolResult(payload, "ok") {
			t.Fatalf("command %q should be treated as verification-like progress", command)
		}
	}
}

func TestChangedFileSummaryMarksObservableProgressSignal(t *testing.T) {
	payload := &ToolResultPayload{
		Name:     "write",
		ArgsJSON: `{"path":"internal/agent/example.go","content":"package example"}`,
		Diff:     "@@ -0,0 +1 @@\n+package example",
	}
	if changed := changedFileSummary(payload); changed == nil {
		t.Fatal("changedFileSummary = nil, want changed file summary")
	}
	if _, err := json.Marshal(changedFileSummary(payload)); err != nil {
		t.Fatalf("changedFileSummary marshal: %v", err)
	}
}

func TestSubAgentTerminalTransitionMarksLoopProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	if got := a.loopState.ConsecutiveNoProgress; got != 1 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 1", got)
	}

	sub := newControllableTestSubAgent(t, a, "worker-1")
	sub.setState(SubAgentStateRunning, "working")
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: sub.instanceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateCompleted, Summary: "done"},
	})

	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue after subagent progress", second)
	}
	if got := a.loopState.ConsecutiveNoProgress; got != 0 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 0 after terminal subagent evidence", got)
	}
}

func TestHandleAgentErrorStopsLoopAsBlocked(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.EnableLoopMode("finish current task")
	a.newTurn("")
	turnID := a.turn.ID

	a.handleAgentError(Event{
		Type:     EventAgentError,
		SourceID: "main",
		TurnID:   turnID,
		Payload:  fmt.Errorf("permission denied: missing token"),
	})

	if a.loopState.Enabled {
		t.Fatal("loop should be disabled after terminal agent error")
	}
	var blockedInfo bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if info, ok := evt.(InfoEvent); ok && strings.Contains(info.Message, "Loop blocked") {
			blockedInfo = true
			break
		}
	}
	if !blockedInfo {
		t.Fatal("expected loop blocked info event after agent error")
	}
}

func TestNextLoopAssessmentFromAssistantReturnsNilWhenLoopDisabled(t *testing.T) {
	a := newLoopAssessmentTestAgent()
	assessment := a.nextLoopAssessmentFromAssistant(message.Message{Role: "assistant", Content: "done", StopReason: "stop"})
	if assessment != nil {
		t.Fatalf("assessment = %#v, want nil when loop is disabled", assessment)
	}
}

func TestHandleLoopAssessmentContinueDoesNotConsumeAutoExitInterceptionBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 1
	a.handleLoopAssessment(Event{Type: EventLoopAssessment, Payload: &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing."}})

	if !a.loopState.Enabled {
		t.Fatal("loop should remain enabled after a normal continuation assessment")
	}
	if got := a.loopState.Iteration; got != 0 {
		t.Fatalf("Iteration = %d, want 0 because only automatic Done rejections count toward the interception budget", got)
	}
	if got := a.loopState.State; got != LoopStateExecuting {
		t.Fatalf("loopState.State = %q, want %q", got, LoopStateExecuting)
	}
}

// Request tuning reads loop mode on the LLM request goroutine while the event
// loop may be handling /loop, so the read has to be serialized by
// loopReductionMu. Without -race this only drives both entry points; with
// -race an unsynchronized field read fails the test.
func TestShouldRequireToolCallInLoopReadIsRaceSafe(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	const iterations = 200
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range iterations {
			_ = a.shouldRequireToolCallInLoop()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range iterations {
			a.EnableLoopMode("finish current task")
			a.DisableLoopMode()
			// Loop state events use blocking delivery; no TUI consumes them here.
			for len(a.outputCh) > 0 {
				<-a.outputCh
			}
		}
	}()
	close(start)
	wg.Wait()
}
