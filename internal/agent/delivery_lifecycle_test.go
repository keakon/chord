package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestDeliveryAcknowledgmentSettlesCanonicalHistory(t *testing.T) {
	for _, worker := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("worker=%t/failure=%t", worker, failure), func(t *testing.T) {
				a := newReadyTestMainAgent(t)
				manager := recovery.NewRecoveryManager(a.sessionDir)
				a.installRecoveryManager(manager)
				a.tools.Register(tools.PresentTool{BaseDir: a.contentRoot})
				a.persist.closeUntil(nil)
				<-a.persist.done
				a.persist = newPersistencePump(2)
				received := make(chan persistEntry, 1)
				release := make(chan struct{})
				a.persist.start(func(entry persistEntry) {
					received <- entry
					<-release
					var err error
					if failure {
						err = errors.New("storage unavailable")
					} else {
						err = manager.PersistMessageDurable(entry.agentID, entry.msg)
					}
					entry.after(err)
				})
				released := false
				defer func() {
					if !released {
						close(release)
					}
				}()
				owner := identity.MainAgentID
				contextManager := a.ctxMgr
				a.newTurn("")
				turn := a.turn
				var sub *SubAgent
				if worker {
					sub = newControllableTestSubAgent(t, a, "delivery-task")
					turn = sub.newTurn()
					contextManager = sub.ctxMgr
					owner = sub.instanceID
				}
				// A sentinel pending call keeps this handler test from scheduling an LLM.
				turn.PendingToolCalls.Store(2)
				contextManager.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call", Name: tools.NamePresent, Args: json.RawMessage(`{"files":[{"path":"report.txt"}]}`)}}})
				file := message.DeliveredFile{ID: "delivery", DisplayName: "report.txt"}
				if worker {
					sub.handleToolResult(&toolResult{CallID: "call", Name: tools.NamePresent, TurnID: turn.ID, ArgsJSON: `{"files":[{"path":"report.txt"}]}`, DeliveredFiles: []message.DeliveredFile{file}, composed: &composedToolResultTexts{Display: "display", Context: "canonical"}})
				} else {
					a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{CallID: "call", Name: tools.NamePresent, TurnID: turn.ID, ArgsJSON: `{"files":[{"path":"report.txt"}]}`, DeliveredFiles: []message.DeliveredFile{file}, composedTexts: &composedToolResultTexts{Display: "display", Context: "canonical"}}})
				}
				<-received
				if !turn.deliveryPending.Load() || turn.PendingToolCalls.Load() != 2 || len(message.CollectDeliveredFiles(contextManager.Snapshot())) != 0 {
					t.Fatal("delivery published or barrier released before ack")
				}
				close(release)
				released = true
				if worker {
					sub.handleToolResult(<-sub.toolCh)
				} else {
					for {
						evt := <-a.eventCh
						if evt.Type == EventToolResult {
							a.handleToolResult(evt)
							break
						}
					}
				}
				snapshot := contextManager.Snapshot()
				last := snapshot[len(snapshot)-1]
				if turn.deliveryPending.Load() {
					t.Fatal("delivery barrier not released")
				}
				if failure {
					if len(message.CollectDeliveredFiles(snapshot)) != 0 || last.ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown || turn.Ctx.Err() == nil {
						t.Fatal("failed acknowledgment was not settled as unknown")
					}
				} else {
					if last.Content != "canonical" || len(last.DeliveredFiles) != 1 || turn.PendingToolCalls.Load() != 1 {
						t.Fatal("canonical success was not appended exactly once")
					}
					persisted, err := manager.LoadMessages(owner)
					if err != nil {
						t.Fatal(err)
					}
					if len(persisted) != 1 || !reflect.DeepEqual(persisted[0].DeliveredFiles, last.DeliveredFiles) {
						t.Fatal("live and restored delivery disagree")
					}
				}
			})
		}
	}
}

func TestDeliverySurvivesRepeatedCompactionAndReload(t *testing.T) {
	a := newReadyTestMainAgent(t)
	manager := recovery.NewRecoveryManager(a.sessionDir)
	a.installRecoveryManager(manager)
	a.newTurn("")
	first := message.DeliveredFile{ID: "first", DisplayName: "first.txt"}
	second := message.DeliveredFile{ID: "second", DisplayName: "second.txt"}
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "create reports"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "one", Name: tools.NamePresent, Args: json.RawMessage(`{"files":[{"path":"first.txt"}]}`)}}})
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, ToolCallID: "one", DeliveredFiles: []message.DeliveredFile{first}})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "two", Name: tools.NamePresent, Args: json.RawMessage(`{"files":[{"path":"second.txt"}]}`)}}})
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, ToolCallID: "two", DeliveredFiles: []message.DeliveredFile{second}})
	for index := 1; index <= 2; index++ {
		split := 3
		if index == 2 {
			split = len(a.ctxMgr.Snapshot())
		}
		draft := &compactionDraft{HeadSplit: split, Index: index, AbsHistoryPath: filepath.Join(a.sessionDir, fmt.Sprintf("history-%d.md", index)), NewMessages: []message.Message{{Role: message.RoleUser, Content: "summary", IsCompactionSummary: true}}, PlanID: uint64(index), Target: compactionTarget{sessionEpoch: a.sessionEpoch}}
		if err := a.applyCompactionDraft(draft); err != nil {
			t.Fatal(err)
		}
		a.flushPersist()
		restored, err := a.recoveryManager().LoadMessages(identity.MainAgentID)
		if err != nil {
			t.Fatal(err)
		}
		if got := message.CollectDeliveredFiles(restored); !reflect.DeepEqual(got, []message.DeliveredFile{first, second}) {
			t.Fatalf("round %d lost or duplicated delivery: %#v", index, got)
		}
		if index == 1 && len(restored[0].CompactionDeliveredFiles) != 1 {
			t.Fatal("retained tail copied into checkpoint")
		}
		fresh := ctxmgr.NewManager(8192, 0)
		for _, msg := range restored {
			fresh.Append(msg)
		}
		a.ctxMgr = fresh
	}
}
