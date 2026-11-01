package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestTaskFileEvidenceSurvivesCompactionRewriteFailure(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	messages := []message.Message{{Role: message.RoleUser, Content: "Inspect the workspace."}}
	for i := range 24 {
		callID := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameWrite}}},
			message.Message{
				Role: message.RoleTool, ToolCallID: callID, Content: strings.Repeat("output ", 200),
				ToolChangedPaths: []string{fmt.Sprintf("file-%02d.go", i)}, FileAttributionIncomplete: i == 0,
			},
		)
	}
	sub.ctxMgr.RestoreMessages(messages)
	sub.restoreTaskToolChanges(messages)
	manager := sub.recoveryManager()
	if manager == nil {
		t.Fatal("missing durable recovery manager")
	}
	parent.flushPersist()
	if err := manager.RewriteLog(sub.instanceID, messages); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(sub.sessionDir, "agents", sub.instanceID+".jsonl")
	beforeLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeMessages := sub.ctxMgr.Snapshot()
	beforePaths, beforeIncomplete := sub.taskChangeSnapshot()

	target := estimateMessagesTokens(sub.ctxMgr, beforeMessages) * 2 / 3
	checkpointBuilt := false
	compressed, applied, reason := sub.compactContextForTargetWithCheckpoint(beforeMessages, target, "context budget protection", func(removed int, archiveRef string) string {
		checkpointBuilt = true
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		return buildSubAgentStructuredCheckpoint(sub, beforeMessages, removed, "context budget protection", archiveRef)
	})
	if !checkpointBuilt {
		t.Fatal("compaction returned before constructing the checkpoint")
	}
	if applied || compressed != nil || reason != subModelDrivenSkipRewriteFailed {
		t.Fatalf("compaction = (%#v, %v, %q), want rewrite failure", compressed, applied, reason)
	}
	if !reflect.DeepEqual(sub.ctxMgr.Snapshot(), beforeMessages) {
		t.Fatal("failed rewrite replaced the in-memory history")
	}
	afterPaths, afterIncomplete := sub.taskChangeSnapshot()
	if !reflect.DeepEqual(afterPaths, beforePaths) || afterIncomplete != beforeIncomplete {
		t.Fatal("failed rewrite changed runtime file attribution")
	}
	afterLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterLog, beforeLog) {
		t.Fatal("failed rewrite changed the durable history")
	}
}
