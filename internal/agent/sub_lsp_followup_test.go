package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

type recordingSubLSPFollowupSource struct {
	items   []LSPFollowupItem
	paths   []string
	baseDir string
	agentID string
}

func (s *recordingSubLSPFollowupSource) Pending(paths []string, displayBaseDir, agentID string) []LSPFollowupItem {
	s.paths = append([]string(nil), paths...)
	s.baseDir = displayBaseDir
	s.agentID = agentID
	return s.items
}

func TestSubAgentStagesAndPersistsLateLSPFollowup(t *testing.T) {
	acked := 0
	source := &recordingSubLSPFollowupSource{}
	source.items = []LSPFollowupItem{{
		DisplayPath: "main.go",
		Lines:       "[W] 3:1 unused value",
		Ack: func() {
			acked++
			source.items = nil
		},
	}}
	ctxMgr := ctxmgr.NewManager(0, 0)
	ctxMgr.Append(message.Message{
		Role: message.RoleTool,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{
			Path: "/tmp/project/main.go",
		}}},
	})
	s := &SubAgent{
		instanceID:        "agent-1",
		ctxMgr:            ctxMgr,
		workDir:           "/tmp/project",
		lspFollowupSource: source,
	}

	overlay, ok := s.buildLSPFollowupOverlay()
	if !ok {
		t.Fatal("expected a staged late LSP follow-up")
	}
	if !strings.Contains(overlay.Content, "unused value") {
		t.Fatalf("overlay content = %q, want the late diagnostic", overlay.Content)
	}
	if source.agentID != "agent-1" || source.baseDir != "/tmp/project" {
		t.Fatalf("source query = agent %q base %q, want the worker identity and workdir", source.agentID, source.baseDir)
	}
	if len(source.paths) != 1 || source.paths[0] != "/tmp/project/main.go" {
		t.Fatalf("source paths = %#v, want the written path", source.paths)
	}
	if acked != 0 {
		t.Fatalf("ack ran before the follow-up was persisted: %d", acked)
	}

	s.emitStagedLSPFollowup()
	if acked != 1 {
		t.Fatalf("ack count = %d, want 1 after persistence", acked)
	}
	history := ctxMgr.Snapshot()
	if len(history) != 2 || history[1].Kind != message.KindLSPFollowup {
		t.Fatalf("history = %#v, want a durable LSP follow-up row", history)
	}
	if !strings.Contains(history[1].Content, "unused value") {
		t.Fatalf("durable follow-up = %q, want the late diagnostic", history[1].Content)
	}
	if again, ok := s.buildLSPFollowupOverlay(); ok || again.Content != "" {
		t.Fatalf("staged follow-up was not consumed: %+v", again)
	}
}
