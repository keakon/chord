package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestExtractLoopDoneReason(t *testing.T) {
	if got := extractLoopDoneReason("summary\n<done>finished verification</done>"); got != "finished verification" {
		t.Fatalf("extractLoopDoneReason() = %q, want %q", got, "finished verification")
	}
	if got := extractLoopDoneReason("<done>line one\nline two</done>"); got != "line one line two" {
		t.Fatalf("extractLoopDoneReason() = %q, want normalized multi-line reason %q", got, "line one line two")
	}
	if got := extractLoopDoneReason("finished without tag"); got != "" {
		t.Fatalf("extractLoopDoneReason() = %q, want empty when tag missing", got)
	}
	if got := extractLoopDoneReason("<done>first</done>\n<done>second</done>"); got != "second" {
		t.Fatalf("extractLoopDoneReason() = %q, want last done reason %q", got, "second")
	}
}

func TestNextLoopAssessmentFromAssistantMarksCompletedOnStop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "finished\n<verify-not-run>no deterministic validation target in this run</verify-not-run>\n<done>implemented and verified</done>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want completed assessment")
	}
	if assessment.Action != LoopAssessmentActionCompleted {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionCompleted)
	}
	if assessment.Message != "Loop completed: implemented and verified" {
		t.Fatalf("assessment.Message = %q, want done-tag completion message", assessment.Message)
	}
	if a.loopState.State != LoopStateAssessing {
		t.Fatalf("loopState.State = %q, want %q", a.loopState.State, LoopStateAssessing)
	}
}

func TestNextLoopAssessmentFromAssistantAllowsMultipleDoneTagsBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "done\n<verify-not-run>environment does not include the required fixture</verify-not-run>\n<done>first</done>\n<done>second</done>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want completed assessment")
	}
	if assessment.Action != LoopAssessmentActionCompleted {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionCompleted)
	}
	if assessment.Message != "Loop completed: second" {
		t.Fatalf("assessment.Message = %q, want %q", assessment.Message, "Loop completed: second")
	}
}

func TestNextLoopAssessmentFromAssistantRequiresNoActiveSubAgentsBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
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
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "finished\n<done>implemented and verified</done>",
		StopReason: "stop",
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
	found := false
	for _, reason := range assessment.Reasons {
		if reason == "subagents_active" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assessment.Reasons = %v, want subagents_active", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRepeatedSignatureContinues(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
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
	a := newTestMainAgent(t, t.TempDir())
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
	a := newTestMainAgent(t, t.TempDir())
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
	a := newTestMainAgent(t, t.TempDir())
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
	a := newTestMainAgent(t, t.TempDir())
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
	a := newTestMainAgent(t, t.TempDir())
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
	a := newTestMainAgent(t, t.TempDir())
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
	if !strings.Contains(note.Text, "WARNING: You appear to be stalling") {
		t.Fatalf("continuation note = %q, want stalling warning instruction", note.Text)
	}
}

func TestStallDetectorDifferentContentDoesNotReset(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
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
	a := newTestMainAgent(t, t.TempDir())
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

func TestNextLoopAssessmentFromAssistantRequiresDoneTagBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "done for now\n<verify-not-run>tests skipped because fixture is unavailable in this environment</verify-not-run>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "<done>single-line reason</done>") {
		t.Fatalf("assessment.Message = %q, want missing-done-tag guidance", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresVerificationStatusBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "done\n<done>implemented the requested change</done>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "<verify-not-run>reason</verify-not-run>") {
		t.Fatalf("assessment.Message = %q, want verification-status guidance", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantReturnsBlockedForBlockedTag(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
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

func TestIsVerificationLikeToolResultDetectsBashValidationOutput(t *testing.T) {
	payload := &ToolResultPayload{Name: "Bash"}
	if !isVerificationLikeToolResult(payload, "go test ./...\nok") {
		t.Fatal("expected go test output to be treated as verification-like progress")
	}
	if isVerificationLikeToolResult(payload, "echo hello") {
		t.Fatal("unexpected verification classification for non-validation bash output")
	}
}

func TestChangedFileSummaryMarksObservableProgressSignal(t *testing.T) {
	payload := &ToolResultPayload{
		Name:     "Write",
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

func TestContinueFromContextDoesNotEnableLoopByDefault(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ContinueFromContext()
	if a.loopState.Enabled {
		t.Fatal("ContinueFromContext() should not enable loop mode by default")
	}
}

func TestHandleUserMessageTreatsLoopOffAsBusyControlCommand(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.EnableLoopMode("finish current task")
	a.newTurn()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop off"})

	if a.loopState.Enabled {
		t.Fatal("loop should be disabled immediately while busy")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for busy /loop off", got)
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if strings.Contains(msg.Content, "/loop off") {
			t.Fatalf("slash command leaked into context: %q", msg.Content)
		}
	}
}

func TestHandleUserMessageTreatsLoopOnAsBusyControlCommand(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled immediately while busy")
	}
	if got := a.loopState.Target; got != "finish current task" {
		t.Fatalf("loopState.Target = %q, want %q", got, "finish current task")
	}
	if got := len(a.pendingUserMessages); got != 1 {
		t.Fatalf("len(pendingUserMessages) = %d, want 1 loop-anchor message", got)
	}
	if got := pendingUserMessageText(a.pendingUserMessages[0]); got != "finish current task" {
		t.Fatalf("queued loop anchor = %q, want target text", got)
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
	a.newTurn()
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
	a := newTestMainAgent(t, t.TempDir())
	assessment := a.nextLoopAssessmentFromAssistant(message.Message{Role: "assistant", Content: "done", StopReason: "stop"})
	if assessment != nil {
		t.Fatalf("assessment = %#v, want nil when loop is disabled", assessment)
	}
}

func TestHandleLoopAssessmentStopsAtMaxIterations(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 1
	a.handleLoopAssessment(Event{Type: EventLoopAssessment, Payload: &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing."}})

	if a.loopState.Enabled {
		t.Fatal("loop should be disabled after budget exhaustion")
	}
	if got := a.CurrentLoopState(); got != "" {
		t.Fatalf("CurrentLoopState() = %q, want empty after terminal stop", got)
	}
	var found bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if info, ok := evt.(InfoEvent); ok && strings.Contains(info.Message, "max iterations reached") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected budget exhausted info event")
	}
}

func TestSendLoopAnchorFromCommandIncludesCompletionContract(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "Completion requirements:") || !strings.Contains(found.Content, "Final completion response requirements:") {
		t.Fatalf("loop notice content = %q, want completion contract", found.Content)
	}
}

func TestEnableLoopModeSetsExecutingStateWhenPreviouslyUnset(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.State = ""
	a.EnableLoopMode("finish current task")
	if got := a.loopState.State; got != LoopStateExecuting {
		t.Fatalf("loopState.State = %q, want %q", got, LoopStateExecuting)
	}
}

func TestSendLoopAnchorFromCommandEmitsLoopNoticeEvent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	var found bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		notice, ok := evt.(LoopNoticeEvent)
		if !ok {
			continue
		}
		if notice.Title == "LOOP" && strings.Contains(notice.Text, "finish current task") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected LOOP notice event when sending loop anchor from command")
	}
}

func TestSendLoopAnchorFromCommandWaitsForOutputSpace(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	for i := 0; i < cap(a.outputCh); i++ {
		a.outputCh <- StreamTextEvent{Text: "busy"}
	}

	delivered := make(chan struct{})
	go func() {
		a.sendLoopAnchorFromCommand("finish current task")
		close(delivered)
	}()

	select {
	case <-delivered:
		t.Fatal("sendLoopAnchorFromCommand returned while output channel was still full")
	case <-time.After(30 * time.Millisecond):
	}

	for i := 0; i < cap(a.outputCh); i++ {
		evt := <-a.outputCh
		if _, ok := evt.(LoopNoticeEvent); ok {
			t.Fatalf("received LoopNoticeEvent before output channel space was made available at slot %d", i)
		}
	}

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("sendLoopAnchorFromCommand did not complete after output channel space became available")
	}

	select {
	case evt := <-a.outputCh:
		notice, ok := evt.(LoopNoticeEvent)
		if !ok {
			t.Fatalf("event type = %T, want LoopNoticeEvent", evt)
		}
		if notice.Title != "LOOP" || !strings.Contains(notice.Text, "finish current task") {
			t.Fatalf("LoopNoticeEvent = %+v, want LOOP notice with target", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for LoopNoticeEvent after freeing output channel space")
	}
}

func TestLoopWorkflowPromptUsesPermissionSpecificConfirmationGuidance(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "ask in plain assistant text with enough context for a non-implementer to answer") {
		t.Fatalf("loop workflow prompt without Question should use plain-text guidance, got %q", block)
	}
	if strings.Contains(block, "Question tool") {
		t.Fatalf("loop workflow prompt without Question should not require Question tool, got %q", block)
	}

	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Question: allow
`)}
	a.rebuildRuleset()
	block = a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "call the `Question` tool") {
		t.Fatalf("loop workflow prompt with Question should require Question tool, got %q", block)
	}
}

func TestLoopWorkflowPromptIncludesCompletionContract(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "A task is complete only when") || !strings.Contains(block, "explicitly mark completion in your final response") {
		t.Fatalf("loop workflow prompt = %q, want completion contract", block)
	}
	if !strings.Contains(block, "Default to making ordinary engineering decisions yourself") {
		t.Fatalf("loop workflow prompt = %q, want autonomy guidance", block)
	}
	// Without open TODOs, the prompt should not mention TodoWrite or "no open TODO items remain"
	if strings.Contains(block, "no open TODO items remain") {
		t.Fatalf("loop workflow prompt should NOT contain 'no open TODO items remain' when no TODOs exist, got: %q", block)
	}
	if strings.Contains(block, "TodoWrite") {
		t.Fatalf("loop workflow prompt should NOT contain 'TodoWrite' when no TODOs exist, got: %q", block)
	}
}

func TestLoopWorkflowPromptIncludesTodoClauseWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.loopState.Enabled = true
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "no open TODO items remain") {
		t.Fatalf("loop workflow prompt should contain 'no open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", block)
	}
	if !strings.Contains(block, "TodoWrite") {
		t.Fatalf("loop workflow prompt should contain 'TodoWrite' when TODOs exist and TodoWrite is available, got: %q", block)
	}
}

func TestLoopWorkflowPromptIncludesTodoClauseWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered.
	a.loopState.Enabled = true
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	block := a.loopWorkflowPromptBlock()
	if strings.Contains(block, "Mark every remaining open TODO item completed or cancelled with TodoWrite") {
		t.Fatalf("loop workflow prompt should NOT tell model to use TodoWrite when the tool is unavailable, got: %q", block)
	}
	if !strings.Contains(block, "no open TODO items remain") {
		t.Fatalf("loop workflow prompt should still contain 'no open TODO items remain' when TODOs exist even without TodoWrite, got: %q", block)
	}
	if !strings.Contains(block, "Open TODO items:") {
		t.Fatalf("loop workflow prompt should list open TODOs even without TodoWrite, got: %q", block)
	}
}

func TestEnableLoopModeEmitsUnlimitedMessageWhenMaxIterationsZero(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.MaxIterations = 0
	a.loopState.MaxIterationsSet = true
	a.EnableLoopMode("finish current task")
	var found bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if info, ok := evt.(InfoEvent); ok && strings.Contains(info.Message, "Max iterations: unlimited") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected unlimited loop enabled info event")
	}
}

func TestLoopAnchorOmitsTodoRequirementWhenNoOpenTodos(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should NOT contain 'No open TODO items remain' when no TODOs exist, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "TodoWrite") {
		t.Fatalf("loop notice should NOT contain 'TodoWrite' when no TODOs exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesTodoRequirementWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should contain 'No open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "TodoWrite") {
		t.Fatalf("loop notice should contain 'TodoWrite' when TODOs exist and TodoWrite is available, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Open TODO items:") {
		t.Fatalf("loop notice should list open TODOs section when TODOs exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesTodoRequirementWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered — hasTodoWriteAccess() returns false.
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "Mark every remaining open TODO item completed or cancelled with TodoWrite") {
		t.Fatalf("loop notice should NOT instruct model to use TodoWrite when the tool is unavailable, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "TodoWrite is not available") {
		t.Fatalf("loop notice should explain TodoWrite is unavailable, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "finish the remaining work") {
		t.Fatalf("loop notice should instruct to finish remaining work, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should NOT say 'No open TODO items remain' when TodoWrite is unavailable (cannot sync), got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Open TODO items:") {
		t.Fatalf("loop notice should list open TODOs section even when TodoWrite is unavailable, got: %q", found.Content)
	}
}

func TestLoopContinuationOmitsTodoRequirementWhenNoOpenTodos(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "No open TODO items remain") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'No open TODO items remain' when no TODOs exist, got: %q", note.Text)
	}
	if strings.Contains(note.Text, "TodoWrite") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'TodoWrite' when no TODOs exist, got: %q", note.Text)
	}
}

func TestLoopContinuationIncludesTodoRequirementWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "in_progress"}}
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "No open TODO items remain") {
		t.Fatalf("LOOP CONTINUE should contain 'No open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "TodoWrite") {
		t.Fatalf("LOOP CONTINUE should contain 'TodoWrite' when TODOs exist and TodoWrite is available, got: %q", note.Text)
	}
}

func TestLoopContinuationIncludesTodoRequirementWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered — hasTodoWriteAccess() returns false.
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "in_progress"}}
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "Mark every remaining open TODO item completed or cancelled with TodoWrite") {
		t.Fatalf("LOOP CONTINUE should NOT tell model to use TodoWrite when the tool is unavailable, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "Open TODO items:") {
		t.Fatalf("LOOP CONTINUE should list open TODOs even when TodoWrite is unavailable, got: %q", note.Text)
	}
}

func TestLoopAnchorOmitsSubAgentRequirementWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "No active subagents remain") {
		t.Fatalf("loop notice should NOT contain 'No active subagents remain' when no active subagents exist, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "Active subagents:") {
		t.Fatalf("loop notice should NOT contain 'Active subagents:' section when no active subagents exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesSubAgentRequirementWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "No active subagents remain") {
		t.Fatalf("loop notice should contain 'No active subagents remain' when active subagents exist, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Active subagents:") {
		t.Fatalf("loop notice should contain 'Active subagents:' section when active subagents exist, got: %q", found.Content)
	}
}

func TestLoopContinuationOmitsSubAgentRequirementWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "No active subagents remain") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'No active subagents remain' when no active subagents exist, got: %q", note.Text)
	}
}

func TestLoopContinuationIncludesSubAgentRequirementWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"subagents_active", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "No active subagents remain") {
		t.Fatalf("LOOP CONTINUE should contain 'No active subagents remain' when active subagents exist, got: %q", note.Text)
	}
}

func TestLoopWorkflowPromptOmitsSubAgentClauseWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	block := a.loopWorkflowPromptBlock()
	if strings.Contains(block, "no active subagents remain") {
		t.Fatalf("loop workflow prompt should NOT contain 'no active subagents remain' when no active subagents exist, got: %q", block)
	}
	if strings.Contains(block, "Active subagents:") {
		t.Fatalf("loop workflow prompt should NOT contain 'Active subagents:' when no active subagents exist, got: %q", block)
	}
}

func TestLoopWorkflowPromptIncludesSubAgentClauseWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "no active subagents remain") {
		t.Fatalf("loop workflow prompt should contain 'no active subagents remain' when active subagents exist, got: %q", block)
	}
	if !strings.Contains(block, "Active subagents:") {
		t.Fatalf("loop workflow prompt should contain 'Active subagents:' when active subagents exist, got: %q", block)
	}
}

func TestLoopAnchorIncludesIterationBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "Iteration budget:") {
		t.Fatalf("loop notice should contain iteration budget, got: %q", found.Content)
	}
}

func TestLoopContinuationIncludesIterationBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 10
	a.loopState.Iteration = 7
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Iteration 7 of 10 (3 remaining)") {
		t.Fatalf("LOOP CONTINUE should contain iteration budget with remaining count, got: %q", note.Text)
	}
}

func TestLoopContinuationConvergenceWarningNearBudgetLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 10
	a.loopState.Iteration = 9
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Budget is nearly exhausted") {
		t.Fatalf("LOOP CONTINUE should warn when budget nearly exhausted, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "near the iteration limit") {
		t.Fatalf("LOOP CONTINUE instruction should mention iteration limit, got: %q", note.Text)
	}
}

func TestLoopContinuationGapLinesConcrete(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("fix the bug")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "subagents_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Why this loop continues:") {
		t.Fatalf("LOOP CONTINUE should have 'Why this loop continues' section, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "open TODO items remain") {
		t.Fatalf("LOOP CONTINUE gap lines should mention open TODO items, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "active subagents are still running") {
		t.Fatalf("LOOP CONTINUE gap lines should mention active subagents, got: %q", note.Text)
	}
}

func TestLoopContinuationNoVagueFallbackWhenConcreteReasonsExist(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("fix the bug")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	// When concrete reasons exist, the generic "loop is not yet complete" fallback should NOT appear.
	if strings.Contains(note.Text, "loop is not yet complete") {
		t.Fatalf("LOOP CONTINUE should not use vague fallback when concrete reasons exist, got: %q", note.Text)
	}
}

func TestLoopContinuationSubAgentStuckInstruction(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"subagents_active", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "stuck or blocked") {
		t.Fatalf("LOOP CONTINUE should instruct about stuck subagents when active subagents exist, got: %q", note.Text)
	}
}

func TestCurrentLoopContinuationReasonsUsesHasActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	// Add a completed subagent — it should NOT trigger subagents_active.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
	}
	sub.setState(SubAgentStateCompleted, "")
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	reasons := a.currentLoopContinuationReasons()
	for _, r := range reasons {
		if r == "subagents_active" {
			t.Fatalf("should not report subagents_active for completed subagent, reasons: %v", reasons)
		}
	}
}

func TestBuildLoopVerificationContinuationNote(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionVerify, Reasons: []string{"verification_required"}})
	if note == nil {
		t.Fatal("expected verification continuation note")
	}
	if note.Title != "LOOP VERIFY" {
		t.Fatalf("note.Title = %q, want LOOP VERIFY", note.Title)
	}
	if !strings.Contains(note.Text, "Verification required") || !strings.Contains(note.Text, "Run the smallest relevant verification now") {
		t.Fatalf("verification note text missing verification instruction: %q", note.Text)
	}
}
