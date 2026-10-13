package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestSessionTakeoverAdmissionRejectsLaterInputInOriginalSession(t *testing.T) {
	a := newReadyTestMainAgent(t)
	if !a.SendUserMessageWithReceipt("earlier instruction", "request-before") {
		t.Fatal("earlier input rejected")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- a.ResumePausedSession(ctx, "target-session", recovery.TransferIdentity{SessionID: "target-session"})
	}()
	var target sessionTakeoverTarget
	for _, want := range []string{EventUserMessage, eventSessionTakeoverTarget} {
		select {
		case event := <-a.eventCh:
			if event.Type != want {
				t.Fatalf("event=%s, want=%s", event.Type, want)
			}
			if event.Type == eventSessionTakeoverTarget {
				target = event.Payload.(sessionTakeoverTarget)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing %s", want)
		}
	}
	if a.SendUserMessageWithReceipt("later instruction", "request-after") {
		t.Fatal("input accepted after target switch admission")
	}
	a.SendUserMessage("/rename sample title")
	a.SendUserMessageWithParts([]message.ContentPart{{Type: "text", Text: "/rename multipart title"}})
	select {
	case event := <-a.eventCh:
		t.Fatalf("later input reached the session loop: %s", event.Type)
	default:
	}
	// Cancelling before owner execution restores the original admission.
	cancel()
	a.handleTakeoverTarget(target)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("switch error=%v", err)
	}
	if !a.SendUserMessageWithReceipt("fresh instruction", "request-fresh") {
		t.Fatal("failed target activation left input closed")
	}
}
