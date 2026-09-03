package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestArchivedHistoryRecoverableViaRead covers the archived-history recall
// loop end to end:
// after a compaction exports the head to history-N.md, the checkpoint map lists
// the archive with its topics, and the read tool can recover the exact archived
// content in one call — the model needs no new query tool.
func TestArchivedHistoryRecoverableViaRead(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	head := []message.Message{
		{Role: message.RoleUser, Content: "保持现有 API 行为不变，继续修复 bug"},
		{Role: message.RoleAssistant, Content: "排查编译错误"},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: "go build output", ToolStatus: string(ToolResultStatusSuccess)},
	}
	index := 1
	absPath, _, _, err := a.exportCompactionHistory(head, index, []string{"保持现有 API 行为不变"}, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("export history: %v", err)
	}

	// The checkpoint map line pairs the archive path with its topics.
	refs, err := listHistoryReferences(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := readCompactionHistoryMetas(refs)
	lines := formatHistoryMapLines(refs, metas)
	if len(lines) != 1 || !strings.Contains(lines[0], "history-1.md") || !strings.Contains(lines[0], "保持现有 API 行为不变") {
		t.Fatalf("checkpoint map line = %q, want path+topics", lines[0])
	}

	// The read tool recovers the archived content in one call.
	reader := tools.ReadTool{}
	args, err := json.Marshal(map[string]string{"path": absPath})
	if err != nil {
		t.Fatalf("marshal read args: %v", err)
	}
	out, err := reader.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("read archived history: %v", err)
	}
	if !strings.Contains(out, "保持现有 API 行为不变") || !strings.Contains(out, "go build output") {
		t.Fatalf("read did not recover the archived payload: %q", out)
	}
	if !strings.Contains(out, "READ_RESULT") {
		t.Fatalf("read output missing the READ_RESULT header: %q", out)
	}
}

// TestArchivedHistoryMapCarriesRecoveryGuidance verifies the checkpoint wrapper
// explicitly tells the model to read the archive with the read tool.
func TestArchivedHistoryMapCarriesRecoveryGuidance(t *testing.T) {
	checkpoint := buildCompactionCheckpointMessage("## Goal\n- continue", []string{"history-1.md: topic"}, "model_summary", nil)
	if !strings.Contains(checkpoint, "read the matching file with the read tool") {
		t.Fatalf("checkpoint missing recovery guidance: %q", checkpoint)
	}
	if !strings.Contains(checkpoint, "history-1.md: topic") {
		t.Fatalf("checkpoint missing the map line: %q", checkpoint)
	}
}
