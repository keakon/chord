package agent

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompletionAfterSiblingWritePreservesDeliveryOutcome(t *testing.T) {
	t.Parallel()
	for _, degraded := range []bool{false, true} {
		for _, blocked := range []bool{false, true} {
			t.Run(fmt.Sprintf("degraded=%t/blocked=%t", degraded, blocked), func(t *testing.T) {
				parent, sub := newMixedBatchTestSubAgent(t)
				summary := "Updated sample.go; no checks were run."
				limitation := "Integration tests were not run."
				args := map[string]any{
					"summary": summary, "remaining_limitations": []string{limitation}, "files_changed": []string{"reported.go"},
				}
				if degraded {
					sub.turn.SubAgentCompletionRecoveryCount = 1
					args["result"] = map[string]any{"value": 1}
				}
				if blocked {
					args["blocked_reason"] = "The required input is unavailable."
				}
				sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
					mustJSONToolCall(t, "complete-report", tools.NameComplete, args),
					mustJSONToolCall(t, "sibling-write", "Dummy", map[string]any{"value": "sample"}),
				})}})
				assertNoAgentEvent(t, parent.eventCh)
				if sub.pendingComplete == nil && sub.pendingRejectedCompleteDegraded == nil {
					t.Fatal("completion did not wait for the sibling")
				}
				sub.handleToolResult(&toolResult{
					CallID: "sibling-write", Name: "Dummy", ArgsJSON: `{"value":"sample"}`, Result: "Written.", TurnID: sub.turn.ID,
					FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{
						Path: filepath.Join(sub.workDir, "sample.go"), Exists: true,
					}}},
				})

				wantEvent := EventAgentDone
				wantStatus := message.ToolStatusSuccess
				if blocked {
					wantEvent = EventAgentError
					wantStatus = message.ToolStatusError
				}
				event := waitForAgentEventType(t, parent.eventCh, wantEvent)
				var result *AgentResult
				if blocked {
					failure, ok := event.Payload.(*blockedCompletionError)
					if !ok {
						t.Fatalf("payload = %#v, want blocked completion", event.Payload)
					}
					result = failure.result
					if result.Envelope.BlockedReason != args["blocked_reason"] {
						t.Fatalf("blocker = %q, want %q", result.Envelope.BlockedReason, args["blocked_reason"])
					}
				} else {
					result = event.Payload.(*AgentResult)
				}
				if result.Summary != summary || result.Envelope == nil ||
					!reflect.DeepEqual(result.Envelope.ActualFilesChanged, []string{"sample.go"}) ||
					!reflect.DeepEqual(result.Envelope.ReportedFilesChanged, []string{"reported.go"}) ||
					!strings.Contains(strings.Join(result.Envelope.RemainingLimitations, "\n"), limitation) {
					t.Fatalf("completion = %#v, want summary, limitation and sibling write", result)
				}
				if degraded && !strings.Contains(strings.Join(result.Envelope.RemainingLimitations, "\n"), droppedTypedResultLimitation) {
					t.Fatal("degraded completion lost the typed-result limitation")
				}
				if sub.llmRequestInFlight.Load() || sub.turn.SubAgentResultContractRecoveryCount != 0 {
					t.Fatal("unchecked completion started an extra request or spent contract budget")
				}
				if receipt := toolResultForCall(t, sub, "complete-report"); receipt.ToolStatus != wantStatus {
					t.Fatalf("receipt = %#v, want status %q", receipt, wantStatus)
				}
				if _, dropped := message.RepairOrphanToolResults(sub.ctxMgr.Snapshot()); dropped != 0 {
					t.Fatal("completion broke tool-call pairing")
				}
				assertNoAgentEvent(t, parent.eventCh)
			})
		}
	}
}
