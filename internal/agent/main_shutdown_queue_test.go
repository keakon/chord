package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
)

// loadDurableInputDelivery reads the queue file directly. Shutdown closes the
// recovery manager, so the durable state has to be read from disk afterwards.
func loadDurableInputDelivery(t *testing.T, sessionDir string) *recovery.InputDeliveryState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionDir, recovery.InputDeliveryFile))
	if os.IsNotExist(err) {
		return &recovery.InputDeliveryState{}
	}
	if err != nil {
		t.Fatalf("read input delivery: %v", err)
	}
	var state recovery.InputDeliveryState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal input delivery: %v", err)
	}
	return &state
}

// queueAgentForTest builds an agent in the same state a running session reaches
// after the user submitted one message and then queued a follow-up: a live turn
// and a durable queue holding exactly one input.
func queueAgentForTest(t *testing.T, sessionID, content string, policy recovery.InputDeliveryPolicy) (*MainAgent, string) {
	t.Helper()
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, sessionID)
	persistRestorableSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	a.newTurn(a.newDeliveryWork("user"))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "current request"})
	queued := submitDeliveryForTest(t, a, content, policy)
	finishDeliveryWrites(t, a)
	return a, queued
}

// A next-request input is what the user expects to go out next, so a hard exit
// must commit it to the conversation history. Leaving it only in the queue
// file would make it invisible after restart.
func TestShutdownCommitsNextRequestInputToHistory(t *testing.T) {
	a, queued := queueAgentForTest(t, "shutdown-next-request", "queued follow-up", recovery.InputDeliveryNextRequest)

	// Verify the input is queued, not yet in history.
	state, err := a.recoveryManager().LoadInputDelivery()
	if err != nil || len(state.Inputs) != 1 || state.Inputs[0].InputID != queued {
		t.Fatalf("durable queue before shutdown = %+v %v", state, err)
	}

	a.Shutdown(5 * time.Second)

	// The input is now in the transcript.
	var found bool
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleUser && msg.Content == "queued follow-up" && msg.InputID == queued {
			found = true
		}
	}
	if !found {
		t.Fatal("next-request input was not committed to the transcript at shutdown")
	}

	// And the queue no longer holds it, so restore shows it once.
	for _, input := range loadDurableInputDelivery(t, a.sessionDir).Inputs {
		if input.InputID == queued {
			t.Fatal("next-request input stayed in the durable queue after being committed to history")
		}
	}
}

// If queue retirement fails after the transcript write, the request is already
// durable in history. Restore retires the stale queued row by InputID, so the
// request is not executed twice.
func TestShutdownQueueRetirementFailureKeepsInputRecoverable(t *testing.T) {
	a, queued := queueAgentForTest(t, "shutdown-save-failure", "queued follow-up", recovery.InputDeliveryNextRequest)

	// Move the durable revision so the compare-and-swap in SaveInputDelivery
	// fails, which is the only way the retirement can be rejected here.
	state := loadDurableInputDelivery(t, a.sessionDir)
	state.Revision += 100
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal input delivery: %v", err)
	}
	if err := os.WriteFile(filepath.Join(a.sessionDir, recovery.InputDeliveryFile), data, 0o600); err != nil {
		t.Fatalf("rewrite input delivery: %v", err)
	}

	if err := a.Shutdown(5 * time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	manager := recovery.NewRecoveryManager(a.sessionDir)
	messages, err := manager.LoadMessages(identity.MainAgentID)
	closeErr := manager.Close()
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	var inHistory bool
	for _, msg := range messages {
		if msg.Role == message.RoleUser && msg.InputID == queued {
			inHistory = true
		}
	}
	if !inHistory {
		t.Fatal("the committed input is missing from the transcript")
	}
	var kept bool
	for _, input := range loadDurableInputDelivery(t, a.sessionDir).Inputs {
		if input.InputID == queued {
			kept = true
		}
	}
	if !kept {
		t.Fatal("the queued input was dropped even though retiring it failed")
	}
}

// An after-completion input is a follow-up, not a next request: it must stay in
// the queue across shutdown so it still runs after the current work finishes.
func TestShutdownKeepsAfterCompletionInputInQueue(t *testing.T) {
	a, queued := queueAgentForTest(t, "shutdown-after-completion", "after completion follow-up", recovery.InputDeliveryAfterCompletion)

	a.Shutdown(5 * time.Second)

	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleUser && msg.Content == "after completion follow-up" {
			t.Fatal("after-completion input leaked into the conversation history")
		}
	}
	state := loadDurableInputDelivery(t, a.sessionDir)
	if len(state.Inputs) != 1 || state.Inputs[0].InputID != queued {
		t.Fatalf("durable queue after shutdown = %+v", state)
	}
}

// A queued image travels with its next-request input into history, and the
// session-owned attachment file must still be there after the commit.
func TestShutdownCommitsQueuedImageToHistory(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "shutdown-queued-image")
	persistRestorableSession(t, sessionDir)

	imagesDir := filepath.Join(sessionDir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(images): %v", err)
	}
	payload := []byte("queued-image-payload")
	sourcePath := filepath.Join(imagesDir, "queued-image.png")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile(image): %v", err)
	}

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	a.newTurn(a.newDeliveryWork("user"))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "current request"})
	queued := submitDeliveryForTest(t, a, "queued image", recovery.InputDeliveryNextRequest)
	attach := &deliveryOperation{update: &UpdateInput{
		OperationID:      "attach-image",
		Target:           a.InputDeliveryTarget(),
		InputID:          queued,
		ExpectedRevision: 1,
		Content:          new("queued image"),
		Parts: []message.ContentPart{{
			Type:      message.ContentPartImage,
			MimeType:  "image/png",
			FileName:  "queued-image.png",
			ImagePath: sourcePath,
			DataBytes: int64(len(payload)),
		}},
	}}
	if result := runDeliveryOperation(t, a, attach); result.Result != InputQueueCommitted {
		t.Fatalf("attach image: %+v", result)
	}
	finishDeliveryWrites(t, a)

	a.Shutdown(5 * time.Second)

	var imagePath string
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role != message.RoleUser || msg.InputID != queued {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type == message.ContentPartImage {
				imagePath = part.ImagePath
			}
		}
	}
	if imagePath == "" {
		t.Fatal("committed queued input lost its image attachment")
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("committed queued image attachment missing: %v", err)
	}
}

// A real shutdown followed by a real restore must carry the queued
// next-request input into the transcript. The in-memory assertion above cannot
// catch a persistence admission failure that drops the row during shutdown.
func TestShutdownRestoredSessionContainsNextRequestInput(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "shutdown-restore-next-request")
	persistRestorableSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	a.newTurn(a.newDeliveryWork("user"))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "current request"})
	queued := submitDeliveryForTest(t, a, "queued follow-up", recovery.InputDeliveryNextRequest)
	finishDeliveryWrites(t, a)

	if err := a.Shutdown(5 * time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	b := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := b.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup (restart): %v", err)
	}
	b.initializeInputDelivery()
	finishDeliveryWrites(t, b)

	for _, msg := range b.ctxMgr.Snapshot() {
		if msg.Role == message.RoleUser && msg.InputID == queued && msg.Content == "queued follow-up" {
			return
		}
	}
	t.Fatal("restored session lost the queued next-request input")
}

// A queued `/skill` request expands into its assistant/tool pair at shutdown, so
// the restored transcript carries the skill body the model would have seen
// instead of a bare slash command.
func TestShutdownRestoredSessionContainsQueuedSkillInvocation(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "shutdown-restore-queued-skill")
	persistRestorableSession(t, sessionDir)
	meta := writeTempSkill(t, t.TempDir(), "go-expert", "Workflow for ${CHORD_SKILL_ARGS}.", false)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	a.SetSkills([]*skill.Meta{meta})
	a.newTurn(a.newDeliveryWork("user"))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "current request"})
	queued := submitDeliveryForTest(t, a, "/skill go-expert release checks", recovery.InputDeliveryNextRequest)
	finishDeliveryWrites(t, a)

	if err := a.Shutdown(5 * time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if state := loadDurableInputDelivery(t, sessionDir); len(state.Inputs) != 0 {
		t.Fatalf("durable queue = %+v, want the committed skill request retired", state.Inputs)
	}

	b := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := b.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup (restart): %v", err)
	}
	var user, call, result bool
	for _, msg := range b.ctxMgr.Snapshot() {
		if msg.InputID != queued {
			continue
		}
		switch msg.Role {
		case message.RoleUser:
			user = true
		case message.RoleAssistant:
			call = len(msg.ToolCalls) == 1
		case message.RoleTool:
			result = strings.Contains(msg.Content, "Workflow for release checks.")
		}
	}
	if !user || !call || !result {
		t.Fatalf("restored skill request user=%v call=%v result=%v, want the full invocation", user, call, result)
	}
}

// The restart side of the contract: a session whose next-request input was
// committed to history by shutdown must show it in history, not replay it from
// the queue. The shutdown half is covered above; this drives only the restart.
func TestRestoredSessionShowsCommittedNextRequestInHistoryOnly(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "restore-committed-next-request")
	persistRestorableSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	a.newTurn(a.newDeliveryWork("user"))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "current request"})
	queued := submitDeliveryForTest(t, a, "restored from history", recovery.InputDeliveryNextRequest)
	finishDeliveryWrites(t, a)

	// The shutdown commit is what moves the row out of the queue and into
	// history; run it directly so this test only exercises the restart side.
	a.commitNextRequestInputsToTranscriptUntil(nil)
	// The commit's transcript row is written by the async persist loop; the
	// restart below reads the session file from disk, so drain the loop first.
	a.flushPersist()

	// A fresh agent restores from disk exactly like a new process would.
	b := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := b.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup (restart): %v", err)
	}
	b.initializeInputDelivery()
	finishDeliveryWrites(t, b)

	var inHistory bool
	for _, msg := range b.ctxMgr.Snapshot() {
		if msg.Role == message.RoleUser && msg.Content == "restored from history" && msg.InputID == queued {
			inHistory = true
		}
	}
	if !inHistory {
		t.Fatal("restored session lost the committed next-request input from history")
	}
	for _, input := range b.InputQueueSnapshot().State.Inputs {
		if input.InputID == queued {
			t.Fatal("restored session still holds the committed next-request input in the queue")
		}
	}
}
