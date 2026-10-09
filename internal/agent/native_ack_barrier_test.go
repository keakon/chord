package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestNativeReceiptPreflightWaitsForAcknowledgement(t *testing.T) {
	for _, agentKind := range []string{"main", "sub"} {
		t.Run(agentKind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := newTestMainAgent(t, t.TempDir())
				agentID := identity.MainAgentID
				var sub *SubAgent
				if agentKind == "sub" {
					sub = newControllableTestSubAgent(t, a, "task-1")
					sub.turn = sub.newTurn()
					agentID = sub.taskID
				} else {
					a.newTurn()
				}
				journal := recovery.NativeRequestJournal{SessionDir: a.SessionDir(), AgentID: agentID}
				id, err := journal.Begin(llm.NativeRequestRecord{Target: "sample/test-model"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				receipt := &message.NativeToolHistory{RequestIDs: []string{id}}
				if err := journal.Finish(id, message.NativeRequestCompleted, &message.Response{Content: "Complete", NativeTools: receipt}, nil); err != nil {
					t.Fatal(err)
				}
				started, release := make(chan struct{}), make(chan struct{})
				a.persistAsyncAfter(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "Sample input"}, func(error) { close(started); <-release })
				<-started
				released := false
				defer func() {
					if !released {
						close(release)
					}
					a.flushPersist()
				}()
				var policy *llm.NativeToolPolicy
				if sub != nil {
					sub.turn.SubAgentTerminalRecoveryCount = 1
					sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, resp: &message.Response{Content: "Complete", StopReason: "stop", NativeTools: receipt}})
					policy = sub.nativeRequestPolicy(sub.turn)
				} else {
					a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: &LLMResponsePayload{Content: "Complete", StopReason: "stop", NativeTools: receipt}})
					if a.turn != nil {
						t.Fatal("receipt persistence blocked the idle transition")
					}
					a.newTurn()
					policy = a.nativeRequestPolicy(a.turn)
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancelled := make(chan error, 1)
				go func() { cancelled <- policy.Preflight(ctx) }()
				synctest.Wait()
				cancel()
				if err := <-cancelled; !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled preflight = %v", err)
				}
				done := make(chan error, 1)
				go func() { done <- policy.Preflight(t.Context()) }()
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("preflight bypassed pending receipt: %v", err)
				default:
				}
				close(release)
				released = true
				if err := <-done; err != nil {
					t.Fatalf("acknowledged preflight = %v", err)
				}
				if _, err := journal.Check(); err != nil {
					t.Fatal(err)
				}
				history, err := a.recoveryManager().LoadMessages(identity.MainAgentID)
				if sub != nil {
					history, err = a.recoveryManager().LoadMessages(sub.instanceID)
				}
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, msg := range history {
					if msg.NativeTools != nil {
						found = true
						if msg.NativeTools.OutcomeUnknown {
							t.Fatal("completed execution became outcome_unknown")
						}
					}
				}
				if !found {
					t.Fatal("journal released before canonical receipt persistence")
				}
			})
		})
	}
}

func TestNativeReceiptPreflightFailsClosedOnAcknowledgementError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: &LLMResponsePayload{Content: "Complete", StopReason: "stop", NativeTools: &message.NativeToolHistory{RequestIDs: []string{"missing-request"}}}})
	a.newTurn()
	policy := a.nativeRequestPolicy(a.turn)
	err := policy.Preflight(t.Context())
	if err == nil || !strings.Contains(err.Error(), "persist native receipt") || llm.IsNativeToolError(err) {
		t.Fatalf("persistence failure misclassified: %v", err)
	}
}
