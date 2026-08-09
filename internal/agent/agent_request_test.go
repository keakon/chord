package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func newPeerTestSubAgent(t *testing.T, parent *MainAgent, instanceID, taskID string) *SubAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(parent.parentCtx)
	sub := NewSubAgent(SubAgentConfig{
		InstanceID: instanceID, TaskID: taskID, AgentDefName: "worker", TaskDesc: "peer work",
		LLMClient: newTestLLMClient(), Recovery: parent.recovery, Parent: parent,
		ParentCtx: ctx, Cancel: cancel, BaseTools: parent.tools, WorkDir: parent.projectRoot,
		SessionDir: parent.sessionDir, ModelName: "test-model",
	})
	parent.subs.mu.Lock()
	parent.subs.subAgents[sub.instanceID] = sub
	parent.subs.mu.Unlock()
	parent.syncTaskRecordFromSub(sub, "")
	return sub
}

func createMainOwnedTestRequest(t *testing.T) (*MainAgent, *SubAgent, *DurableAgentRequest) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-request")
	request, err := a.createAgentRequest(sub, tools.AgentRequestPayload{Reason: "choose an API"})
	if err != nil {
		t.Fatalf("createAgentRequest: %v", err)
	}
	return a, sub, request
}

func TestAgentRequestRoundTripRestoresLedgerAndSequence(t *testing.T) {
	a, _, request := createMainOwnedTestRequest(t)
	loaded, err := loadAgentRequests(a.sessionDir)
	if err != nil {
		t.Fatalf("loadAgentRequests: %v", err)
	}
	if got := loaded[request.CorrelationID]; got == nil || got.SourceTaskID != "adhoc-request" || got.SourceAttempt != 1 || got.State != "pending" {
		t.Fatalf("loaded request = %#v", got)
	}
	a.resetAgentRequests(loaded)
	if got := a.agentRequestSeq.Load(); got != 1 {
		t.Fatalf("agentRequestSeq = %d, want 1", got)
	}
}

func TestAgentResponseResumesSourceWithCorrelatedMailboxMetadata(t *testing.T) {
	a, sub, request := createMainOwnedTestRequest(t)
	sub.setState(SubAgentStateWaitingMain, "choose an API")

	handle, err := a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{
		TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "preserve compatibility", Kind: "reply",
	})
	if err != nil {
		t.Fatalf("NotifySubAgentMessage: %v", err)
	}
	if handle.TaskID != sub.taskID || sub.State() != SubAgentStateRunning {
		t.Fatalf("handle=%#v state=%q", handle, sub.State())
	}
	select {
	case input := <-sub.inputCh:
		if input.Mailbox == nil {
			t.Fatal("response mailbox metadata is nil")
		}
		want := &message.MailboxMetadata{
			MessageID: "response-" + request.CorrelationID, TaskID: sub.taskID,
			MessageType: string(AgentMessageTypeResponse), CorrelationID: request.CorrelationID,
			InReplyTo: request.RequestMessageID, TargetTaskID: sub.taskID, TargetAttempt: request.SourceAttempt,
		}
		if input.Mailbox.MessageID != want.MessageID || input.Mailbox.TaskID != want.TaskID || input.Mailbox.MessageType != want.MessageType || input.Mailbox.CorrelationID != want.CorrelationID || input.Mailbox.InReplyTo != want.InReplyTo || input.Mailbox.TargetTaskID != want.TargetTaskID || input.Mailbox.TargetAttempt != want.TargetAttempt {
			t.Fatalf("mailbox = %#v", input.Mailbox)
		}
	default:
		t.Fatal("expected correlated response input")
	}
	loaded, err := loadAgentRequests(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded[request.CorrelationID]; got == nil || got.State != "responded" || got.Response == nil || got.Response.DeliveredAt.IsZero() {
		t.Fatalf("persisted response = %#v", got)
	}
}

func TestAgentResponseIsIdempotentAndRejectsConflict(t *testing.T) {
	a, sub, request := createMainOwnedTestRequest(t)
	response := tools.AgentResponseRequest{TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "option A"}
	if _, err := a.NotifySubAgentMessage(context.Background(), response); err != nil {
		t.Fatal(err)
	}
	if handle, err := a.NotifySubAgentMessage(context.Background(), response); err != nil || handle.Message != "response already recorded" {
		t.Fatalf("idempotent response handle=%#v error=%v", handle, err)
	}
	response.Message = "option B"
	if _, err := a.NotifySubAgentMessage(context.Background(), response); err == nil || !strings.Contains(err.Error(), "responded") {
		t.Fatalf("conflicting response error = %v", err)
	}
}

func TestAgentResponseRetryAfterFinalPersistenceFailureDoesNotRedeliver(t *testing.T) {
	a, sub, request := createMainOwnedTestRequest(t)
	originalSessionDir := a.sessionDir
	response := tools.AgentResponseRequest{TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "option A"}

	sub.lifecycleMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := a.NotifySubAgentMessage(context.Background(), response)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		loaded, err := loadAgentRequests(originalSessionDir)
		if err != nil {
			sub.lifecycleMu.Unlock()
			t.Fatal(err)
		}
		if got := loaded[request.CorrelationID]; got != nil && got.Response != nil {
			break
		}
		if time.Now().After(deadline) {
			sub.lifecycleMu.Unlock()
			t.Fatal("response was not durably prepared")
		}
		time.Sleep(time.Millisecond)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		sub.lifecycleMu.Unlock()
		t.Fatal(err)
	}
	a.sessionDir = blocked
	sub.lifecycleMu.Unlock()
	if err := <-done; err == nil {
		t.Fatal("expected final response persistence failure")
	}
	a.sessionDir = originalSessionDir

	if _, err := a.NotifySubAgentMessage(context.Background(), response); err != nil {
		t.Fatalf("retry response: %v", err)
	}
	select {
	case first := <-sub.inputCh:
		if first.Mailbox == nil || first.Mailbox.MessageID != "response-"+request.CorrelationID {
			t.Fatalf("first mailbox = %#v", first.Mailbox)
		}
	default:
		t.Fatal("expected original response delivery")
	}
	select {
	case duplicate := <-sub.inputCh:
		t.Fatalf("response was delivered twice: %#v", duplicate.Mailbox)
	default:
	}
}

func TestRespondedAgentRequestRedeliversWhenDeliveryWasNotDurable(t *testing.T) {
	a, sub, request := createMainOwnedTestRequest(t)
	response := tools.AgentResponseRequest{TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "option A"}
	if _, err := a.NotifySubAgentMessage(context.Background(), response); err != nil {
		t.Fatalf("first response: %v", err)
	}
	select {
	case <-sub.inputCh:
	default:
		t.Fatal("expected first response delivery")
	}
	sub.inputQueueMu.Lock()
	sub.inputQueueBytes = 0
	sub.acceptedMailboxIDs = make(map[string]struct{})
	sub.inputQueueMu.Unlock()

	if _, err := a.NotifySubAgentMessage(context.Background(), response); err != nil {
		t.Fatalf("recovery response: %v", err)
	}
	select {
	case recovered := <-sub.inputCh:
		if recovered.Mailbox == nil || recovered.Mailbox.MessageID != "response-"+request.CorrelationID {
			t.Fatalf("recovered mailbox = %#v", recovered.Mailbox)
		}
	default:
		t.Fatal("responded request without durable delivery was not redelivered")
	}
}

func TestRespondedAgentRequestUsesDurableTranscriptEvidence(t *testing.T) {
	a, sub, request := createMainOwnedTestRequest(t)
	request.State = "responded"
	request.Response = &DurableAgentResponse{
		Message: "option A", ResponseID: "response-" + request.CorrelationID, PersistedAt: time.Now(), DeliveredAt: time.Now(),
	}
	a.subs.mu.Lock()
	a.subs.agentRequests[request.CorrelationID] = cloneDurableAgentRequest(request)
	a.subs.mu.Unlock()
	mailbox := &message.MailboxMetadata{MessageID: request.Response.ResponseID, MessageType: string(AgentMessageTypeResponse)}
	if err := a.recovery.PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "option A", Kind: message.KindSubAgentMailbox, Mailbox: mailbox}); err != nil {
		t.Fatal(err)
	}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, sub.instanceID)
	a.subs.mu.Unlock()

	handle, err := a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{
		TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "option A",
	})
	if err != nil {
		t.Fatalf("idempotent response: %v", err)
	}
	if handle.Message != "response already recorded" {
		t.Fatalf("handle = %#v", handle)
	}
}

func TestRetainAgentRequestsKeepsPendingAndNewestClosed(t *testing.T) {
	records := make(map[string]*DurableAgentRequest, maxRetainedClosedAgentRequests+3)
	for i := range maxRetainedClosedAgentRequests + 2 {
		id := fmt.Sprintf("corr-%d", i+1)
		records[id] = &DurableAgentRequest{CorrelationID: id, State: "responded", CreatedAt: time.Unix(int64(i+1), 0)}
	}
	records["corr-pending"] = &DurableAgentRequest{CorrelationID: "corr-pending", State: "pending", CreatedAt: time.Unix(1, 0)}
	retained := retainAgentRequests(records)
	if len(retained) != maxRetainedClosedAgentRequests+1 {
		t.Fatalf("retained requests = %d", len(retained))
	}
	if retained["corr-pending"] == nil {
		t.Fatal("pending request was pruned")
	}
	if retained["corr-1"] != nil || retained["corr-2"] != nil {
		t.Fatalf("oldest closed requests were retained: %#v %#v", retained["corr-1"], retained["corr-2"])
	}
	if retained[fmt.Sprintf("corr-%d", maxRetainedClosedAgentRequests+2)] == nil {
		t.Fatal("newest closed request was pruned")
	}
}

func TestPersistAgentRequestStatePrunesMemoryAndDiskTogether(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	records := make(map[string]*DurableAgentRequest, maxRetainedClosedAgentRequests+2)
	for i := range maxRetainedClosedAgentRequests + 1 {
		id := fmt.Sprintf("corr-%d", i+1)
		records[id] = &DurableAgentRequest{
			Version: agentRequestSchemaVersion, CorrelationID: id, RequestMessageID: "msg-" + id,
			SourceTaskID: "task", SourceAttempt: 1, State: "responded", CreatedAt: time.Unix(int64(i+1), 0),
		}
	}
	pending := &DurableAgentRequest{
		Version: agentRequestSchemaVersion, CorrelationID: "corr-pending", RequestMessageID: "msg-pending",
		SourceTaskID: "task", SourceAttempt: 1, State: "pending", CreatedAt: time.Now(),
	}
	records[pending.CorrelationID] = pending
	if err := a.persistAgentRequestStateLocked(records, pending, "cancelled"); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAgentRequests(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	a.subs.mu.RLock()
	memoryCount := len(a.subs.agentRequests)
	a.subs.mu.RUnlock()
	if memoryCount != maxRetainedClosedAgentRequests || len(loaded) != memoryCount {
		t.Fatalf("memory=%d disk=%d", memoryCount, len(loaded))
	}
}

func TestAgentResponseDoesNotPersistAcrossSessionChange(t *testing.T) {
	a, sub, request := createMainOwnedTestRequest(t)
	sub.lifecycleMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{
			TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "option A",
		})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		loaded, err := loadAgentRequests(a.sessionDir)
		if err != nil {
			sub.lifecycleMu.Unlock()
			t.Fatal(err)
		}
		if got := loaded[request.CorrelationID]; got != nil && got.Response != nil {
			break
		}
		if time.Now().After(deadline) {
			sub.lifecycleMu.Unlock()
			t.Fatal("response was not durably prepared")
		}
		time.Sleep(time.Millisecond)
	}
	a.admissionPaused.Store(true)
	a.admissionEpoch.Add(1)
	a.resetAgentRequests(nil)
	newSessionDir := t.TempDir()
	a.sessionDir = newSessionDir
	sub.lifecycleMu.Unlock()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "session change") {
		t.Fatalf("error = %v", err)
	}
	if _, err := loadAgentRequests(newSessionDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(newSessionDir, "subagents", "agent-requests.json")); !os.IsNotExist(err) {
		t.Fatalf("new session request ledger stat error = %v", err)
	}
}

func TestAgentResponseExpiresAndOrphansChangedSourceAttempt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*MainAgent, *DurableAgentRequest)
		want  string
		state string
	}{
		{name: "changed attempt", setup: func(a *MainAgent, request *DurableAgentRequest) {
			a.subs.mu.Lock()
			a.subs.taskRecords[request.SourceTaskID].Attempt++
			a.subs.mu.Unlock()
		}, want: "no longer current", state: "orphaned"},
		{name: "terminal source", setup: func(a *MainAgent, request *DurableAgentRequest) {
			a.subs.mu.Lock()
			a.subs.taskRecords[request.SourceTaskID].State = string(SubAgentStateCancelled)
			a.subs.mu.Unlock()
		}, want: "source task is terminal", state: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sub, request := createMainOwnedTestRequest(t)
			tc.setup(a, request)
			_, err := a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "answer"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			a.subs.mu.RLock()
			state := a.subs.agentRequests[request.CorrelationID].State
			a.subs.mu.RUnlock()
			if state != tc.state {
				t.Fatalf("state = %q, want %q", state, tc.state)
			}
		})
	}
}

func TestPeerNoticeUsesCommonOwnerAndCurrentAttempts(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	source := newPeerTestSubAgent(t, a, "worker-a", "task-a")
	target := newPeerTestSubAgent(t, a, "worker-b", "task-b")
	sourceCtx := tools.WithTaskID(tools.WithAgentID(context.Background(), source.instanceID), source.taskID)
	handle, err := a.NotifyPeerMessage(sourceCtx, tools.AgentPeerNoticeRequest{
		TargetTaskID: target.taskID, Message: "schema v1 is ready", Kind: "dependency_update",
	})
	if err != nil {
		t.Fatalf("NotifyPeerMessage: %v", err)
	}
	if handle.TaskID != target.taskID || handle.AgentID != target.instanceID {
		t.Fatalf("handle = %#v", handle)
	}
	select {
	case input := <-target.inputCh:
		if input.Mailbox == nil || input.Mailbox.MessageType != "notice" || input.Mailbox.CorrelationID != "" || input.Mailbox.SourceTaskID != source.taskID || input.Mailbox.SourceAttempt != 1 || input.Mailbox.TargetTaskID != target.taskID || input.Mailbox.TargetAttempt != 1 {
			t.Fatalf("notice mailbox = %#v", input.Mailbox)
		}
	default:
		t.Fatal("target did not receive peer notice")
	}
	loaded, err := loadAgentRequests(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("peer notice unexpectedly created request ledger: %#v", loaded)
	}
}

func TestPeerNoticeRejectsWhileSessionTransitionIsPaused(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	source := newPeerTestSubAgent(t, a, "worker-a", "task-a")
	target := newPeerTestSubAgent(t, a, "worker-b", "task-b")
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), source.instanceID), source.taskID)
	a.admissionPaused.Store(true)

	if _, err := a.NotifyPeerMessage(ctx, tools.AgentPeerNoticeRequest{TargetTaskID: target.taskID, Message: "old-session data"}); err == nil || !strings.Contains(err.Error(), "session transition") {
		t.Fatalf("NotifyPeerMessage error = %v, want transition rejection", err)
	}
	select {
	case input := <-target.inputCh:
		t.Fatalf("target received peer notice during transition: %#v", input)
	default:
	}

	a.admissionPaused.Store(false)
	if _, err := a.NotifyPeerMessage(ctx, tools.AgentPeerNoticeRequest{TargetTaskID: target.taskID, Message: "current-session data"}); err != nil {
		t.Fatalf("NotifyPeerMessage after transition: %v", err)
	}
}

func TestPeerRoutingRejectsNonSiblingTerminalAndRehydrate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	source := newPeerTestSubAgent(t, a, "worker-a", "task-a")
	target := newPeerTestSubAgent(t, a, "worker-b", "task-b")
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), source.instanceID), source.taskID)

	a.subs.mu.Lock()
	a.subs.taskRecords[target.taskID].OwnerTaskID = "different-owner"
	a.subs.mu.Unlock()
	if _, err := a.NotifyPeerMessage(ctx, tools.AgentPeerNoticeRequest{TargetTaskID: target.taskID, Message: "data"}); err == nil || !strings.Contains(err.Error(), "not a sibling") {
		t.Fatalf("non-sibling error = %v", err)
	}

	a.subs.mu.Lock()
	a.subs.taskRecords[target.taskID].OwnerTaskID = ""
	a.subs.taskRecords[target.taskID].State = string(SubAgentStateCompleted)
	a.subs.mu.Unlock()
	if _, err := a.NotifyPeerMessage(ctx, tools.AgentPeerNoticeRequest{TargetTaskID: target.taskID, Message: "data"}); err == nil || !strings.Contains(err.Error(), "non-terminal") {
		t.Fatalf("terminal target error = %v", err)
	}

	a.subs.mu.Lock()
	a.subs.taskRecords[target.taskID].State = string(SubAgentStateRunning)
	delete(a.subs.subAgents, target.instanceID)
	a.subs.mu.Unlock()
	if _, err := a.NotifyPeerMessage(ctx, tools.AgentPeerNoticeRequest{TargetTaskID: target.taskID, Message: "data"}); err == nil || !strings.Contains(err.Error(), "no live worker") {
		t.Fatalf("missing live target error = %v", err)
	}
}
