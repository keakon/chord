package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestContinueFromContextDoesNotEnableLoopByDefault(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ContinueFromContext()
	if a.loopState.Enabled {
		t.Fatal("ContinueFromContext() should not enable loop mode by default")
	}
}

func TestHandleUserMessageTreatsLoopOffAsBusyControlCommand(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.EnableLoopMode("finish current task")
	a.pendingLoopContinuation = &LoopContinuationNote{Title: "LOOP CONTINUE", Text: "Call `done` when complete."}
	a.newTurn("")

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop off"})

	if a.loopState.Enabled {
		t.Fatal("loop should be disabled immediately while busy")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for busy /loop off", got)
	}
	if a.pendingLoopContinuation != nil {
		t.Fatal("pending loop continuation should be cleared when loop is disabled")
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if strings.Contains(msg.Content, "/loop off") {
			t.Fatalf("slash command leaked into context: %q", msg.Content)
		}
	}
}

func TestHandleUserMessageTreatsLoopOnAsBusyControlCommand(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	// The command is handled inline as a control command rather than queued as
	// a user message for the busy turn.
	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled when the Done tool can be mounted")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for a busy /loop on", got)
	}
}

func TestHandleUserMessageBusyLoopOnDefersContinuationPromptInjection(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.newTurn("")

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled when Done tool is available")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("busy /loop on should defer loop continuation prompt injection")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for busy /loop on control command", got)
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind == "loop_notice" {
			t.Fatalf("unexpected persisted loop notice on busy /loop on: %q", msg.Content)
		}
	}
	for len(a.outputCh) > 0 {
		if _, ok := (<-a.outputCh).(LoopNoticeEvent); ok {
			t.Fatal("unexpected LoopNoticeEvent on busy /loop on")
		}
	}
}

func TestShouldEmitLoopContinuationForAssessmentRespectsDeferredGate(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.DeferContinuationPromptUntilDone = true

	if a.shouldEmitLoopContinuationForAssessment(&LoopAssessment{
		Action:            LoopAssessmentActionContinue,
		TriggerStopReason: "interrupted",
	}) {
		t.Fatal("should not emit continuation when deferred gate is active and stop reason is not done")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("deferred gate should remain active for non-done stop reason")
	}
	if a.shouldEmitLoopContinuationForAssessment(&LoopAssessment{
		Action:            LoopAssessmentActionContinue,
		TriggerStopReason: "stop",
	}) {
		t.Fatal("should not emit continuation on stop when deferred gate requires done")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("deferred gate should remain active for stop when done is required")
	}
	if !a.shouldEmitLoopContinuationForAssessment(&LoopAssessment{
		Action:            LoopAssessmentActionContinue,
		TriggerStopReason: "done",
	}) {
		t.Fatal("should emit continuation when terminal done stop reason arrives")
	}
	if a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("deferred gate should be cleared after terminal done stop reason")
	}
}

func TestHandleUserMessageRejectsLoopOnWhenDoneIsDenied(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.tools.Register(tools.ReadTool{})
	// Only a rule naming done keeps a role out of loop mode; see
	// donePermissionAction.
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
done: deny
`)}
	a.rebuildRuleset()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if a.loopState.Enabled {
		t.Fatal("loop should remain disabled when Done tool is denied")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0", got)
	}
}

// An allowlist role must be able to run a loop without knowing that it also
// has to allow the loop's internal exit tool: entering loop mode is the
// authorization for done, so a wildcard-only deny does not reach it.
func TestHandleUserMessageAllowsLoopOnUnderWildcardOnlyDeny(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.tools.Register(tools.ReadTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
`)}
	a.rebuildRuleset()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if !a.loopState.Enabled {
		t.Fatal("a wildcard-only deny must not keep an allowlist role out of loop mode")
	}
}

func TestHandleUserMessageLoopOnAllowedWhenYoloEnabled(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.SetInitialYoloMode(true)
	a.newTurn("")

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled when YOLO mode keeps Done available")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("busy /loop on should defer loop continuation prompt injection")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for busy /loop on control command", got)
	}
}

func TestSendLoopAnchorFromCommandIncludesCompletionContract(t *testing.T) {
	t.Parallel()
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
	if !strings.Contains(found.Content, "Completion requirements:") || strings.Contains(found.Content, "Final completion response requirements:") {
		t.Fatalf("loop notice content = %q, want one completion contract", found.Content)
	}
	if !strings.Contains(found.Content, "Pass the complete final Markdown completion report in the `done` tool's required `report` argument, following the report structure in its tool description") {
		t.Fatalf("loop notice content = %q, want Done report-argument requirement", found.Content)
	}
	// The report structure fields live in the Done tool description, which the
	// loop notice references instead of inlining.
	desc := tools.DoneTool{}.Description()
	if !strings.Contains(desc, "**Completion status**:") {
		t.Fatalf("Done description = %q, want completion status field", desc)
	}
	if !strings.Contains(desc, "**Verification**:") {
		t.Fatalf("Done description = %q, want verification field", desc)
	}
}

func TestSendLoopAnchorFromCommandPersistsLoopNoticeAsUser(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	for i := range msgs {
		if msgs[i].Kind != "loop_notice" {
			continue
		}
		if msgs[i].Role != "user" {
			t.Fatalf("loop notice role = %q, want user", msgs[i].Role)
		}
		return
	}
	t.Fatal("expected persisted loop notice message")
}

func TestEnableLoopModeSetsExecutingStateWhenPreviouslyUnset(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.State = ""
	a.EnableLoopMode("finish current task")
	if got := a.loopState.State; got != LoopStateExecuting {
		t.Fatalf("loopState.State = %q, want %q", got, LoopStateExecuting)
	}
}

func TestSendLoopAnchorFromCommandEmitsLoopNoticeEvent(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestHandleUserMessageBusyLoopOnUpdatesTargetAndMaxIterations(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
done: allow
`)}
	a.rebuildRuleset()
	a.newTurn("")

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on revised target --max-iterations 7"})

	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled")
	}
	if a.loopState.Target != "revised target" {
		t.Fatalf("loop target = %q, want revised target", a.loopState.Target)
	}
	if a.loopState.MaxIterations != 7 {
		t.Fatalf("MaxIterations = %d, want 7", a.loopState.MaxIterations)
	}
	if !a.loopState.MaxIterationsSet {
		t.Fatal("MaxIterationsSet = false, want true")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("busy /loop on should still defer continuation prompt")
	}
}

func TestEnableLoopModeEmitsUnlimitedMessageWhenMaxIterationsZero(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.MaxIterations = 0
	a.loopState.MaxIterationsSet = true
	a.EnableLoopMode("finish current task")
	var found bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if info, ok := evt.(InfoEvent); ok && strings.Contains(info.Message, "Automatic Done interceptions: unlimited") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected unlimited loop enabled info event")
	}
}
