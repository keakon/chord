package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// The detail pass exists to fill in the preview a session list needs. It must
// not let that preview stand in for the original request: on a compacted history
// the scan skips the checkpoint and names the first prompt *after* it, and an
// original request is sticky — session lists prefer it and every later
// checkpoint copies it forward as its "Original request:" anchor.
func TestFillSessionSummaryDetailsKeepsScannedPreviewOutOfOriginal(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		t.Fatalf("projectSessionsDir: %v", err)
	}
	if !strings.HasPrefix(sessionsDir, projectRoot) {
		t.Fatalf("projectSessionsDir = %q, want a directory under the test project root", sessionsDir)
	}

	const sessionID = "compacted-legacy"
	rm := recovery.NewRecoveryManager(filepath.Join(sessionsDir, sessionID))
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "[Context Summary]\n## Goal\n- carry on", IsCompactionSummary: true},
		{Role: message.RoleAssistant, Content: "ack"},
		{Role: message.RoleUser, Content: "mid-session prompt"},
	} {
		if err := rm.PersistMessage("main", msg); err != nil {
			t.Fatalf("PersistMessage: %v", err)
		}
	}
	rm.Close()

	list := a.FillSessionSummaryDetails([]SessionSummary{{
		ID:           sessionID,
		MessageCount: UnknownSessionMessageCount,
	}})
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].FirstUserMessage != "mid-session prompt" {
		t.Fatalf("FirstUserMessage = %q, want the scanned preview", list[0].FirstUserMessage)
	}
	if list[0].OriginalFirstUserMessage != "" {
		t.Fatalf("OriginalFirstUserMessage = %q, want empty: a scanned preview is not the original request", list[0].OriginalFirstUserMessage)
	}
}

// TestFillSessionSummaryDetailsLoadsWorktreeName pins the picker's worktree
// column to its data source. Sessions are shared by every checkout of one
// repository, so without this the picker cannot tell a session created in a
// worktree from one in the main checkout.
func TestFillSessionSummaryDetailsLoadsWorktreeName(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		t.Fatalf("projectSessionsDir: %v", err)
	}

	const sessionID = "worktree-legacy"
	sessionPath := filepath.Join(sessionsDir, sessionID)
	rm := recovery.NewRecoveryManager(sessionPath)
	if err := rm.PersistMessage("main", message.Message{Role: message.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}
	rm.Close()
	if err := recovery.SaveSessionMeta(sessionPath, recovery.SessionMeta{WorktreeName: "feat-picker"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	list := a.FillSessionSummaryDetails([]SessionSummary{{
		ID:           sessionID,
		MessageCount: UnknownSessionMessageCount,
	}})
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].WorktreeName != "feat-picker" {
		t.Fatalf("WorktreeName = %q, want the recorded worktree", list[0].WorktreeName)
	}
}

func TestRestoredSubAgentBuilderTaskDescPrefersUserAuthoredMessage(t *testing.T) {
	b := newRestoredSubAgentBuilder("sub-1")
	b.attachTranscript([]message.Message{
		{Role: message.RoleUser, Content: "compaction checkpoint body", IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "  Refactor the parser  "},
		{Role: message.RoleUser, Content: "job finished", Kind: message.KindBackgroundResult},
	})
	if b.state.TaskDesc != "Refactor the parser" {
		t.Fatalf("TaskDesc = %q, want the trimmed user-authored task", b.state.TaskDesc)
	}
}

func TestRestoredSubAgentBuilderTaskDescEmptyWithoutUserAuthoredMessage(t *testing.T) {
	b := newRestoredSubAgentBuilder("sub-1")
	b.attachTranscript([]message.Message{
		{Role: message.RoleUser, Content: "compaction checkpoint body", IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "job finished", Kind: message.KindBackgroundResult},
	})
	if b.state.TaskDesc != "" {
		t.Fatalf("TaskDesc = %q, want empty when only synthetic messages exist", b.state.TaskDesc)
	}
}
