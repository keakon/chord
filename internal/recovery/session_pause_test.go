package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func pauseInput(id string) PendingInput {
	return PendingInput{InputID: id, OwnerTaskID: identity.MainAgentID, Sequence: 1, Revision: 2, RequestID: "request-1", DraftID: "draft-1", Content: "Review the sample", FromUser: true, Stage: PendingInputStageQueued}
}

func TestPausedSessionAttachmentSurvivesTemporaryDirectoryRemoval(t *testing.T) {
	r, dir := newTestManager(t)
	temp := t.TempDir()
	path := filepath.Join(temp, "sample.pdf")
	payload := []byte("sample attachment bytes")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	input := pauseInput("runtime:1")
	input.Parts = []message.ContentPart{{Type: message.ContentPartPDF, MimeType: "application/pdf", FileName: "sample.pdf", ImagePath: path}}
	checkpoint := &SessionPauseCheckpoint{Identity: TransferIdentity{TransferID: "transfer-1", SessionID: "session-1", SourceRuntimeID: "runtime-1", SourceSessionEpoch: 1, Workspace: dir, TargetKind: "tui", TargetInstanceID: "instance-1"}, CheckpointID: "checkpoint-1", Paused: true, CheckpointComplete: true, PendingInputs: []PendingInput{input}, FrozenAt: time.Now()}
	if err := r.SaveSnapshot(&SessionSnapshot{Pause: checkpoint}); err != nil {
		t.Fatal(err)
	}
	if checkpoint.PendingInputs[0].Parts[0].ImagePath != path {
		t.Fatal("save mutated caller checkpoint")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(temp); err != nil {
		t.Fatal(err)
	}
	restored := NewRecoveryManager(dir)
	defer restored.Close()
	snap, err := restored.Recover()
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := restored.RestorePendingInputs(snap.Pause.PendingInputs)
	if err != nil {
		t.Fatal(err)
	}
	part := inputs[0].Parts[0]
	if len(part.Data) != 0 || part.DataBytes != int64(len(payload)) || !pathWithin(filepath.Join(dir, "images"), part.ImagePath) {
		t.Fatalf("attachment not lazy/session-owned: %+v", part)
	}
	actual, err := os.ReadFile(part.ImagePath)
	if err != nil || !reflect.DeepEqual(actual, payload) {
		t.Fatalf("attachment = %q, err = %v", actual, err)
	}
	if inputs[0].RequestID != input.RequestID || inputs[0].Revision != 2 || !snap.Pause.CheckpointComplete {
		t.Fatalf("metadata lost: %+v", snap.Pause)
	}
	if err := os.Remove(part.ImagePath); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.RestorePendingInputs(inputs); err == nil {
		t.Fatal("missing saved attachment accepted")
	}
}

func TestInputTranscriptCommitCrashReconcilesWithoutDuplicate(t *testing.T) {
	r, dir := newTestManager(t)
	input := pauseInput("runtime:1")
	checkpoint := &SessionPauseCheckpoint{Paused: true, CheckpointComplete: true, PendingInputs: []PendingInput{input}}
	if err := r.SaveSnapshot(&SessionSnapshot{Pause: checkpoint}); err != nil {
		t.Fatal(err)
	}
	msg := message.Message{Role: message.RoleUser, Content: input.Content, InputID: input.InputID}
	if err := r.PersistMessageDurable(identity.MainAgentID, msg); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	restored := NewRecoveryManager(dir)
	defer restored.Close()
	snap, err := restored.Recover()
	if err != nil {
		t.Fatal(err)
	}
	history, err := restored.LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatal(err)
	}
	pending := ReconcilePendingInputs(snap.Pause.PendingInputs, map[string][]message.Message{identity.MainAgentID: history})
	if len(pending) != 1 || pending[0].Stage != PendingInputStageTranscriptStaged || len(history) != 1 {
		t.Fatalf("wrong crash reconciliation: %+v, %+v", pending, history)
	}
	if err := restored.RewriteLog(identity.MainAgentID, []message.Message{{Role: message.RoleUser, Content: "summary"}}); err == nil {
		t.Fatal("rewrite discarded pending transcript identity")
	}
	if err := restored.RewriteLog(identity.MainAgentID, []message.Message{{Role: message.RoleUser, Content: "summary", InputID: input.InputID}}); err == nil {
		t.Fatal("rewrite changed pending original while preserving identity")
	}
	still, _ := restored.LoadMessages(identity.MainAgentID)
	if len(still) != 1 || still[0].InputID != input.InputID {
		t.Fatal("rejected rewrite changed history")
	}
	snap.Pause.PendingInputs = nil
	if err := restored.SaveSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if err := restored.RewriteLog(identity.MainAgentID, []message.Message{{Role: message.RoleUser, Content: "summary"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPendingInputStagesFollowOwnerTranscriptEvidence(t *testing.T) {
	inputs := []PendingInput{
		{InputID: "input-1", OwnerTaskID: identity.MainAgentID, Sequence: 1, Stage: PendingInputStageTranscriptStaged},
		{InputID: "input-2", OwnerTaskID: identity.MainAgentID, Sequence: 2, Stage: PendingInputStageQueued},
		{InputID: "input-1", OwnerTaskID: "task-1", Sequence: 1, Stage: PendingInputStageTranscriptStaged},
		{InputID: "input-3", OwnerTaskID: "task-2", Sequence: 1, Stage: PendingInputStageTranscriptStaged},
	}
	transcripts := map[string][]message.Message{
		identity.MainAgentID: {{Role: message.RoleUser, InputID: "input-2"}},
		"task-1":             {{Role: message.RoleUser, InputID: "input-1"}},
	}
	reconciled := ReconcilePendingInputs(inputs, transcripts)
	want := []string{PendingInputStageQueued, PendingInputStageTranscriptStaged, PendingInputStageTranscriptStaged, PendingInputStageQueued}
	for i, input := range reconciled {
		if input.Stage != want[i] {
			t.Fatalf("input %s owner %s stage = %s, want %s", input.InputID, input.OwnerTaskID, input.Stage, want[i])
		}
	}
	if inputs[0].Stage != PendingInputStageTranscriptStaged || inputs[1].Stage != PendingInputStageQueued {
		t.Fatal("reconciliation changed the saved checkpoint")
	}
}

func TestSaveSnapshotDurabilityFailuresAreReturned(t *testing.T) {
	injected := errors.New("injected persistence failure")
	for _, kind := range []string{"write", "rename", "sync"} {
		t.Run(kind, func(t *testing.T) {
			r, _ := newTestManager(t)
			defer r.Close()
			switch kind {
			case "write":
				r.snapshotWrite = func(string, string, []byte) error { return injected }
			case "rename":
				r.snapshotRename = func(string, string) error { return injected }
			case "sync":
				r.snapshotSyncDir = func(string) error { return injected }
			}
			if err := r.SaveSnapshot(&SessionSnapshot{Pause: &SessionPauseCheckpoint{Paused: true}}); !errors.Is(err, injected) {
				t.Fatalf("SaveSnapshot error = %v", err)
			}
		})
	}
}

func TestPauseMissingAttachmentDoesNotCommitCheckpoint(t *testing.T) {
	r, dir := newTestManager(t)
	defer r.Close()
	if err := r.SaveSnapshot(&SessionSnapshot{ModelName: "sample/model-1"}); err != nil {
		t.Fatal(err)
	}
	input := pauseInput("runtime:1")
	input.Parts = []message.ContentPart{{Type: message.ContentPartImage, ImagePath: filepath.Join(dir, "missing.png")}}
	if err := r.SaveSnapshot(&SessionSnapshot{Pause: &SessionPauseCheckpoint{Paused: true, CheckpointComplete: true, PendingInputs: []PendingInput{input}}}); err == nil {
		t.Fatal("saved invalid attachment")
	}
	snap, err := r.Recover()
	if err != nil || snap.Pause != nil || snap.ModelName != "sample/model-1" {
		t.Fatalf("failed save changed checkpoint: %+v %v", snap, err)
	}
}

func TestSyncCloseErrorsCannotBeClearedByRepeatedClose(t *testing.T) {
	r, _ := newTestManager(t)
	if err := r.PersistMessage(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "sample"}); err != nil {
		t.Fatal(err)
	}
	if err := r.handles[identity.MainAgentID].Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(); err == nil {
		t.Fatal("Sync accepted closed writer")
	}
	first := r.Close()
	if first == nil {
		t.Fatal("Close ignored failed writer")
	}
	if second := r.Close(); second != first {
		t.Fatalf("Close did not retain error: first=%v second=%v", first, second)
	}
	if err := r.Sync(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Sync after close = %v", err)
	}
	if err := r.SaveSnapshot(&SessionSnapshot{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("SaveSnapshot after close = %v", err)
	}
	if err := r.RewriteLog(identity.MainAgentID, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("RewriteLog after close = %v", err)
	}
	if _, err := r.PersistPendingInputs([]PendingInput{pauseInput("runtime:2")}); !errors.Is(err, ErrClosed) {
		t.Fatalf("PersistPendingInputs after close = %v", err)
	}
}

func TestPersistWriteFailureRetrySafety(t *testing.T) {
	injected := errors.New("injected writer failure")
	for _, kind := range []string{"before_write", "partial_write", "sync"} {
		t.Run(kind, func(t *testing.T) {
			r, _ := newTestManager(t)
			defer r.Close()
			wantSafe := kind == "before_write"
			switch kind {
			case "before_write":
				r.messageWrite = func(*os.File, []byte) (int, error) { return 0, injected }
			case "partial_write":
				r.messageWrite = func(f *os.File, b []byte) (int, error) {
					n, err := f.Write(b[:2])
					if err != nil {
						return n, err
					}
					return n, injected
				}
			case "sync":
				r.messageSync = func(*os.File) error { return injected }
			}
			err := r.PersistMessageDurable(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "sample", InputID: "runtime:1"})
			typed, ok := errors.AsType[*PersistWriteError](err)
			if !ok || typed.RetrySafe != wantSafe || !errors.Is(err, injected) {
				t.Fatalf("failure safety = %+v, want safe=%v", err, wantSafe)
			}
			if kind == "sync" {
				history, loadErr := r.LoadMessages(identity.MainAgentID)
				if loadErr != nil || len(history) != 1 || history[0].InputID != "runtime:1" {
					t.Fatalf("sync failure lost append evidence: %+v %v", history, loadErr)
				}
			}
		})
	}
}
