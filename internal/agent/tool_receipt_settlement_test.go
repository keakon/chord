package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type cancelReceiptTool struct {
	barrierRecordingTool
	cancel context.CancelFunc
	err    error
}

func (tool *cancelReceiptTool) Execute(context.Context, json.RawMessage) (string, error) {
	tool.ran.Store(true)
	tool.cancel()
	return "complete remote receipt", tool.err
}

func TestDispatchedReceiptSurvivesCancellation(t *testing.T) {
	for _, agentKind := range []string{"main", "sub"} {
		for _, outcome := range []struct {
			name         string
			err          error
			wantStatus   string
			wantRecovery string
		}{
			{name: "success", wantStatus: message.ToolStatusSuccess},
			{name: "reported error", err: tools.WrapExternalCallError(errors.New("request rejected"), tools.ExternalCallReportedError), wantStatus: message.ToolStatusError},
			{name: "unknown", err: tools.WrapExternalCallError(context.Canceled, tools.ExternalCallOutcomeUnknown), wantStatus: message.ToolStatusError, wantRecovery: message.ToolRecoveryStateOutcomeUnknown},
			{name: "unclassified cancellation", err: context.Canceled, wantStatus: message.ToolStatusError, wantRecovery: message.ToolRecoveryStateOutcomeUnknown},
		} {
			t.Run(agentKind+"/"+outcome.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					a := newTestMainAgent(t, t.TempDir())
					a.ruleset = nil
					a.newTurn("")
					turn := a.turn
					registry := a.tools
					ctxMgr := a.ctxMgr
					var sub *SubAgent
					if agentKind == "sub" {
						sub = newControllableTestSubAgent(t, a, "receipt")
						sub.setRuleset(nil)
						turn = sub.newTurn()
						registry, ctxMgr = sub.tools, sub.ctxMgr
					}
					turn.streamingToolExec = nil
					tool := &cancelReceiptTool{name: "sample_receipt", cancel: turn.Cancel, err: outcome.err}
					registry.Register(tool)
					call := message.ToolCall{ID: "receipt-call", Name: tool.Name(), Args: []byte(`{}`)}
					ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{call}})
					turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})
					batch := toolExecutionBatch{Calls: []message.ToolCall{call}}
					if sub == nil {
						a.promoteStreamingToolBatch(turn, batch)
					} else {
						turn.toolExecutionBatches = []toolExecutionBatch{batch}
						sub.startNextToolBatch(turn)
					}
					synctest.Wait()
					if !tool.ran.Load() {
						t.Fatal("tool did not execute")
					}
					if sub == nil {
						a.interruptCurrentTurnForReplacement()
					} else {
						sub.interruptCurrentTurnWithStatus(message.ToolStatusError, context.Canceled, false)
					}
					results := toolMessagesForCall(t, ctxMgr.Snapshot(), call.ID)
					if len(results) != 1 {
						t.Fatalf("terminal messages = %d, want 1", len(results))
					}
					if got := results[0]; got.ToolStatus != outcome.wantStatus || got.ToolRecoveryState != outcome.wantRecovery {
						t.Fatalf("terminal status=%q recovery=%q; want %q / %q", got.ToolStatus, got.ToolRecoveryState, outcome.wantStatus, outcome.wantRecovery)
					}
					if outcome.err == nil && !strings.Contains(results[0].Content, "complete remote receipt") {
						t.Fatalf("success receipt lost: %q", results[0].Content)
					}
					if turn.registerReadyToolResult(call.ID, &ToolResultPayload{CallID: call.ID}) {
						t.Fatal("late result reopened settled call")
					}
				})
			})
		}
	}
}

func TestSubInterruptedSpeculativeResultKeepsTerminal(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "success"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "receipt")
			sub.newTurn()
			call := message.ToolCall{ID: "spec-result", Name: tools.NameRead, Args: []byte(`{"path":"sample.txt"}`)}
			sub.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{call}})
			sub.turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args), AgentID: sub.instanceID})
			sub.turn.streamingToolExec = NewStreamingToolExecutor(sub.turn.ID, sub.turn.Ctx, nil,
				func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
					var err error
					if unknown {
						err = tools.WrapExternalCallError(errors.New("receipt unavailable"), tools.ExternalCallOutcomeUnknown)
					}
					return ToolExecutionResult{Result: "completed output", Payload: "completed output", Notes: []string{"sample note"}, EffectiveArgsJSON: string(call.Args)}, err
				})
			if !sub.turn.streamingToolExec.Start(call) {
				t.Fatal("speculative execution did not start")
			}
			waitForStreamingToolDone(t, sub.turn.streamingToolExec, call.ID)
			sub.interruptCurrentTurnWithStatus(message.ToolStatusError, context.Canceled, false)
			a.flushPersist()
			results := toolMessagesForCall(t, sub.GetMessages(), call.ID)
			if len(results) != 1 {
				t.Fatalf("terminal messages = %d, want 1", len(results))
			}
			if results[0].ToolPayload != "completed output" || len(results[0].ToolNotes) != 1 {
				t.Fatalf("result metadata lost: %#v", results[0])
			}
			if unknown {
				if results[0].ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown || !strings.Contains(results[0].Content, externalOutcomeTransportNote) {
					t.Fatalf("unknown classification lost: %#v", results[0])
				}
			} else if results[0].ToolStatus != message.ToolStatusSuccess {
				t.Fatalf("completed result lost: %#v", results[0])
			}
		})
	}
}

func TestCompletedReceiptAvailableWhileAppendHookWaits(t *testing.T) {
	for _, agentKind := range []string{"main", "sub"} {
		t.Run(agentKind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := newGateTestMainAgent(t)
				a.ruleset = nil
				a.newTurn("")
				turn, registry, ctxMgr := a.turn, a.tools, a.ctxMgr
				var sub *SubAgent
				if agentKind == "sub" {
					sub = newControllableTestSubAgent(t, a, "receipt")
					sub.setRuleset(nil)
					turn = sub.newTurn()
					registry, ctxMgr = sub.tools, sub.ctxMgr
				}
				turn.streamingToolExec = nil
				engine := gateTestHookEngineOf(a)
				engine.hasHooks = true
				started, release := make(chan struct{}), make(chan struct{})
				defer func() {
					close(release)
					synctest.Wait()
				}()
				engine.modify = func(env hook.Envelope) *hook.Result {
					if env.Point == hook.OnBeforeToolResultAppend {
						close(started)
						<-release
					}
					return nil
				}
				tool := &barrierRecordingTool{name: "sample_receipt"}
				registry.Register(tool)
				call := message.ToolCall{ID: "hook-receipt", Name: tool.Name(), Args: []byte(`{}`)}
				ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{call}})
				turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})
				batch := toolExecutionBatch{Calls: []message.ToolCall{call}}
				if sub == nil {
					a.promoteStreamingToolBatch(turn, batch)
				} else {
					turn.toolExecutionBatches = []toolExecutionBatch{batch}
					sub.startNextToolBatch(turn)
				}
				synctest.Wait()
				select {
				case <-started:
				default:
					t.Fatal("append hook did not start")
				}
				if sub == nil {
					a.interruptCurrentTurnForReplacement()
				} else {
					sub.interruptCurrentTurnWithStatus(message.ToolStatusError, context.Canceled, false)
				}
				results := toolMessagesForCall(t, ctxMgr.Snapshot(), call.ID)
				if len(results) != 1 || results[0].ToolStatus != message.ToolStatusSuccess {
					t.Fatalf("receipt behind hook lost: %#v", results)
				}
				if !strings.Contains(results[0].Content, "ok") {
					t.Fatalf("output lost: %q", results[0].Content)
				}
			})
		})
	}
}
