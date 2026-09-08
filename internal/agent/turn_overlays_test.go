package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// turnOverlayBenchAgent builds an agent with a long conversation and no
// SubAgents: the shape every single-agent session has on every request.
func turnOverlayBenchAgent(messages int) *MainAgent {
	a := &MainAgent{}
	a.ctxMgr = ctxmgr.NewManager(128000, 4096)
	for i := range messages {
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: fmt.Sprintf("message %d", i)})
	}
	return a
}

// TestTurnOverlaysWithoutSubAgentsTouchNothing pins the precondition the
// conversation snapshot is skipped on: with no task records and no pending
// mailboxes, overlay assembly contributes nothing and must not rewrite the
// durable conversation either.
func TestTurnOverlaysWithoutSubAgentsTouchNothing(t *testing.T) {
	a := turnOverlayBenchAgent(8)
	before := len(a.ctxMgr.Snapshot())
	if overlays := a.buildTurnOverlayMessages(); len(overlays) != 0 {
		t.Fatalf("buildTurnOverlayMessages() = %d overlays, want none", len(overlays))
	}
	if after := len(a.ctxMgr.Snapshot()); after != before {
		t.Fatalf("conversation length = %d, want %d", after, before)
	}
}

// TestRequestInjectedMailboxIDsUnionsBothSides covers the set the coordination
// snapshot dedupes against: the mailboxes already durable in the conversation
// plus the batch about to be appended. Losing either half makes a terminal
// completion appear twice in one request.
func TestRequestInjectedMailboxIDsUnionsBothSides(t *testing.T) {
	conversation := []message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "durable-1"}},
		{Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: " durable-2 "}},
	}
	durable := conversationMailboxIDs(conversation)
	if len(durable) != 2 || !mapContains(durable, "durable-1") || !mapContains(durable, "durable-2") {
		t.Fatalf("conversationMailboxIDs() = %v", durable)
	}
	injected := requestInjectedMailboxIDs(durable, []*SubAgentMailboxMessage{
		nil,
		{MessageID: "pending-1"},
		{MessageID: ""},
	})
	for _, id := range []string{"durable-1", "durable-2", "pending-1"} {
		if !mapContains(injected, id) {
			t.Fatalf("requestInjectedMailboxIDs() lost %q: %v", id, injected)
		}
	}
	if len(injected) != 3 {
		t.Fatalf("requestInjectedMailboxIDs() = %v, want exactly the three real ids", injected)
	}
	if requestInjectedMailboxIDs(nil, nil) != nil {
		t.Fatal("an empty request must not allocate a dedupe set")
	}
}

func BenchmarkBuildTurnOverlayMessagesWithoutSubAgents(b *testing.B) {
	a := turnOverlayBenchAgent(500)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if overlays := a.buildTurnOverlayMessages(); len(overlays) != 0 {
			b.Fatalf("overlays = %d, want none", len(overlays))
		}
	}
}

func TestStageCompletionCandidateOverlayIsOneShot(t *testing.T) {
	a := turnOverlayBenchAgent(1)
	a.stageCompletionCandidatePending = true
	overlays := a.buildTurnOverlayMessages()
	if len(overlays) != 1 || !strings.Contains(overlays[0].Content, "provisional context checkpoint") {
		t.Fatalf("stage completion overlays = %#v", overlays)
	}
	if a.stageCompletionCandidatePending {
		t.Fatal("stage completion candidate overlay was not consumed")
	}
	if len(a.ctxMgr.Snapshot()) != 1 {
		t.Fatal("stage completion overlay must not be durable")
	}
}
