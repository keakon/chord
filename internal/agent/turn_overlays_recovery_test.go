package agent

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func TestOverlayRecoveryOverflowReconcilesTranscript(t *testing.T) {
	mainAgent := newReadyTestMainAgent(t)
	drainAgentEvents(mainAgent.outputCh)
	want := make(map[string]int)
	for index := range maxPendingOverlayAppends + 2 {
		kind := message.KindSubAgentMailbox
		if index%2 == 0 {
			kind = message.KindBackgroundResult
		}
		msg := message.Message{
			Role: message.RoleUser, Kind: kind, Content: fmt.Sprintf("result %d", index),
			Mailbox: &message.MailboxMetadata{MessageID: fmt.Sprintf("message-%d", index)},
		}
		messageIndex := mainAgent.ctxMgr.MessageCount()
		want[msg.Mailbox.MessageID] = messageIndex
		mainAgent.ctxMgr.Append(msg)
		mainAgent.noteOverlayAppendPersistFailure(msg, identity.MainAgentID, messageIndex, kind == message.KindBackgroundResult)
	}
	if len(mainAgent.pendingOverlayAppends) != 0 || !mainAgent.pendingOverlayReconcile {
		t.Fatal("overflow must use transcript reconciliation without retaining queued payloads")
	}
	mainAgent.flushPendingOverlayAppends()
	for _, event := range drainAgentEvents(mainAgent.outputCh) {
		var msg message.Message
		var index int
		switch event := event.(type) {
		case MailboxTranscriptAppendedEvent:
			msg, index = event.Message, event.MessageIndex
			if msg.Kind != message.KindSubAgentMailbox {
				t.Fatal("incorrect mailbox event kind")
			}
		case BackgroundResultAppendedEvent:
			msg, index = event.Message, event.MessageIndex
			if msg.Kind != message.KindBackgroundResult {
				t.Fatal("incorrect background event kind")
			}
		default:
			continue
		}
		expected, exists := want[msg.Mailbox.MessageID]
		if !exists || expected != index {
			t.Fatalf("unexpected message or index: %s at %d", msg.Mailbox.MessageID, index)
		}
		delete(want, msg.Mailbox.MessageID)
	}
	if len(want) != 0 {
		t.Fatalf("missing recovery notifications: %v", want)
	}
	mainAgent.flushPendingOverlayAppends()
	if events := drainAgentEvents(mainAgent.outputCh); len(events) != 0 {
		t.Fatalf("recovery repeated events: %v", events)
	}
}

func TestClearOverlayRecoveryClearsReconciliation(t *testing.T) {
	mainAgent := newReadyTestMainAgent(t)
	mainAgent.pendingOverlayReconcile = true
	mainAgent.clearPendingOverlayAppends()
	if mainAgent.pendingOverlayReconcile {
		t.Fatal("session reset retained reconciliation")
	}
}
