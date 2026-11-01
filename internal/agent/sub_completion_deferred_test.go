package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompletionRevalidatesAfterChildReactivation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		degraded      bool
		observedWrite bool
	}{
		{name: "ordinary"},
		{name: "degraded", degraded: true},
		{name: "ordinary with observed write", observedWrite: true},
		{name: "degraded with observed write", degraded: true, observedWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			configureNestedDelegationTestRuntime(parent, 2)
			sub.instanceID = "worker-parent"
			sub.setState(SubAgentStateRunning, "checking")
			parent.subs.mu.Lock()
			parent.subs.subAgents[sub.instanceID] = sub
			parent.subs.mu.Unlock()
			parent.syncTaskRecordFromSub(sub, "")

			child := newControllableTestSubAgent(t, parent, "child-1")
			child.instanceID = "worker-child"
			child.ownerAgentID = sub.instanceID
			child.ownerTaskID = sub.taskID
			child.depth = 2
			child.joinToOwner = true
			child.setState(SubAgentStateRunning, "checking")
			parent.subs.mu.Lock()
			delete(parent.subs.subAgents, "worker-1")
			parent.subs.subAgents[child.instanceID] = child
			parent.subs.mu.Unlock()
			parent.syncTaskRecordFromSub(child, "")
			if err := parent.acquireSubAgentSlot(child); err != nil {
				t.Fatalf("acquire child slot: %v", err)
			}

			provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "resumed-complete", tools.NameComplete, map[string]any{
					"summary": "Inspection complete.",
				}),
			})}}}}
			parent.llmFactory = func(string, []string, string) *llm.Client {
				return testLLMClientWithProvider(provider)
			}
			if tc.observedWrite {
				writeCall := mustJSONToolCall(t, "write-1", tools.NameWrite, map[string]any{"path": "sample.go", "content": "package sample"})
				fileState := &message.ToolFileState{Writes: []message.TrackedFileState{{
					Path: filepath.Join(sub.workDir, "sample.go"), Exists: true,
				}}}
				files, incomplete := sub.recordTaskToolChanges(&toolResult{
					CallID: writeCall.ID, Name: writeCall.Name, ArgsJSON: string(writeCall.Args), FileState: fileState,
				}, false)
				// Rehydration reads durable file observations, not the old runtime's map.
				for _, msg := range []message.Message{
					{Role: message.RoleAssistant, ToolCalls: convertCalls([]messageToolCall{writeCall})},
					{
						Role: message.RoleTool, ToolCallID: writeCall.ID, Content: "File written.", ToolStatus: message.ToolStatusSuccess,
						FileState: fileState, ToolChangedPaths: files, FileAttributionIncomplete: incomplete,
					},
				} {
					sub.ctxMgr.Append(msg)
					sub.persistMessageAsync(msg, "tool fixture", nil)
				}
				parent.flushPersist()
			}
			args := map[string]any{"summary": "Initial inspection complete."}
			if tc.degraded {
				sub.turn.SubAgentCompletionRecoveryCount = 1
				args["result"] = map[string]any{"value": 1}
			}
			sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "initial-complete", tools.NameComplete, args),
			})}})

			assertNoAgentEvent(t, parent.eventCh)
			pending := sub.PendingCompleteIntent()
			if sub.State() != SubAgentStateWaitingDescendant || pending == nil || pending.Summary != "Initial inspection complete." || pending.Envelope == nil {
				t.Fatalf("state=%q pending=%#v, want the completion deferred for the child", sub.State(), pending)
			}
			if tc.degraded && !strings.Contains(strings.Join(pending.Envelope.RemainingLimitations, "\n"), droppedTypedResultLimitation) {
				t.Fatalf("pending limitations = %#v, want the typed-result drop disclosed", pending.Envelope.RemainingLimitations)
			}
			if rec := parent.taskRecordByTaskID(sub.taskID); rec == nil || !rec.RuntimeParked ||
				rec.PendingCompletion == nil || rec.PendingCompletion.Summary != "Initial inspection complete." {
				t.Fatalf("task record = %#v, want the parked completion intent", rec)
			}

			child.setState(SubAgentStateCompleted, "child complete")
			parent.syncTaskRecordFromSub(child, "")
			if !parent.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
				AgentID: child.instanceID, TaskID: child.taskID,
				OwnerAgentID: sub.instanceID, OwnerTaskID: sub.taskID,
				Kind: SubAgentMailboxKindCompleted, Summary: "Child inspection complete.",
			}) {
				t.Fatal("child completion did not reactivate the parent")
			}
			var result *AgentResult
			deadline := time.After(2 * time.Second)
			for result == nil {
				select {
				case evt := <-parent.eventCh:
					switch evt.Type {
					case EventSubAgentRequestBoundary:
					case EventAgentDone:
						result = evt.Payload.(*AgentResult)
					default:
						t.Fatalf("unexpected event %q: %#v", evt.Type, evt.Payload)
					}
				case <-deadline:
					t.Fatal("timed out waiting for the rehydrated parent's completion")
				}
			}
			resumed := parent.subAgentByTaskID(sub.taskID)
			if resumed == nil || resumed == sub || resumed.PendingCompleteIntent() != nil {
				t.Fatalf("resumed runtime = %#v, want a rehydrated runtime without a stale completion", resumed)
			}
			completeResult := toolResultForCall(t, resumed, "resumed-complete")
			if completeResult.ToolStatus != message.ToolStatusSuccess {
				t.Fatalf("resumed completion = %#v, want success without a check", completeResult)
			}
			if resumed.turn.SubAgentCompletionRecoveryCount != 0 || resumed.turn.SubAgentResultContractRecoveryCount != 0 {
				t.Fatalf("budgets = %#v, want no completion repair in the new turn", resumed.turn)
			}
			if result.Summary != "Inspection complete." || result.Envelope == nil {
				t.Fatalf("final result = %#v, want the fresh completion summary", result)
			}
			if tc.observedWrite && strings.Join(result.Envelope.ActualFilesChanged, ",") != "sample.go" {
				t.Fatalf("actual files = %q, want the original observation after the child completed", result.Envelope.ActualFilesChanged)
			}
			seen, _ := provider.snapshot()
			if len(seen) != 1 {
				t.Fatalf("request count = %d, want 1", len(seen))
			}
			lastInput := seen[0][len(seen[0])-1].Content
			if !strings.Contains(lastInput, "Initial inspection complete.") || !strings.Contains(lastInput, "Child inspection complete.") {
				t.Fatalf("resumed input = %q, want the pending intent and child report", lastInput)
			}
		})
	}
}

func TestCompletionProcessesNewInputBeforeResubmission(t *testing.T) {
	t.Parallel()
	for _, degraded := range []bool{false, true} {
		name := "ordinary"
		if degraded {
			name = "degraded"
		}
		t.Run(name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			provider := installContractFollowUpLLM(t, sub)
			provider.calls[0].resp = &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "resumed-complete", tools.NameComplete, map[string]any{
					"summary": "Additional inspection complete.",
				}),
			})}
			sub.enqueueUserMessage(pendingUserMessage{Content: "Inspect the additional input.", FromUser: true})
			args := map[string]any{"summary": "Initial inspection complete."}
			if degraded {
				sub.turn.SubAgentCompletionRecoveryCount = 1
				args["result"] = map[string]any{"value": 1}
			}
			sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "initial-complete", tools.NameComplete, args),
			})}})

			assertNoAgentEvent(t, parent.eventCh)
			deferred := toolResultForCall(t, sub, "initial-complete")
			if deferred.ToolStatus != message.ToolStatusSuccess || !strings.Contains(deferred.Content, "received new user input") {
				t.Fatalf("initial receipt = %#v, want successful deferral for the new input", deferred)
			}
			if sub.PendingCompleteIntent() != nil {
				t.Fatal("new input left a stale completion intent")
			}
			resumed := waitForSubAgentLLMResult(t, sub, time.Second)
			if resumed.err != nil {
				t.Fatalf("resumed request: %v", resumed.err)
			}
			seen, _ := provider.snapshot()
			if len(seen) != 1 || len(seen[0]) == 0 || seen[0][len(seen[0])-1].Content != "Inspect the additional input." {
				t.Fatalf("requests = %#v, want the queued input before resubmission", seen)
			}
			sub.handleLLMResponse(resumed)
			result := waitForAgentEventType(t, parent.eventCh, EventAgentDone).Payload.(*AgentResult)
			if result.Summary != "Additional inspection complete." || result.Envelope == nil {
				t.Fatalf("final result = %#v, want the fresh completion summary", result)
			}
		})
	}
}
