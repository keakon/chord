package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestPendingOwnerReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*DurableTaskRecord, *DurableAgentRequest, map[string]*DurableTaskRecord)
		want   bool
	}{
		{"matching", func(_ *DurableTaskRecord, _ *DurableAgentRequest, _ map[string]*DurableTaskRecord) {}, true},
		{"response persisted", func(_ *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			r.Response = &DurableAgentResponse{Message: "answer"}
		}, true},
		{"old attempt", func(_ *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			r.SourceAttempt = 2
		}, false},
		{"zero attempt", func(_ *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			r.SourceAttempt = 0
		}, false},
		{"old mailbox", func(_ *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			r.RequestMessageID = "old"
		}, false},
		{"empty mailbox", func(task *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			task.EscalationMailboxID = ""
			r.RequestMessageID = ""
		}, false},
		{"wrong owner", func(_ *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			r.TargetTaskID = "other"
		}, false},
		{"main attempt", func(_ *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			r.TargetAttempt = 1
		}, false},
		{"missing owner", func(task *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			task.OwnerTaskID = "owner"
			r.TargetTaskID = "owner"
		}, false},
		{"nested", func(task *DurableTaskRecord, r *DurableAgentRequest, tasks map[string]*DurableTaskRecord) {
			task.OwnerTaskID = "owner"
			r.TargetTaskID = "owner"
			r.TargetAttempt = 3
			tasks["owner"] = &DurableTaskRecord{Attempt: 3, State: string(SubAgentStateRunning)}
		}, true},
		{"terminal owner", func(task *DurableTaskRecord, r *DurableAgentRequest, tasks map[string]*DurableTaskRecord) {
			task.OwnerTaskID = "owner"
			r.TargetTaskID = "owner"
			r.TargetAttempt = 3
			tasks["owner"] = &DurableTaskRecord{Attempt: 3, State: string(SubAgentStateCompleted)}
		}, false},
		{"old owner attempt", func(task *DurableTaskRecord, r *DurableAgentRequest, tasks map[string]*DurableTaskRecord) {
			task.OwnerTaskID = "owner"
			r.TargetTaskID = "owner"
			r.TargetAttempt = 2
			tasks["owner"] = &DurableTaskRecord{Attempt: 3, State: string(SubAgentStateRunning)}
		}, false},
		{"empty", func(_ *DurableTaskRecord, r *DurableAgentRequest, _ map[string]*DurableTaskRecord) {
			r.Message = " \n\t "
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			task := &DurableTaskRecord{TaskID: "task", Attempt: 1, EscalationMailboxID: "request", State: string(SubAgentStateRunning)}
			req := &DurableAgentRequest{SourceTaskID: "task", SourceAttempt: 1, RequestMessageID: "request", State: agentRequestStatePending, Message: " Need\n a\tdecision "}
			a.subs.taskRecords = map[string]*DurableTaskRecord{"task": task}
			tc.change(task, req, a.subs.taskRecords)
			a.subs.agentRequests = map[string]*DurableAgentRequest{"request": req}
			got := a.pendingOwnerReasonsLocked()[taskAttemptKey{TaskID: "task", Attempt: 1}]
			want := ""
			if tc.want {
				want = "Need a decision"
			}
			if got != want {
				t.Fatalf("reason=%q want=%q", got, want)
			}
			for _, state := range []string{agentRequestStateResponded, agentRequestStateExpired, agentRequestStateCancelled, agentRequestStateOrphaned} {
				req.State = state
				if len(a.pendingOwnerReasonsLocked()) != 0 {
					t.Fatalf("reason for %s request", state)
				}
			}
		})
	}
}

func TestOwnerReasonSelectionAndState(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	task := &DurableTaskRecord{TaskID: "task", Attempt: 2, EscalationMailboxID: "request"}
	a.subs.taskRecords = map[string]*DurableTaskRecord{"task": task}
	a.subs.agentRequests = make(map[string]*DurableAgentRequest)
	for i, id := range []string{"a", "b", "c"} {
		a.subs.agentRequests[id] = &DurableAgentRequest{CorrelationID: id, SourceTaskID: "task", SourceAttempt: 2, RequestMessageID: "request", State: agentRequestStatePending, Message: strings.Repeat(id, 300), CreatedAt: time.Unix(int64(min(i, 1)), 0)}
	}
	for range 20 {
		reasons := a.pendingOwnerReasonsLocked()
		reason := reasons[taskAttemptKey{TaskID: "task", Attempt: 2}]
		if reason != strings.Repeat("c", 255)+"…" {
			t.Fatalf("reason=%q", reason)
		}
		for _, state := range []SubAgentState{SubAgentStateWaitingMain, SubAgentStateRunning, SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled, SubAgentStateIdle, SubAgentStateWaitingDescendant} {
			info := SubAgentInfo{TaskID: "task", State: string(state), AwaitingOwnerReason: "stale", LastSummary: "blocked"}
			applyAwaitingOwnerReason(&info, task, reasons)
			if (info.AwaitingOwnerReason != "") != (state == SubAgentStateWaitingMain) {
				t.Fatalf("state=%s reason=%q", state, info.AwaitingOwnerReason)
			}
			applyAwaitingOwnerReason(&info, nil, reasons)
			if info.AwaitingOwnerReason != "" {
				t.Fatal("nil record preserved reason")
			}
		}
	}
}

func TestOwnerReasonLiveParkedAndRestore(t *testing.T) {
	t.Parallel()
	a, sub, request := createMainOwnedTestRequest(t)
	sub.setState(SubAgentStateWaitingMain, "unrelated summary")
	a.recordSubAgentEscalation(sub, request.RequestMessageID)
	a.syncTaskRecordFromSub(sub, "")
	assertReason := func(want string) {
		t.Helper()
		before := a.snapshotAgentRequests()
		for range 2 {
			infos := a.GetSubAgents()
			if len(infos) != 1 || infos[0].AwaitingOwnerReason != want {
				t.Fatalf("infos=%+v want reason=%q", infos, want)
			}
		}
		if !reflect.DeepEqual(before, a.snapshotAgentRequests()) {
			t.Fatal("projection mutated requests")
		}
	}
	assertReason("choose an API")
	a.parkSubAgent(sub.instanceID)
	assertReason("choose an API")
	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := loadAgentRequests(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	a.subs.mu.Lock()
	a.subs.taskRecords = records
	a.subs.mu.Unlock()
	a.resetAgentRequests(requests)
	assertReason("choose an API")
	a.resetAgentRequests(nil)
	assertReason("")
}

func TestOwnerReasonResponseRefresh(t *testing.T) {
	t.Parallel()
	a, sub, request := createMainOwnedTestRequest(t)
	sub.setState(SubAgentStateWaitingMain, "")
	a.recordSubAgentEscalation(sub, request.RequestMessageID)
	for len(a.outputCh) > 0 {
		<-a.outputCh
	}
	_, err := a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "use API A", Kind: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for len(a.outputCh) > 0 {
		if _, ok := (<-a.outputCh).(SubAgentDisplayChangedEvent); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("missing display invalidation")
	}
	if got := a.GetSubAgents()[0].AwaitingOwnerReason; got != "" {
		t.Fatalf("stale reason=%q", got)
	}
}

func TestSubAgentDisplayEmptySummaryAndStaleAttempt(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task")
	attempt := a.taskRecordByTaskID(sub.taskID).Attempt
	for _, tc := range []struct {
		attempt uint64
		want    bool
	}{{attempt + 1, false}, {attempt, true}} {
		a.handleSubAgentStateChangedEvent(Event{SourceID: sub.instanceID, Payload: &SubAgentStateChangedPayload{InstanceID: sub.instanceID, TaskID: sub.taskID, Attempt: tc.attempt, State: SubAgentStateWaitingMain}})
		found := false
		for len(a.outputCh) > 0 {
			if _, ok := (<-a.outputCh).(SubAgentDisplayChangedEvent); ok {
				found = true
			}
		}
		if found != tc.want {
			t.Fatalf("attempt=%d invalidation=%v", tc.attempt, found)
		}
	}
}

func TestOwnerReasonResponsePersistedButDeliveryRejected(t *testing.T) {
	t.Parallel()
	a, sub, request := createMainOwnedTestRequest(t)
	sub.setState(SubAgentStateWaitingMain, "")
	a.recordSubAgentEscalation(sub, request.RequestMessageID)
	sub.queueMessageLimit = 1
	if !sub.InjectUserMessage("already queued") {
		t.Fatal("could not fill input queue")
	}
	for len(a.outputCh) > 0 {
		<-a.outputCh
	}
	_, err := a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "answer"})
	if err == nil {
		t.Fatal("expected saturated queue rejection")
	}
	req := a.snapshotAgentRequests()[request.CorrelationID]
	if req.State != agentRequestStatePending || req.Response == nil {
		t.Fatalf("request=%+v", req)
	}
	if got := a.GetSubAgents()[0].AwaitingOwnerReason; got != "choose an API" {
		t.Fatalf("pending reason=%q", got)
	}
	found := false
	for len(a.outputCh) > 0 {
		if _, ok := (<-a.outputCh).(SubAgentDisplayChangedEvent); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("persisted response did not invalidate display")
	}
}

func TestCompactOwnerReasonBoundaries(t *testing.T) {
	for _, message := range []string{
		"", " \n\t ", " need\n a\tdecision ",
		strings.Repeat("x", 255), strings.Repeat("x", 256), strings.Repeat("x", 257),
		strings.Repeat("x", 256) + " \n\t ",
		strings.Repeat("x", 255) + " \n y",
		strings.Repeat("x", 254) + " \n y",
		strings.Repeat("x", 256) + " " + strings.Repeat("y", 100000),
		"\u2003需要\u00a0确认\n" + strings.Repeat("范围", 200),
	} {
		want := []rune(strings.Join(strings.Fields(message), " "))
		if len(want) > awaitingOwnerReasonMaxRunes {
			want = append(want[:awaitingOwnerReasonMaxRunes-1], '…')
		}
		if got := compactOwnerReason(message); got != string(want) {
			t.Fatalf("input bytes=%d: got %q, want %q", len(message), got, string(want))
		}
	}
}
