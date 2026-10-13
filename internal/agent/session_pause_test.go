package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
)

type pauseBarrierTool struct {
	dummyTool
	entered, release chan struct{}
	cancelled        atomic.Bool
}

func (t *pauseBarrierTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	close(t.entered)
	select {
	case <-t.release:
		return "stored result", nil
	case <-ctx.Done():
		t.cancelled.Store(true)
		return "", ctx.Err()
	}
}

func testPauseIdentity(a *MainAgent) recovery.TransferIdentity {
	return recovery.TransferIdentity{TransferID: "transfer-1", SessionID: filepath.Base(a.SessionDir()), SourceRuntimeID: "runtime-1", SourceSessionEpoch: a.sessionEpoch, Workspace: a.ContentRoot(), TargetKind: "gateway", TargetInstanceID: "target-1"}
}

func waitPauseState(t *testing.T, a *MainAgent, state string) SessionPauseStatus {
	t.Helper()
	for {
		select {
		case event, ok := <-a.Events():
			if !ok {
				status, _ := a.SessionPauseStatus("transfer-1")
				t.Fatalf("closed before %s was delivered; status=%#v", state, status)
			}
			if event, ok := event.(SessionTransferEvent); ok && event.Status.State == state {
				return event.Status
			}
		case <-time.After(5 * time.Second):
			status, _ := a.SessionPauseStatus("transfer-1")
			t.Fatalf("waiting for %s; status=%#v", state, status)
		}
	}
}

func TestSessionPauseAPIsRejectStoppedAgent(t *testing.T) {
	a := &MainAgent{sessionDir: filepath.Join(t.TempDir(), "session-1"), stoppingCh: make(chan struct{})}
	close(a.stoppingCh)
	id := recovery.TransferIdentity{TransferID: "transfer-1", SessionID: "session-1"}
	if _, err := a.BeginSessionPause(context.Background(), SessionPauseRequest{Identity: id}); !errors.Is(err, ErrAgentShutdown) {
		t.Fatalf("begin error=%v", err)
	}
	a.pause.status = SessionPauseStatus{TransferIdentity: id, State: SessionPauseSaveFailed}
	if err := a.RetrySessionPauseSave(context.Background(), id.TransferID); !errors.Is(err, ErrAgentShutdown) {
		t.Fatalf("retry error=%v", err)
	}
}

func TestSessionPauseNaturalToolCompletionAndOrdinaryResume(t *testing.T) {
	a, provider := scenarioAgent(t, scriptedStreamCall{resp: &message.Response{ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_tool", Args: json.RawMessage(`{}`)}}, StopReason: "tool_use"}})
	tool := &pauseBarrierTool{dummyTool: dummyTool{name: "sample_tool"}, entered: make(chan struct{}), release: make(chan struct{})}
	a.tools.Register(tool)
	a.ruleset = permission.Ruleset{{Permission: "sample_tool", Pattern: "*", Action: permission.ActionAllow}}
	lock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetSessionLock(lock)
	startMainAgentLoopForTest(t, a)
	a.SendUserMessageWithReceipt("run tool", "request-1")
	select {
	case <-tool.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	if !a.QueuePendingUserDraft("draft-1", []message.ContentPart{{Type: message.ContentPartText, Text: "queued input"}, {Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("sample image")}}) {
		t.Fatal("input not accepted")
	}
	status, err := a.BeginSessionPause(context.Background(), SessionPauseRequest{Identity: testPauseIdentity(a)})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SessionPauseDraining {
		t.Fatalf("state=%#v", status)
	}
	if status.BlockerCount == 0 || len(status.Blockers) == 0 {
		t.Fatal("draining status does not identify the already running tool")
	}
	if tool.cancelled.Load() {
		t.Fatal("takeover cancelled existing tool")
	}
	if _, err := recovery.AcquireSessionLock(a.SessionDir()); err == nil {
		t.Fatal("lock released before execution completed")
	}
	if a.SendUserMessageWithReceipt("late input", "request-2") {
		t.Fatal("input accepted after cutoff")
	}
	requests, _ := provider.snapshot()
	if len(requests) != 1 {
		t.Fatal("new model work while draining")
	}
	close(tool.release)
	waitPauseState(t, a, SessionPauseSaved)
	if err := a.ReleasePausedSession("transfer-1"); err != nil {
		t.Fatal(err)
	}
	manager := recovery.NewRecoveryManager(a.SessionDir())
	snapshot, err := manager.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Pause == nil || !snapshot.Pause.CheckpointComplete || len(snapshot.Pause.PendingInputs) != 1 {
		t.Fatalf("checkpoint=%#v", snapshot.Pause)
	}
	if _, err := os.Stat(snapshot.Pause.PendingInputs[0].Parts[1].ImagePath); err != nil {
		t.Fatal(err)
	}
	messages, err := manager.LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range messages {
		if msg.ToolCallID == "call-1" && msg.Content == "stored result" {
			found = true
		}
	}
	if !found {
		t.Fatal("tool result not saved")
	}
	_ = manager.Close()
	b, nextProvider := scenarioAgent(t, scriptedStreamCall{resp: &message.Response{Content: "continued", StopReason: "stop"}})
	loaded, err := b.loadSessionState(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	newLock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	b.SetSessionLock(newLock)
	b.activateLoadedSession(loaded)
	if !b.sessionExecutionPaused() || len(b.PendingSessionInputs()) != 1 {
		t.Fatal("ordinary resume did not pause")
	}
	startMainAgentLoopForTest(t, b)
	if requests, _ := nextProvider.snapshot(); len(requests) != 0 {
		t.Fatal("ordinary resume issued model request")
	}
	b.SendUserMessageWithReceipt("continue with the queued input", "request-3")
	collectAgentEventsUntil(t, b.Events(), func(events []AgentEvent) bool {
		for _, e := range events {
			if _, ok := e.(GlobalIdleEvent); ok {
				return true
			}
		}
		return false
	})
	requests, _ = nextProvider.snapshot()
	if len(requests) != 1 {
		t.Fatalf("requests=%d", len(requests))
	}
	count := 0
	for _, msg := range b.ctxMgr.Snapshot() {
		if msg.Content == "queued input" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("queued input appeared %d times", count)
	}
}
