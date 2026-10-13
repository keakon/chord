package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func deliveryFixture(t *testing.T) (*RecoveryManager, *InputDeliveryState) {
	t.Helper()
	dir := t.TempDir()
	manager := NewRecoveryManager(dir)
	t.Cleanup(func() { _ = manager.Close() })
	return manager, &InputDeliveryState{SessionID: filepath.Base(dir), Revision: 1, NextSequence: 1,
		Gate:   FollowupGate{Mode: FollowupGateRunning},
		Inputs: []PendingInput{{InputID: "input-1", OwnerTaskID: identity.MainAgentID, Sequence: 1, Revision: 1, Stage: PendingInputStageQueued, DeliveryPolicy: InputDeliveryAfterCompletion, Content: "sample input"}},
	}
}

func TestInputDeliveryCASAndIsolation(t *testing.T) {
	manager, next := deliveryFixture(t)
	empty, err := manager.LoadInputDelivery()
	if err != nil || empty != nil {
		t.Fatalf("missing file: %v %v", empty, err)
	}
	saved, err := manager.SaveInputDelivery(0, next)
	if err != nil {
		t.Fatal(err)
	}
	next.Inputs[0].Content = "modified"
	if saved.Inputs[0].Content != "sample input" {
		t.Fatal("save aliased input")
	}
	if _, err := manager.SaveInputDelivery(0, next); !errors.Is(err, ErrInputDeliveryConflict) {
		t.Fatalf("CAS: %v", err)
	}
	loaded, err := manager.LoadInputDelivery()
	if err != nil || loaded.Inputs[0].Content != "sample input" {
		t.Fatalf("load: %v %v", loaded, err)
	}
	saved.Revision++
	saved.Inputs[0].Content = "replacement"
	if _, err := manager.SaveInputDelivery(1, saved); err != nil {
		t.Fatal(err)
	}
}

func TestInputDeliveryValidation(t *testing.T) {
	cases := map[string]func(*InputDeliveryState){
		"session":        func(s *InputDeliveryState) { s.SessionID = "other" },
		"revision":       func(s *InputDeliveryState) { s.Revision = 0 },
		"owner":          func(s *InputDeliveryState) { s.Inputs[0].OwnerTaskID = "worker" },
		"policy":         func(s *InputDeliveryState) { s.Inputs[0].DeliveryPolicy = "invalid" },
		"stage":          func(s *InputDeliveryState) { s.Inputs[0].Stage = PendingInputStageTranscriptStaged },
		"sequence":       func(s *InputDeliveryState) { s.Inputs[0].Sequence = 2 },
		"item revision":  func(s *InputDeliveryState) { s.Inputs[0].Revision = 0 },
		"duplicate":      func(s *InputDeliveryState) { s.Inputs = append(s.Inputs, s.Inputs[0]) },
		"hold":           func(s *InputDeliveryState) { s.EditHolds = []string{"missing"} },
		"duplicate hold": func(s *InputDeliveryState) { s.EditHolds = []string{"input-1", "input-1"} },
		"work":           func(s *InputDeliveryState) { s.Inputs[0].Stage = PendingInputStageClaimed },
		"queued work":    func(s *InputDeliveryState) { s.Inputs[0].WorkID = "work-1" },
		"gate work":      func(s *InputDeliveryState) { s.Gate.WaitingWorkID = "work-1" },
		"running reason": func(s *InputDeliveryState) { s.Gate.Reason = FollowupUserPaused },
		"paused reason":  func(s *InputDeliveryState) { s.Gate.Mode = FollowupGatePaused },
		"size":           func(s *InputDeliveryState) { s.Inputs[0].Content = strings.Repeat("x", InputDeliveryMaxTextBytes+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manager, state := deliveryFixture(t)
			mutate(state)
			if _, err := manager.SaveInputDelivery(0, state); err == nil {
				t.Fatal("invalid state saved")
			}
			if _, err := os.Stat(filepath.Join(manager.sessionDir, InputDeliveryFile)); !os.IsNotExist(err) {
				t.Fatal("invalid state changed file")
			}
		})
	}
}

func TestInputDeliveryCommitFailures(t *testing.T) {
	for _, phase := range []string{"write", "rename", "sync"} {
		t.Run(phase, func(t *testing.T) {
			manager, state := deliveryFixture(t)
			failure := errors.New("injected failure")
			switch phase {
			case "write":
				manager.deliveryWrite = func(string, string, []byte) error { return failure }
			case "rename":
				manager.deliveryRename = func(string, string) error { return failure }
			case "sync":
				manager.deliverySyncDir = func(string) error { return failure }
			}
			_, err := manager.SaveInputDelivery(0, state)
			if !errors.Is(err, failure) {
				t.Fatalf("error: %v", err)
			}
			unknown, _ := errors.AsType[*InputDeliveryCommitUnknownError](err)
			if (unknown != nil) != (phase == "sync") {
				t.Fatalf("unknown classification: %v", err)
			}
			loaded, err := manager.LoadInputDelivery()
			if err != nil || (loaded != nil) != (phase == "sync") {
				t.Fatalf("disk: %v %v", loaded, err)
			}
		})
	}
}

func TestInputDeliveryCorruptionIsNotEmpty(t *testing.T) {
	manager, state := deliveryFixture(t)
	path := filepath.Join(manager.sessionDir, InputDeliveryFile)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LoadInputDelivery(); err == nil {
		t.Fatal("corruption ignored")
	}
	if _, err := manager.SaveInputDelivery(0, state); err == nil {
		t.Fatal("corruption overwritten")
	}
}

func TestInputDeliveryReconcileCrashWindows(t *testing.T) {
	for _, stage := range []string{PendingInputStageQueued, PendingInputStageClaimed, PendingInputStageStarted} {
		for _, present := range []bool{false, true} {
			t.Run(stage+"/"+map[bool]string{false: "absent", true: "present"}[present], func(t *testing.T) {
				manager, state := deliveryFixture(t)
				state.Inputs[0].Stage = stage
				if stage != PendingInputStageQueued {
					state.Inputs[0].WorkID = "work-1"
					state.Works = []DeliveryWork{{WorkID: "work-1", Origin: DeliveryWorkFollowup, Outcome: DeliveryWorkActive}}
					state.Gate.WaitingWorkID = "work-1"
				}
				var transcript []message.Message
				if present {
					transcript = []message.Message{{Role: message.RoleUser, InputID: "input-1"}}
				}
				restored := ReconcileInputDelivery(state, transcript)
				if err := ValidateInputDelivery(restored, filepath.Base(manager.sessionDir)); err != nil {
					t.Fatal(err)
				}
				if restored.Gate.Mode != FollowupGatePaused {
					t.Fatal("restore granted execution")
				}
				wantStage := stage
				if stage == PendingInputStageClaimed && !present {
					wantStage = PendingInputStageQueued
				}
				if restored.Inputs[0].Stage != wantStage {
					t.Fatal("wrong recovered stage")
				}
				if stage == PendingInputStageStarted && restored.Works[0].Outcome != DeliveryWorkUnknown {
					t.Fatal("started input was not classified unknown")
				}
			})
		}
	}
}

func TestInputDeliveryRewriteGuard(t *testing.T) {
	manager, state := deliveryFixture(t)
	state.Inputs[0].Stage, state.Inputs[0].WorkID = PendingInputStageClaimed, "work-1"
	state.Works = []DeliveryWork{{WorkID: "work-1", Origin: DeliveryWorkUser, Outcome: DeliveryWorkActive}}
	if _, err := manager.SaveInputDelivery(0, state); err != nil {
		t.Fatal(err)
	}
	msg := message.Message{Role: message.RoleUser, InputID: "input-1", Content: "sample"}
	if err := manager.PersistMessage(identity.MainAgentID, msg); err != nil {
		t.Fatal(err)
	}
	if err := manager.SyncMessages(identity.MainAgentID); err != nil {
		t.Fatal(err)
	}
	if err := manager.ValidatePendingInputRewrite(identity.MainAgentID, nil); err == nil {
		t.Fatal("claimed evidence removed")
	}
	if err := manager.ValidatePendingInputRewrite(identity.MainAgentID, []message.Message{msg}); err != nil {
		t.Fatal(err)
	}
	state.Revision++
	state.Inputs[0].Stage = PendingInputStageStarted
	if _, err := manager.SaveInputDelivery(1, state); err != nil {
		t.Fatal(err)
	}
	if err := manager.ValidatePendingInputRewrite(identity.MainAgentID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestInputDeliveryAttachmentOwnership(t *testing.T) {
	manager, state := deliveryFixture(t)
	source := filepath.Join(t.TempDir(), "sample.png")
	if err := os.WriteFile(source, []byte("sample image data"), 0600); err != nil {
		t.Fatal(err)
	}
	state.Inputs[0].Parts = []message.ContentPart{{Type: message.ContentPartImage, ImagePath: source, MimeType: "image/png"}}
	saved, err := manager.SaveInputDelivery(0, state)
	if err != nil {
		t.Fatal(err)
	}
	part := saved.Inputs[0].Parts[0]
	if part.ImagePath == source || len(part.Data) != 0 || part.DataBytes == 0 {
		t.Fatalf("not session owned: %+v", part)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LoadInputDelivery(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(part.ImagePath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LoadInputDelivery(); err == nil {
		t.Fatal("missing attachment ignored")
	}
}

func TestInputDeliveryFailedAttachmentLeavesQueue(t *testing.T) {
	manager, state := deliveryFixture(t)
	if _, err := manager.SaveInputDelivery(0, state); err != nil {
		t.Fatal(err)
	}
	state.Revision++
	state.Inputs[0].Parts = []message.ContentPart{{Type: message.ContentPartImage, ImagePath: filepath.Join(t.TempDir(), "missing.png")}}
	if _, err := manager.SaveInputDelivery(1, state); err == nil {
		t.Fatal("missing source accepted")
	}
	current, err := manager.LoadInputDelivery()
	if err != nil || current.Revision != 1 || len(current.Inputs[0].Parts) != 0 {
		t.Fatalf("failed write changed queue: %v %v", current, err)
	}
}

func TestInputDeliveryTranscriptRequestIdentity(t *testing.T) {
	manager, _ := deliveryFixture(t)
	msg := message.Message{Role: message.RoleUser, Content: "sample input", InputID: "input-1", InputRequestID: "request-1"}
	if err := manager.PersistMessage(identity.MainAgentID, msg.Clone()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SyncMessages(identity.MainAgentID); err != nil {
		t.Fatal(err)
	}
	messages, err := manager.LoadMessages(identity.MainAgentID)
	if err != nil || len(messages) != 1 || messages[0].InputRequestID != msg.InputRequestID {
		t.Fatalf("request correlation lost: %v %v", messages, err)
	}
	manager.messageSync = func(*os.File) error { return errors.New("injected sync failure") }
	if err := manager.SyncMessages(identity.MainAgentID); err == nil {
		t.Fatal("sync failure ignored")
	}
	messages, err = manager.LoadMessages(identity.MainAgentID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("sync appended a record: %v %v", messages, err)
	}
}

func TestInputDeliveryPreservesPauseReasonAndHold(t *testing.T) {
	_, state := deliveryFixture(t)
	state.Gate = FollowupGate{Mode: FollowupGatePaused, Reason: FollowupUserPaused}
	state.EditHolds = []string{"input-1"}
	restored := ReconcileInputDelivery(state, nil)
	if restored.Gate.Reason != FollowupUserPaused || len(restored.EditHolds) != 1 {
		t.Fatal("restore lost pause or edit hold")
	}
	restored.Inputs[0].Content = "replacement"
	restored.EditHolds[0] = "changed"
	if state.Inputs[0].Content != "sample input" || state.EditHolds[0] != "input-1" {
		t.Fatal("reconcile mutated committed state")
	}
}

func TestInputDeliveryCheckpointHasSingleOwner(t *testing.T) {
	manager, state := deliveryFixture(t)
	if _, err := manager.SaveInputDelivery(0, state); err != nil {
		t.Fatal(err)
	}
	checkpoint := &SessionPauseCheckpoint{InputDeliveryRevision: 1, PendingInputs: state.Inputs}
	if err := manager.SaveSnapshot(&SessionSnapshot{Pause: checkpoint}); err == nil {
		t.Fatal("duplicate executable input stored")
	}
	checkpoint.PendingInputs = nil
	checkpoint.InputDeliveryRevision = 2
	if err := manager.SaveSnapshot(&SessionSnapshot{Pause: checkpoint}); err == nil {
		t.Fatal("uncommitted seal stored")
	}
	checkpoint.InputDeliveryRevision = 1
	if err := manager.SaveSnapshot(&SessionSnapshot{Pause: checkpoint}); err != nil {
		t.Fatal(err)
	}
}

func TestInputDeliveryRewriteProtectsSkillPair(t *testing.T) {
	manager, state := deliveryFixture(t)
	state.Inputs[0].Stage, state.Inputs[0].WorkID = PendingInputStageClaimed, "work-1"
	state.Works = []DeliveryWork{{WorkID: "work-1", Origin: DeliveryWorkUser, Outcome: DeliveryWorkActive}}
	if _, err := manager.SaveInputDelivery(0, state); err != nil {
		t.Fatal(err)
	}
	messages := []message.Message{
		{Role: message.RoleUser, InputID: "input-1", Content: "sample input"},
		{Role: message.RoleAssistant, InputID: "input-1", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_tool"}}},
		{Role: message.RoleTool, InputID: "input-1", ToolCallID: "call-1", Content: "sample instructions"},
	}
	for _, msg := range messages {
		if err := manager.PersistMessage(identity.MainAgentID, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.ValidatePendingInputRewrite(identity.MainAgentID, messages); err != nil {
		t.Fatal(err)
	}
	if err := manager.ValidatePendingInputRewrite(identity.MainAgentID, messages[:1]); err == nil {
		t.Fatal("skill pair removed before start permit")
	}
	changed := append([]message.Message(nil), messages...)
	changed[2].ToolCallID = "other-call"
	if err := manager.ValidatePendingInputRewrite(identity.MainAgentID, changed); err == nil {
		t.Fatal("skill pair changed before start permit")
	}
}

func TestInputDeliveryConcurrentCAS(t *testing.T) {
	manager, state := deliveryFixture(t)
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := manager.SaveInputDelivery(0, state); results <- err }()
	}
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("expected one winner: %v %v", first, second)
	}
	if first == nil {
		first = second
	}
	if !errors.Is(first, ErrInputDeliveryConflict) {
		t.Fatalf("losing CAS: %v", first)
	}
}

func TestInputDeliveryQueueOnlySessionIsDiscoverable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "queue-only")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manager := NewRecoveryManager(dir)
	defer manager.Close()
	state := &InputDeliveryState{SessionID: "queue-only", Revision: 1, NextSequence: 1, Gate: FollowupGate{Mode: FollowupGatePaused, Reason: FollowupUserPaused}, Inputs: []PendingInput{{InputID: "input", OwnerTaskID: identity.MainAgentID, Revision: 1, Sequence: 1, Stage: PendingInputStageQueued, DeliveryPolicy: InputDeliveryAfterCompletion, Content: "sample"}}}
	if _, err := manager.SaveInputDelivery(0, state); err != nil {
		t.Fatal(err)
	}
	sessions, err := ListSessions(root, "")
	if err != nil || len(sessions) != 1 || sessions[0].ID != "queue-only" || sessions[0].MessageCount != 0 {
		t.Fatalf("sessions=%+v error=%v", sessions, err)
	}
	if candidates := RecentSessionCandidates(root, ""); len(candidates) != 1 || candidates[0] != dir {
		t.Fatalf("candidates=%v", candidates)
	}
}
