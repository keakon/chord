package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestSessionPausePumpRetainsFailureAcrossBarrierAndRetriesFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, identity.MainSessionLogFilename)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	manager := recovery.NewRecoveryManager(dir)
	defer manager.Close()
	a := &MainAgent{persist: newPersistencePump(4), stoppingCh: make(chan struct{})}
	a.startPersistLoop()
	defer func() { a.persist.closeUntil(nil); <-a.persist.done }()
	for _, content := range []string{"First.", "Second."} {
		a.persistAsyncThrough(manager, identity.MainAgentID, message.Message{Role: message.RoleUser, Content: content}, nil)
	}
	if !a.persist.flush() {
		t.Fatal("barrier unavailable")
	}
	if err := a.persist.strictError(manager); err == nil {
		t.Fatal("barrier cleared append failure")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := a.persist.retryFailures(manager); err != nil {
		t.Fatal(err)
	}
	messages, err := manager.LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "First." || messages[1].Content != "Second." {
		t.Fatalf("messages=%+v", messages)
	}
	if err := a.persist.retryFailures(manager); err != nil {
		t.Fatal(err)
	}
	messages, _ = manager.LoadMessages(identity.MainAgentID)
	if len(messages) != 2 {
		t.Fatal("retry appended duplicate messages")
	}
}

func TestSessionPausePumpUnknownFailureBlocksRetries(t *testing.T) {
	manager := recovery.NewRecoveryManager(t.TempDir())
	defer manager.Close()
	pump := newPersistencePump(1)
	cause := errors.New("partial append")
	pump.rememberFailure(persistEntry{recovery: manager, agentID: identity.MainAgentID, msg: message.Message{Role: message.RoleUser, Content: "First."}}, &recovery.PersistWriteError{Cause: cause})
	pump.rememberFailure(persistEntry{recovery: manager, agentID: identity.MainAgentID, msg: message.Message{Role: message.RoleUser, Content: "Second."}}, &recovery.PersistWriteError{RetrySafe: true, Cause: errors.New("blocked")})
	if err := pump.retryFailures(manager); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	messages, err := manager.LoadMessages(identity.MainAgentID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("unknown write retried: messages=%+v err=%v", messages, err)
	}
}

func TestSessionPauseCheckpointClearsOnlyMatchingTranscriptFailures(t *testing.T) {
	first := recovery.NewRecoveryManager(t.TempDir())
	second := recovery.NewRecoveryManager(t.TempDir())
	defer first.Close()
	defer second.Close()
	pump := newPersistencePump(1)
	for _, entry := range []persistEntry{{recovery: first, agentID: identity.MainAgentID}, {recovery: first, agentID: "worker"}, {recovery: second, agentID: identity.MainAgentID}} {
		pump.rememberFailure(entry, errors.New("failed"))
	}
	pump.clearCheckpointFailures(first, identity.MainAgentID)
	if len(pump.failures) != 2 || pump.failures[0].entry.agentID != "worker" || pump.failures[1].entry.recovery != second {
		t.Fatalf("cleared wrong failure: %+v", pump.failures)
	}
}

func TestSubPersistSealWaitsForAlreadyFiredCallback(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	d := newSubAgentPersistDebouncer(func() { close(started); <-release }, func() string { return "session" })
	d.markRegistryDirty()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timer did not fire")
	}
	go func() { d.stopAndWait(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("seal returned before callback finished")
	default:
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("seal did not wait for callback")
	}
	d.markRegistryDirty()
	if d.strictError() == nil {
		t.Fatal("late producer was not rejected")
	}
}

func TestPausedInputMissingTranscriptIsAppendedOnceAfterRestore(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	path, _ := createPausedTakeoverTarget(t, a, "sample-target")
	manager := recovery.NewRecoveryManager(path)
	snapshot, err := manager.Recover()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Pause.CheckpointComplete = false
	snapshot.Pause.PendingInputs[0].Stage = recovery.PendingInputStageTranscriptStaged
	if err := manager.PersistMessageDurable(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "Review the sample"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.loadSessionState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Pause.PendingInputs[0].Stage != recovery.PendingInputStageQueued {
		t.Fatal("missing transcript was still considered staged")
	}
	for _, msg := range loaded.Messages {
		a.ctxMgr.Append(msg)
	}
	a.installPausedSession(loaded)
	if err := a.sessionExecutionGate().Resume(); err != nil {
		t.Fatal(err)
	}
	a.processPendingUserMessagesBeforeLLMInTurn()
	a.processPendingUserMessagesBeforeLLMInTurn()
	count := 0
	for _, msg := range a.GetMessages() {
		if msg.InputID == loaded.Pause.PendingInputs[0].InputID {
			count++
			if msg.Content != loaded.Pause.PendingInputs[0].Content {
				t.Fatal("restored input content changed")
			}
		}
	}
	if count != 1 {
		t.Fatalf("restored input appears %d times, want one", count)
	}
}

func TestInitialPauseRetainsPendingCopyWhenTranscriptPersistenceFails(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	manager := a.recoveryManager()
	input := recovery.PendingInput{InputID: "input-1", OwnerTaskID: identity.MainAgentID, Content: "Review sample files", Stage: recovery.PendingInputStageTranscriptStaged}
	path := filepath.Join(a.SessionDir(), identity.MainSessionLogFilename)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	a.persistAsync(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: input.Content, InputID: input.InputID})
	checkpoint := &recovery.SessionPauseCheckpoint{Paused: true, CheckpointID: "checkpoint-1", PendingInputs: []recovery.PendingInput{input}}
	a.saveInitialSessionPause(nil, checkpoint, &recovery.SessionSnapshot{})
	event := <-a.eventCh
	result := event.Payload.(sessionPauseResult)
	if result.err == nil {
		t.Fatal("initial save acknowledged a failed transcript write")
	}
	snapshot, err := manager.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Pause == nil || len(snapshot.Pause.PendingInputs) != 1 || snapshot.Pause.PendingInputs[0].Content != input.Content {
		t.Fatal("transcript failure discarded the recoverable pending copy")
	}
}
