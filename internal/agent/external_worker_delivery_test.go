package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestExternalQueuedFollowupReportsDelivery(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "release")
	script := writeFakeClaude(t, strings.ReplaceAll(`
printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'
case "$*" in
  *--resume*) ;;
  *) while [ ! -f "__RELEASE__" ]; do sleep 0.01; done ;;
esac
`, "__RELEASE__", release))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	w := a.subs.runtime(handle.AgentID).(*externalWorker)
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.promptMu.Lock()
		sealed := w.inputClosed
		w.promptMu.Unlock()
		if sealed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input did not seal")
		}
		time.Sleep(time.Millisecond)
	}
	drainAgentEvents(a.outputCh)

	// Focused TUI input has no reply channel: the delivery event is the only
	// signal that the message became a queued next attempt instead of running.
	a.handleSubAgentSendMessageEvent(Event{Payload: &SubAgentSendMessagePayload{
		TaskID: handle.TaskID, Message: "Queued input.", Kind: "follow_up",
	}})

	delivery := requireInputDeliveryEvent(t, drainAgentEvents(a.outputCh))
	if delivery.AgentID != handle.AgentID || delivery.Status != SubAgentInputDeliveryQueued {
		t.Fatalf("delivery event = %+v, want queued for %s", delivery, handle.AgentID)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if record := driveExternalTask(t, a, handle.TaskID); record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed", record.State)
	}
}

func TestExternalRejectedFollowupReportsDelivery(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'
sleep 60
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	if !a.interruptSubAgentTurnsForUserCancel() {
		t.Fatal("interruptSubAgentTurnsForUserCancel = false, want the live external worker reported")
	}
	if record := driveExternalTask(t, a, handle.TaskID); record.State != string(SubAgentStateCancelled) {
		t.Fatalf("task state = %q, want cancelled", record.State)
	}
	drainAgentEvents(a.outputCh)

	a.handleSubAgentSendMessageEvent(Event{Payload: &SubAgentSendMessagePayload{
		TaskID: handle.TaskID, Message: "Follow up.", Kind: "follow_up",
	}})

	delivery := requireInputDeliveryEvent(t, drainAgentEvents(a.outputCh))
	if delivery.AgentID != handle.AgentID || delivery.Status != SubAgentInputDeliveryFailed {
		t.Fatalf("delivery event = %+v, want failed for %s", delivery, handle.AgentID)
	}
}

func requireInputDeliveryEvent(t *testing.T, events []AgentEvent) SubAgentInputDeliveryEvent {
	t.Helper()
	for _, evt := range events {
		if delivery, ok := evt.(SubAgentInputDeliveryEvent); ok {
			return delivery
		}
	}
	t.Fatal("no SubAgentInputDeliveryEvent emitted")
	return SubAgentInputDeliveryEvent{}
}

// TestInputDeliveryEventIsReliable pins that a dropped delivery event cannot
// leave the composer's draft in flight: both queued and failed deliveries have
// to survive a full TUI output channel.
func TestInputDeliveryEventIsReliable(t *testing.T) {
	for _, status := range []string{SubAgentInputDeliveryQueued, SubAgentInputDeliveryFailed} {
		evt := SubAgentInputDeliveryEvent{AgentID: "agent-1", Status: status}
		if _, _, ok := reliableOutputEventLog(evt); !ok {
			t.Errorf("reliableOutputEventLog(%q) = not reliable, want blocking delivery", status)
		}
	}
}
