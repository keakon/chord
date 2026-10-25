package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/message"
)

func newExternalInputTestWorker(t *testing.T) (*MainAgent, *externalWorker) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-input-1",
		taskID: "task-input", taskDesc: "Sample task", ctx: context.Background(),
		resultSchemaJSON: json.RawMessage(externalResultContractSchema),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.setState(SubAgentStateRunning, "Sample running")
	w.injectInitialInput("Sample input")
	w.inputs[0].State = message.ToolRecoveryStateOutcomeUnknown
	a.setTaskRecords(map[string]*DurableTaskRecord{
		w.taskID: w.buildTaskRecord(nil, "", 0, time.Now()),
	})
	a.subs.mu.Lock()
	a.subs.subAgents[w.instanceID] = w
	a.subs.mu.Unlock()
	t.Cleanup(func() { a.subs.removeRuntime(w.instanceID) })
	return a, w
}

func TestExternalResultSealsInputOnEveryTerminalExit(t *testing.T) {
	for _, scenario := range []string{"contract", "failed", "duplicate", "permission", "tool", "message persistence", "input persistence", "success"} {
		t.Run(scenario, func(t *testing.T) {
			a, w := newExternalInputTestWorker(t)
			event := externalclaude.Event{Type: externalclaude.TypeResult, Result: "Sample result"}
			switch scenario {
			case "failed":
				event.IsError = true
			case "duplicate":
				w.resultSeen = true
			case "permission":
				w.permissionRequests["permission-1"] = externalPermissionRequest{cancel: func() {}}
			case "tool":
				w.toolNames["tool-1"] = "Read"
				w.toolBindings["tool-1"] = &externalToolBinding{started: true}
			case "message persistence":
				if err := a.recoveryManager().Close(); err != nil {
					t.Fatal(err)
				}
			case "input persistence":
				if err := os.MkdirAll(durableTaskRegistryPath(a.currentRecoveryOwnership().dir), 0o700); err != nil {
					t.Fatal(err)
				}
			case "success":
				w.resultSchema = nil
			}
			stop, next := w.handleExternalResult(event)
			if !stop || next.ID != "" || !w.inputClosed {
				t.Fatalf("terminal result left input open: stop=%t next=%+v closed=%t", stop, next, w.inputClosed)
			}
			if scenario == "success" {
				if err := w.getResultError(); err != nil {
					t.Fatal(err)
				}
			} else if w.getResultError() == nil {
				t.Fatal("expected the terminal failure")
			}
			if scenario == "message persistence" || scenario == "input persistence" {
				return
			}
			deferred, err := w.enqueueExternalInput("Sample correction", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !deferred || w.inputs[len(w.inputs)-1].State != externalInputQueued {
				t.Fatalf("follow-up was not deferred: deferred=%t inputs=%+v", deferred, w.inputs)
			}
			records, err := loadDurableTaskRecords(a.currentRecoveryOwnership().dir)
			if err != nil || records[w.taskID].ExternalSession.Inputs[len(w.inputs)-1].State != externalInputQueued {
				t.Fatalf("follow-up was not durably queued: records=%+v err=%v", records, err)
			}
		})
	}
}

func TestExternalResultKeepsQueuedInputOpen(t *testing.T) {
	_, w := newExternalInputTestWorker(t)
	if deferred, err := w.enqueueExternalInput("Sample follow-up", nil); err != nil || deferred {
		t.Fatalf("enqueue = %t, %v", deferred, err)
	}
	stop, next := w.handleExternalResult(externalclaude.Event{Type: externalclaude.TypeResult, Result: "Intermediate text"})
	if !stop || next.Content != "Sample follow-up" || w.inputClosed || w.getResultError() != nil {
		t.Fatalf("queued input was lost or checked as a deliverable: next=%+v closed=%t err=%v", next, w.inputClosed, w.getResultError())
	}
	if len(w.inputs) != 1 || w.inputs[0].ID != next.ID {
		t.Fatalf("finished input was not removed: %+v", w.inputs)
	}
}

func TestExternalResultSealsBeforeConcurrentFollowUp(t *testing.T) {
	a, w := newExternalInputTestWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	a.taskRegistryPersistHook = func() {
		if calls.Add(1) == 1 {
			close(snapshot)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	resultDone := make(chan struct{})
	go func() {
		w.handleExternalResult(externalclaude.Event{Type: externalclaude.TypeResult, Result: "Sample result"})
		close(resultDone)
	}()
	select {
	case <-snapshot:
	case <-ctx.Done():
		t.Fatal("result never reached input completion")
	}
	if w.promptMu.TryLock() {
		w.promptMu.Unlock()
		t.Fatal("input completion released the result decision lock")
	}
	followUp := make(chan error, 1)
	go func() {
		deferred, err := w.enqueueExternalInput("Sample correction", nil)
		if err == nil && !deferred {
			err = fmt.Errorf("follow-up was routed to the completed input")
		}
		followUp <- err
	}()
	close(release)
	select {
	case <-resultDone:
	case <-ctx.Done():
		t.Fatal("result did not settle")
	}
	select {
	case err := <-followUp:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("concurrent follow-up did not finish")
	}
	if w.getResultError() == nil || w.inputs[len(w.inputs)-1].State != externalInputQueued {
		t.Fatalf("contract or follow-up decision changed during completion: inputs=%+v err=%v", w.inputs, w.getResultError())
	}
}

func TestExternalSessionPersistenceKeepsConcurrentMailbox(t *testing.T) {
	for _, ordering := range []string{"before snapshot", "during write"} {
		t.Run(ordering, func(t *testing.T) {
			a, w := newExternalInputTestWorker(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			release := make(chan struct{})
			snapshot := make(chan struct{})
			var released atomic.Bool
			unlock := func() {
				if released.CompareAndSwap(false, true) {
					if ordering == "before snapshot" {
						a.taskRegistryPersistMu.Unlock()
					} else {
						close(release)
					}
				}
			}
			defer unlock()
			if ordering == "before snapshot" {
				a.taskRegistryPersistMu.Lock()
			} else {
				var calls atomic.Int32
				a.taskRegistryPersistHook = func() {
					if calls.Add(1) == 1 {
						close(snapshot)
						select {
						case <-release:
						case <-ctx.Done():
						}
					}
				}
			}
			persisted := make(chan error, 1)
			go func() {
				w.promptMu.Lock()
				defer w.promptMu.Unlock()
				persisted <- w.persistExternalSessionLocked()
			}()
			if ordering == "during write" {
				select {
				case <-snapshot:
				case <-ctx.Done():
					t.Fatal("worker never reached the write barrier")
				}
			}
			mailboxDone := make(chan struct{})
			go func() {
				a.syncTaskRecordFromMailbox(SubAgentMailboxMessage{
					TaskID: w.taskID, AgentID: w.instanceID, MessageID: "mailbox-1",
					OwnerAgentID: "owner-2", OwnerTaskID: "owner-task-2",
					Kind: SubAgentMailboxKindNotice, Summary: "Sample notice", ReportOnly: true,
				})
				close(mailboxDone)
			}()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for a.taskRecordByTaskID(w.taskID).LastMailboxID != "mailbox-1" {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("mailbox never published at the write barrier")
				}
			}
			unlock()
			select {
			case err := <-persisted:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("worker persistence did not complete")
			}
			select {
			case <-mailboxDone:
			case <-ctx.Done():
				t.Fatal("mailbox persistence did not complete")
			}
			records, err := loadDurableTaskRecords(a.currentRecoveryOwnership().dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range []*DurableTaskRecord{a.taskRecordByTaskID(w.taskID), records[w.taskID]} {
				if record.LastMailboxID != "mailbox-1" || record.LifecycleRevision < 2 ||
					record.OwnerAgentID != "owner-2" || record.OwnerTaskID != "owner-task-2" {
					t.Fatalf("worker persistence replaced mailbox fields: %+v", record)
				}
				if len(record.ExternalSession.Inputs) != 1 || record.ExternalSession.Inputs[0].State != message.ToolRecoveryStateOutcomeUnknown {
					t.Fatalf("mailbox persistence replaced worker input: %+v", record)
				}
			}
		})
	}
}

func TestExternalSpawnKeepsOwnershipChecksWithoutDuplicateWrite(t *testing.T) {
	for _, scenario := range []string{"success", "session replaced", "instance replaced", "degraded"} {
		t.Run(scenario, func(t *testing.T) {
			command := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","result":"Sample result"}'`)
			a, w, input := newStartFailureWorker(t, command)
			var writes atomic.Int32
			a.taskRegistryPersistHook = func() { writes.Add(1) }
			original := startExternalSession
			startExternalSession = func(cfg externalclaude.ProcessConfig) (*externalclaude.Session, error) {
				if writes.Load() != 1 {
					return nil, fmt.Errorf("run identity was not persisted before spawn")
				}
				session, err := externalclaude.Start(cfg)
				if err != nil {
					return nil, err
				}
				switch scenario {
				case "session replaced":
					a.sessionEpoch++
					a.installRecoveryManager(a.recoveryManager())
				case "instance replaced":
					a.subs.mu.Lock()
					a.subs.taskRecords[w.taskID].LatestInstanceID = "replacement-1"
					a.subs.mu.Unlock()
				case "degraded":
					w.persistence.markDegraded(fmt.Errorf("sample persistence failure"))
				}
				return session, nil
			}
			defer func() { startExternalSession = original }()
			session, ended, err := w.startSession(input, false)
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				session.Stop(time.Second)
				done := make(chan error, 1)
				go func() { done <- session.Wait() }()
				select {
				case <-done:
				case <-w.ctx.Done():
					t.Fatal("session cleanup did not complete")
				}
				if writes.Load() != 3 {
					t.Fatalf("registry writes = %d, want run identity, initialization and input", writes.Load())
				}
			} else if err == nil || !ended || session != nil || writes.Load() != 1 {
				t.Fatalf("post-spawn guard failed: session=%v ended=%t err=%v writes=%d", session, ended, err, writes.Load())
			}
		})
	}
}
