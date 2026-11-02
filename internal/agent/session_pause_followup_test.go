package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestPausedTransferFollowupRejectsWithoutReemission(t *testing.T) {
	for _, fromAgent := range []bool{false, true} {
		a := newTestMainAgent(t, t.TempDir())
		a.setTaskRecords(map[string]*DurableTaskRecord{
			"sample-task": {TaskID: "sample-task", State: string(SubAgentStateIdle), LatestInstanceID: "sample-agent", RuntimeParked: true},
		})
		a.executionGate = &executiongate.Gate{}
		a.executionGate.Freeze()
		payload := &SubAgentSendMessagePayload{
			TaskID: "sample-task", RequestID: "request-1", Message: "Follow-up.", Kind: "follow_up",
			Parts: []message.ContentPart{{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("image")}},
		}
		if fromAgent {
			payload.Reply = make(chan subAgentControlResult, 1)
			payload.CallerAgentID = "sample-owner"
		}
		a.handleSubAgentSendMessageEvent(Event{Type: EventSubAgentSendMessage, Payload: payload})
		select {
		case event := <-a.eventCh:
			t.Fatalf("follow-up reemitted: %s", event.Type)
		default:
		}
		if fromAgent {
			select {
			case result := <-payload.Reply:
				if _, ok := errors.AsType[*executiongate.ErrSessionPaused](result.Err); !ok {
					t.Fatalf("error = %v", result.Err)
				}
			default:
				t.Fatal("frozen notify did not receive its refusal")
			}
		} else {
			timer := time.NewTimer(time.Second)
			found := false
			for !found {
				select {
				case raw := <-a.Events():
					if event, ok := raw.(SubAgentInputDeliveryEvent); ok {
						if event.RequestID != "request-1" || event.Status != SubAgentInputDeliveryFailed {
							t.Fatalf("delivery = %+v", event)
						}
						found = true
					}
				case <-timer.C:
					t.Fatal("focused draft did not receive its refusal")
				}
			}
			timer.Stop()
		}
		if len(a.pause.resumeFollowups) != 0 {
			t.Fatal("transfer follow-up entered the restored queue")
		}
	}
}

func TestTargetedMultipartInputRejectsUnavailableOrSettledConversation(t *testing.T) {
	for _, settled := range []bool{false, true} {
		a := newTestMainAgent(t, t.TempDir())
		if settled {
			a.setTaskRecords(map[string]*DurableTaskRecord{
				"sample-task": {TaskID: "sample-task", State: string(SubAgentStateCompleted), LatestInstanceID: "sample-agent"},
			})
		}
		a.SendUserMessageWithPartsToTarget(ConversationTarget{TaskID: "sample-task", AgentID: "sample-agent"}, []message.ContentPart{{Type: message.ContentPartText, Text: "Follow-up."}}, "request-1")
		select {
		case event := <-a.eventCh:
			t.Fatalf("input was rerouted: %s", event.Type)
		default:
		}
		timer := time.NewTimer(time.Second)
		for {
			select {
			case raw := <-a.Events():
				if event, ok := raw.(SubAgentInputDeliveryEvent); ok {
					if event.AgentID != "sample-agent" || event.RequestID != "request-1" || event.Status != SubAgentInputDeliveryFailed {
						t.Fatalf("delivery = %+v", event)
					}
					timer.Stop()
					goto received
				}
			case <-timer.C:
				t.Fatal("missing rejection receipt")
			}
		}
	received:
	}
}

func TestPausedFollowupRejectsExternalTask(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"external-1": {
			TaskID: "external-1", Attempt: 1, State: string(SubAgentStateCompleted),
			LatestInstanceID: "external-agent-1", RuntimeParked: true,
			ResumePolicy:    taskResumePolicyNotify,
			ExternalSession: &ExternalTaskSession{Backend: config.BackendKindClaudeCode, ProcessEnded: true},
		},
	})
	a.executionGate = &executiongate.Gate{}
	a.executionGate.Freeze()
	reply := make(chan subAgentControlResult, 1)
	a.queuePausedFollowup(&SubAgentSendMessagePayload{
		Ctx: context.Background(), TaskID: "external-1", Message: "Follow-up.", Kind: "follow_up", Reply: reply,
	})
	select {
	case response := <-reply:
		if response.Err == nil || !strings.Contains(response.Err.Error(), "while the session is paused") {
			t.Fatalf("paused external follow-up error = %v, want the paused-session refusal", response.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("paused external follow-up did not settle")
	}
	if len(a.pause.resumeFollowups) != 0 || len(a.pause.parkedInputs) != 0 {
		t.Fatalf("paused follow-up was queued for an external task: followups=%d inputs=%d", len(a.pause.resumeFollowups), len(a.pause.parkedInputs))
	}
}

func TestPausedTargetedFollowupSavesBeforeDispatchAndKeepsInputOrder(t *testing.T) {
	t.Parallel()
	a, mainProvider := scenarioAgent(t)
	sub := newControllableTestSubAgent(t, a, "sample-task")
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{Content: "Finished.", StopReason: "stop"}}}}
	sub.llmClient.Close()
	providerConfig := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}, Modalities: &config.ModelModalities{Input: []string{"text", "image"}}}}}, []string{"test-key"})
	sub.llmClient = llm.NewClient(providerConfig, provider, "test-model", 1024, "")
	sub.setState(SubAgentStateIdle, "paused")
	a.syncTaskRecordFromSub(sub, "")
	a.executionGate = &executiongate.Gate{}
	a.executionGate.Freeze()
	a.pause.restored = true
	a.pause.checkpoint = &recovery.SessionPauseCheckpoint{Identity: testPauseIdentity(a), CheckpointID: "sample-checkpoint", Paused: true, CheckpointComplete: true, FrozenAt: time.Now()}
	a.pause.parkedInputs = map[string][]pendingUserMessage{sub.taskID: {{InputID: "pending-1", AcceptedOrder: 1, Revision: 1, Content: "Earlier request.", FromUser: true}}}
	a.pendingUserMessages = []pendingUserMessage{{InputID: "main-pending", AcceptedOrder: 1, Revision: 1, Content: "Main request stays queued.", FromUser: true}}
	a.suspendPendingUserDrain()
	sub.startRunLoop()
	t.Cleanup(func() { sub.cancel(); <-sub.done; sub.llmClient.Close() })
	attachment := []byte("sample attachment")
	attachmentPath := filepath.Join(t.TempDir(), "sample.png")
	if err := os.WriteFile(attachmentPath, attachment, 0600); err != nil {
		t.Fatal(err)
	}
	reply := make(chan subAgentControlResult, 1)
	a.handleSubAgentSendMessageEvent(Event{Payload: &SubAgentSendMessagePayload{Ctx: context.Background(), TaskID: sub.taskID, RequestID: "request-1", Message: "New request.", Kind: "follow_up", Reply: reply, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "New request."}, {Type: message.ContentPartImage, MimeType: "image/png", ImagePath: attachmentPath}}}})
	if requests, _ := provider.snapshot(); len(requests) != 0 {
		t.Fatal("paused follow-up dispatched before saving")
	}
	var result sessionPauseResult
	select {
	case evt := <-a.eventCh:
		if evt.Type != eventSessionPauseResumeReady {
			t.Fatalf("unexpected owner event: %s", evt.Type)
		}
		result = evt.Payload.(sessionPauseResult)
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up checkpoint did not finish")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	snapshot, err := a.recoveryManager().Recover()
	if err != nil || len(snapshot.Pause.PendingInputs) != 3 {
		t.Fatalf("follow-up was not durable: snapshot=%#v err=%v", snapshot, err)
	}
	foundReceipt := false
	for _, input := range snapshot.Pause.PendingInputs {
		if input.RequestID == "request-1" {
			foundReceipt = true
		}
	}
	if !foundReceipt {
		t.Fatal("paused checkpoint lost the submission receipt")
	}
	if !a.sessionExecutionPaused() {
		t.Fatal("save worker reopened admission outside the owner loop")
	}
	if err := os.Remove(attachmentPath); err != nil {
		t.Fatal(err)
	}
	a.finishPausedSessionResume(result)
	select {
	case response := <-reply:
		if response.Err != nil || response.Handle.TaskID != sub.taskID {
			t.Fatalf("follow-up response=%#v", response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up did not settle")
	}
	waitForBlockingStreamProviderCalls(t, provider, 1)
	requests, _ := provider.snapshot()
	var content strings.Builder
	foundAttachment := false
	foundReceipt = false
	for _, msg := range requests[0] {
		foundReceipt = foundReceipt || msg.InputRequestID == "request-1"
		content.WriteString(msg.Content)
		content.WriteByte('\n')
		for _, part := range msg.Parts {
			if part.Type == message.ContentPartImage {
				data := part.Data
				if len(data) == 0 {
					data, err = os.ReadFile(part.ImagePath)
				}
				foundAttachment = err == nil && bytes.Equal(data, attachment)
			}
		}
	}
	if !foundAttachment {
		t.Fatal("saved attachment did not reach the explicit follow-up request")
	}
	if !foundReceipt {
		t.Fatal("restored follow-up lost the submission receipt")
	}
	if strings.Count(content.String(), "Earlier request.") != 1 || strings.Count(content.String(), "New request.") != 1 || strings.Index(content.String(), "Earlier request.") > strings.Index(content.String(), "New request.") {
		t.Fatalf("pending input order or identity lost: %q", content.String())
	}
	if requests, _ := mainProvider.snapshot(); len(requests) != 0 {
		t.Fatal("targeted follow-up automatically started the main conversation")
	}
}
