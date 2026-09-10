package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

// TestParkedUserQueueReportsGlobalIdleWithoutStartingTurn pins the parked-queue
// idle contract: after a user interrupt leaves a residual (here a
// system-generated entry the commit step does not consume), the queue must not
// suppress global idle and must not auto-start a turn.
func TestParkedUserQueueReportsGlobalIdleWithoutStartingTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	// A system-generated entry survives the interrupt commit (which only commits
	// FromUser non-slash input), so the queue stays non-empty while the user has
	// not acted again.
	a.pendingUserMessages = []pendingUserMessage{{Content: "system note queued during the cancelled turn"}}

	if !a.CancelCurrentTurn() {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}
	select {
	case evt := <-a.eventCh:
		if _, ok := evt.Payload.(*TurnCancelledPayload); !ok {
			t.Fatalf("payload = %T, want *TurnCancelledPayload", evt.Payload)
		}
		a.handleTurnCancelled(evt)
	default:
		t.Fatal("expected a turn-cancelled event")
	}

	if a.turn != nil {
		t.Fatal("turn must be cleared after cancellation")
	}
	if len(a.pendingUserMessages) == 0 {
		t.Fatal("the parked residual must stay queued until the next action")
	}
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("global idle must be reportable while the user queue is parked")
	}
	if a.turn != nil {
		t.Fatal("a parked queue must not start a new turn")
	}
}

// TestParkedUserQueueInjectedInArrivalOrderWithNextUserMessage pins the
// injection contract: messages queued while the Handoff decision was pending are
// dispatched in FIFO order together with the next user message, in that message's
// own request.
func TestParkedUserQueueInjectedInArrivalOrderWithNextUserMessage(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	setupHandoffTurn(t, a, writePlanFile(t, projectRoot)) // parks the queue while the user decides

	const (
		firstQueued  = "first queued while deciding"
		secondQueued = "second queued while deciding"
		newAction    = "the new action"
	)
	a.pendingUserMessages = []pendingUserMessage{
		{Content: firstQueued, FromUser: true},
		{Content: secondQueued, FromUser: true},
	}

	a.handleUserMessage(Event{Payload: newAction})

	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("parked messages must all be injected, %d still queued", len(a.pendingUserMessages))
	}
	var got []string
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role != message.RoleUser {
			continue
		}
		switch msg.Content {
		case firstQueued, secondQueued, newAction:
			got = append(got, msg.Content)
		}
	}
	want := []string{firstQueued, secondQueued, newAction}
	if len(got) != len(want) {
		t.Fatalf("injected user messages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("injected user messages = %v, want %v", got, want)
		}
	}
}

// TestHandoffCancelParkedQueueConsumedByNextAction pins the cancel path: a
// cancelled Handoff keeps the parked message waiting (so global idle is still
// reportable), and the next user action consumes it into its request.
func TestHandoffCancelParkedQueueConsumedByNextAction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	setupHandoffTurn(t, a, writePlanFile(t, projectRoot))
	reqID := a.pendingHandoff.RequestID

	const parked = "typed while deciding"
	a.pendingUserMessages = []pendingUserMessage{{Content: parked, FromUser: true}}

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{RequestID: reqID, Action: handoffResolveCancel}})

	if len(a.pendingUserMessages) != 1 || a.pendingUserMessages[0].Content != parked {
		t.Fatalf("a cancelled Handoff must keep the parked message for the next action, got %#v", a.pendingUserMessages)
	}
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("a cancelled Handoff must still report global idle while the parked message waits")
	}

	a.handleUserMessage(Event{Payload: "the next action"})
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("the next action must consume the parked message, %d still queued", len(a.pendingUserMessages))
	}
	if !conversationContainsUserText(a.ctxMgr.Snapshot(), parked) {
		t.Fatal("the parked message must be injected by the next action")
	}
}

// TestInterruptResidualSlashCommandProcessedAfterResume pins the idle-only slash
// contract on the interrupt path: a slash command the commit step leaves behind
// is parked, then executed exactly once by the idle drain once a user action
// resumes normal semantics.
func TestInterruptResidualSlashCommandProcessedAfterResume(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.pendingUserMessages = []pendingUserMessage{{Content: "/rename parked-title", FromUser: true}}

	if !a.CancelCurrentTurn() {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}
	select {
	case evt := <-a.eventCh:
		if _, ok := evt.Payload.(*TurnCancelledPayload); !ok {
			t.Fatalf("payload = %T, want *TurnCancelledPayload", evt.Payload)
		}
		a.handleTurnCancelled(evt)
	default:
		t.Fatal("expected a turn-cancelled event")
	}
	if len(a.pendingUserMessages) != 1 {
		t.Fatalf("the residual slash command must stay parked, got %#v", a.pendingUserMessages)
	}
	drainAgentEvents(a.outputCh)

	a.handleUserMessage(Event{Payload: "the next action"})

	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("the residual slash command must be processed, %d still queued", len(a.pendingUserMessages))
	}
	title := ""
	for _, evt := range drainAgentEvents(a.outputCh) {
		if changed, ok := evt.(SessionTitleChangedEvent); ok {
			title = changed.Title
		}
	}
	if title != "parked-title" {
		t.Fatalf("residual /rename was not executed, session title = %q", title)
	}
}
