package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestRestoreSessionPreservesResumedTaskAttempt(t *testing.T) {
	for _, previous := range []SubAgentState{SubAgentStateCompleted, SubAgentStateFailed} {
		for _, current := range []SubAgentState{SubAgentStateRunning, SubAgentStateCompleted, SubAgentStateFailed} {
			t.Run(string(previous)+"/"+string(current), func(t *testing.T) {
				const taskID, instanceID = "task-resumed", "restorer-current"
				projectRoot := t.TempDir()
				sessionDir := testProjectSessionDir(t, projectRoot, "resumed-attempt")
				rm := recovery.NewRecoveryManager(sessionDir)
				if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "Continue the task"}); err != nil {
					t.Fatal(err)
				}
				if err := rm.PersistMessage(instanceID, message.Message{Role: "user", Content: "Check the current result"}); err != nil {
					t.Fatal(err)
				}
				if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
					CreatedAt: time.Now(),
					ActiveAgents: []recovery.AgentSnapshot{{
						InstanceID:   instanceID,
						TaskID:       taskID,
						AgentDefName: "restorer",
						TaskDesc:     "Check the current result",
						State:        string(SubAgentStateRunning),
						LastSummary:  "Earlier progress",
					}},
				}); err != nil {
					t.Fatal(err)
				}
				rm.Close()

				earlier := &TaskSettlement{
					TaskID: taskID, Attempt: 1, TerminalRevision: 4,
					Outcome: string(previous), Summary: "Earlier result", SettledAt: time.Now(),
					Completion: &CompletionEnvelope{Summary: "Earlier result"},
				}
				if err := appendTaskSettlement(sessionDir, earlier); err != nil {
					t.Fatal(err)
				}
				record := &DurableTaskRecord{
					TaskID: taskID, AgentDefName: "restorer", TaskDesc: "Check the current result",
					State: string(current), ResumePolicy: durableTaskResumePolicy(current),
					LatestInstanceID: instanceID, InstanceHistory: []string{"restorer-earlier", instanceID},
					Attempt: 2, LifecycleRevision: 8, LastSummary: "Current result",
					CreatedAt: time.Now(), UpdatedAt: time.Now(),
				}
				if isTerminalSubAgentState(current) {
					settlement := &TaskSettlement{
						TaskID: taskID, Attempt: 2, TerminalRevision: 8,
						Outcome: string(current), Summary: "Current result", SettledAt: time.Now(),
						Completion: &CompletionEnvelope{Summary: "Current result"},
					}
					if err := appendTaskSettlement(sessionDir, settlement); err != nil {
						t.Fatal(err)
					}
					record.LatestSettlement = settlement
					record.SettlementDurable = true
					record.LastCompletion = settlement.Completion
				}
				if err := persistDurableTaskRecords(sessionDir, map[string]*DurableTaskRecord{taskID: record}); err != nil {
					t.Fatal(err)
				}

				a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
				a.SetAgentConfigs(map[string]*config.AgentConfig{
					"restorer": {Name: "restorer", Mode: "subagent", Models: map[string][]string{"default": {"sample/test-model"}}},
				})
				a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
				if _, err := a.restoreSessionState(sessionDir); err != nil {
					t.Fatal(err)
				}
				wantState := current
				if wantState == SubAgentStateRunning {
					wantState = SubAgentStateIdle
				}
				onDisk, err := loadDurableTaskRecords(sessionDir)
				if err != nil {
					t.Fatal(err)
				}
				for _, got := range []*DurableTaskRecord{a.taskRecordByTaskID(taskID), onDisk[taskID]} {
					if got == nil || got.Attempt != 2 || got.LifecycleRevision < 8 || got.State != string(wantState) || got.LastSummary != "Current result" {
						t.Fatalf("restored record = %#v, want current attempt, state and summary", got)
					}
					if current == SubAgentStateRunning {
						if got.LatestSettlement != nil || got.LastCompletion != nil {
							t.Fatalf("running attempt inherited an earlier result: %#v", got)
						}
					} else if got.LatestSettlement == nil || got.LatestSettlement.Attempt != 2 || got.LatestSettlement.Outcome != string(current) || got.LastCompletion == nil || got.LastCompletion.Summary != "Current result" {
						t.Fatalf("restored result = %#v, want current settlement", got)
					}
				}
			})
		}
	}
}
