package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func writeBackgroundResultMailboxRow(t *testing.T, sessionDir, messageID, summary string) {
	t.Helper()
	path := filepath.Join(sessionDir, "subagents", "mailbox.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(mailbox.jsonl): %v", err)
	}
	err = json.NewEncoder(f).Encode(SubAgentMailboxMessage{
		MessageID:   messageID,
		Kind:        SubAgentMailboxKindBackgroundResult,
		Priority:    SubAgentMailboxPriorityNotify,
		MessageType: AgentMessageTypeNotice,
		Summary:     summary,
		CreatedAt:   time.Now(),
	})
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("append background_result mailbox row: %v", err)
	}
}

// A background result queued behind a busy turn is durable before it is shown,
// so a restart replays it instead of dropping it.
func TestRestoreReplaysUndeliveredBackgroundResult(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "background-result-replay")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: message.RoleUser, Content: "Investigate the flaky test"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()
	writeBackgroundResultMailboxRow(t, sessionDir, "background-job-1", "Run production build")

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	queued := append([]SubAgentMailboxMessage{}, a.subAgentInbox.urgent...)
	queued = append(queued, a.subAgentInbox.normal...)
	if len(queued) != 1 || queued[0].Kind != SubAgentMailboxKindBackgroundResult {
		t.Fatalf("restored main inbox = %#v, want the undelivered background result", queued)
	}

	if !a.stageNextSubAgentMailboxBatch() {
		// Restore parks delivery until the next user interaction; unpause to
		// exercise the boundary delivery the parked state allows.
		a.mailboxDeliveryPaused.Store(false)
		if !a.stageNextSubAgentMailboxBatch() {
			t.Fatal("staging the restored background result failed")
		}
	}
	overlays := a.buildTurnOverlayMessages()
	if len(overlays) != 1 || overlays[0].Kind != message.KindBackgroundResult {
		t.Fatalf("restored delivery overlays = %#v, want one KindBackgroundResult message", overlays)
	}
	if !containsBackgroundResultContent(a.ctxMgr.Snapshot(), "Run production build") {
		t.Fatalf("context = %#v, want the replayed background result", a.ctxMgr.Snapshot())
	}
}

// A background result whose transcript row is already durable was seen by the
// model; restoring must not replay it even though its consumed ack never
// landed before the crash.
func TestRestoreSkipsDeliveredBackgroundResult(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "background-result-delivered")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const messageID = "background-job-1"
	const content = "[Background job background-job-1 completed]\n\nDescription: Run production build\nStatus: completed (exit code 0)"
	rm := recovery.NewRecoveryManager(sessionDir)
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "Investigate the flaky test"},
		{
			Role:    message.RoleUser,
			Kind:    message.KindBackgroundResult,
			Content: content,
			Mailbox: &message.MailboxMetadata{MessageID: messageID, Kind: string(SubAgentMailboxKindBackgroundResult)},
		},
	} {
		if err := rm.PersistMessage("main", msg); err != nil {
			t.Fatalf("PersistMessage(main): %v", err)
		}
	}
	rm.Close()
	writeBackgroundResultMailboxRow(t, sessionDir, messageID, "Run production build")

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	queued := append([]SubAgentMailboxMessage{}, a.subAgentInbox.urgent...)
	queued = append(queued, a.subAgentInbox.normal...)
	if len(queued) != 0 {
		t.Fatalf("restored main inbox = %#v, want nothing: the result is already durable in the transcript", queued)
	}
}

func containsBackgroundResultContent(msgs []message.Message, want string) bool {
	for _, msg := range msgs {
		if msg.Kind == message.KindBackgroundResult && strings.Contains(msg.Content, want) {
			return true
		}
	}
	return false
}
