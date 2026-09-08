package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestRestoreTerminalNotificationAfterMailboxCompaction(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(fmt.Sprintf("current-delivered=%t", delivered), func(t *testing.T) {
			const taskID, instanceID = "task-resumed", "agent-current"
			projectRoot, sessionDir, previous := seedSettledSnapshotConflictSession(t, taskID, instanceID)
			current := cloneTaskSettlement(previous)
			current.Attempt = 2
			current.TerminalRevision = 6
			current.Summary = "Current result"
			current.Completion = &CompletionEnvelope{Summary: current.Summary}
			if err := appendTaskSettlement(sessionDir, current); err != nil {
				t.Fatal(err)
			}
			record := &DurableTaskRecord{
				TaskID: taskID, AgentDefName: "restorer", LatestInstanceID: instanceID,
				State: current.Outcome, Attempt: 2, LifecycleRevision: 6,
				LastSummary: current.Summary, LastCompletion: current.Completion,
				LatestSettlement: current, SettlementDurable: true,
				LastMailboxID: "mail-previous", RuntimeParked: true, UpdatedAt: time.Now(),
			}
			if delivered {
				// The journal is authoritative even if its registry mirror was
				// not persisted before the notification was consumed.
				record.LatestSettlement = nil
				record.SettlementDurable = false
			}
			if err := persistDurableTaskRecords(sessionDir, map[string]*DurableTaskRecord{taskID: record}); err != nil {
				t.Fatal(err)
			}
			msgs := []SubAgentMailboxMessage{{
				MessageID: "mail-previous", AgentID: "agent-previous", TaskID: taskID, Attempt: 1,
				Kind: SubAgentMailboxKindCompleted, Summary: previous.Summary, Consumed: true,
			}}
			if delivered {
				msgs = append(msgs, SubAgentMailboxMessage{
					MessageID: "mail-current", AgentID: instanceID, TaskID: taskID, Attempt: 2,
					Kind: SubAgentMailboxKindCompleted, Summary: current.Summary, Completion: current.Completion, Consumed: true,
				})
			}
			for i := range mailboxCompactionThreshold {
				msgs = append(msgs, SubAgentMailboxMessage{
					MessageID: fmt.Sprintf("mail-progress-%d", i), TaskID: "task-progress",
					Kind: SubAgentMailboxKindProgress, Consumed: true,
				})
			}
			if err := rewriteMailboxLog(sessionDir, msgs); err != nil {
				t.Fatal(err)
			}
			a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
			for _, id := range []string{"mail-previous", "mail-current"} {
				if err := a.appendSubAgentMailboxAck(SubAgentMailboxAckRecord{MessageID: id, Outcome: "consumed", AckedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			if err := compactSubAgentMailboxLogs(sessionDir, msgs); err != nil {
				t.Fatal(err)
			}
			compacted, err := loadSubAgentMailboxMessages(sessionDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, msg := range compacted {
				if msg.MessageID == "mail-previous" {
					t.Fatal("earlier attempt was not compacted")
				}
			}
			acks, err := loadSubAgentMailboxAcks(sessionDir)
			if err != nil {
				t.Fatal(err)
			}
			if _, kept := acks["mail-current"]; kept != delivered {
				t.Fatalf("current delivery ack retained = %t, want %t", kept, delivered)
			}
			a.SetAgentConfigs(map[string]*config.AgentConfig{
				"restorer": {Name: "restorer", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"sample/test-model"}}},
			})
			a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
			for range 2 {
				if _, err := a.restoreSessionState(sessionDir); err != nil {
					t.Fatal(err)
				}
				notices := restoredCompletedMailboxesForTask(a, taskID)
				if delivered {
					if len(notices) != 0 {
						t.Fatalf("delivered terminal notice repeated: %#v", notices)
					}
				} else if len(notices) != 1 || notices[0].Attempt != 2 || notices[0].Summary != current.Summary {
					t.Fatalf("missing current attempt's terminal notice: %#v", notices)
				}
			}
		})
	}
}

func TestRestoreStallAlertDoesNotCoverTaskFailure(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "terminal-notice")
	restoreFailedRiskAlertFixture(t, projectRoot, sessionDir)
	if err := rewriteMailboxLog(sessionDir, []SubAgentMailboxMessage{{
		MessageID: "mail-stall", AgentID: "agent-lost-failure", TaskID: "adhoc-lost-failure", Attempt: 1,
		Kind: SubAgentMailboxKindRiskAlert, Summary: "Waiting for activity",
	}}); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if _, err := a.restoreSessionState(sessionDir); err != nil {
		t.Fatal(err)
	}
	alerts := restoredRiskAlertsForTask(a, "adhoc-lost-failure")
	terminalCount := 0
	for _, alert := range alerts {
		if alert.Subtype == agentMessageSubtypeTaskFailure {
			terminalCount++
		}
	}
	if len(alerts) != 2 || terminalCount != 1 {
		t.Fatalf("stall alert suppressed the terminal failure: %#v", alerts)
	}
}

func TestRestoreDoesNotReplayExpiryWithdrawnByResponse(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "withdrawn-expiry")
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	configureNestedDelegationTestRuntime(a, 2)
	if err := a.recoveryManager().PersistMessage("main", message.Message{Role: "user", Content: "Continue the work"}); err != nil {
		t.Fatal(err)
	}
	sub := newControllableTestSubAgent(t, a, "task-waiting")
	// The correlated response revives this parked worker through rehydration,
	// which refuses scope-less records; carry the boundary here.
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.setState(SubAgentStateWaitingMain, "Choose an option")
	request, err := a.createAgentRequest(sub, tools.AgentRequestPayload{Reason: "Choose an option"})
	if err != nil {
		t.Fatal(err)
	}
	a.syncTaskRecordFromSub(sub, "")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("failed to park waiting worker")
	}
	before := a.taskRecordByTaskID(sub.taskID)
	reason := waitingMainExpiryClosedReasonPrefix + " (no reply within the wait limit)"
	prepared, durable := a.prepareWaitingMainExpiryAlert(nil, before, reason)
	if !durable {
		t.Fatal("expiry notice was not durably prepared")
	}
	handle, err := a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{
		TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "Choose option A",
	})
	if err != nil || !handle.Rehydrated {
		t.Fatalf("correlated response did not revive the task: %#v, %v", handle, err)
	}
	if state := a.settleDetachedTerminalTaskGuarded(sub.taskID, SubAgentStateCancelled, reason, reason, func(rec *DurableTaskRecord) bool {
		return rec.RuntimeParked && rec.State == string(SubAgentStateWaitingMain)
	}); state != "" {
		t.Fatalf("stale expiry won after the response: %q", state)
	}
	if err := a.Shutdown(time.Second); err != nil {
		t.Fatal(err)
	}

	restored := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	configureNestedDelegationTestRuntime(restored, 2)
	if _, err := restored.restoreSessionState(sessionDir); err != nil {
		t.Fatal(err)
	}
	for _, alert := range restoredRiskAlertsForTask(restored, sub.taskID) {
		if alert.MessageID == prepared.MessageID || strings.Contains(alert.Summary, waitingMainExpiryClosedReasonPrefix) {
			t.Fatalf("uncommitted expiry was replayed: %#v", alert)
		}
	}
	if rec := restored.taskRecordByTaskID(sub.taskID); rec == nil || rec.State == string(SubAgentStateCancelled) {
		t.Fatalf("responded task restored as cancelled: %#v", rec)
	}
	if requests, err := loadAgentRequests(sessionDir); err != nil || requests[request.CorrelationID].State != "responded" {
		t.Fatalf("request lost its response: %#v, %v", requests, err)
	}
}

func TestExpiryNotificationRequiresItsOwnSettlement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	msg := a.buildWaitingMainExpiryAlertMailbox(nil, &DurableTaskRecord{
		TaskID: "task", LatestInstanceID: "agent", Attempt: 2,
	}, waitingMainExpiryClosedReasonPrefix+" (wait limit)")
	msg.Attempt = 2
	settlement := &TaskSettlement{TaskID: "task", Attempt: 2, Outcome: string(SubAgentStateCancelled), Summary: "Stopped by owner"}
	if terminalMailboxMatchesSettlement(*msg, settlement) {
		t.Fatal("an explicit stop must not commit a prepared expiry notice")
	}
	settlement.Summary = msg.Summary
	if !terminalMailboxMatchesSettlement(*msg, settlement) {
		t.Fatal("matching expiry settlement did not cover the notice")
	}
	settlement.Attempt = 1
	if terminalMailboxMatchesSettlement(*msg, settlement) {
		t.Fatal("an earlier attempt must not commit a prepared expiry notice")
	}
}
