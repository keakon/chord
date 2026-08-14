package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestSubAgentPartialPatchChangedFilesComeFromFileState verifies the SubAgent
// tool-result path attributes turn.ChangedFiles from the committed FileState
// instead of re-parsing every patch target, so files whose group failed in a
// partial apply_patch are not reported as changed to hooks.
func TestSubAgentPartialPatchChangedFilesComeFromFileState(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	// Keep a second call pending so the batch-completion path stays out of the
	// way; everything under test happens before the pending counter check.
	sub.turn.PendingToolCalls.Store(2)

	patch := "*** Begin Patch\n" +
		"*** Update File: committed.txt\n@@\n-a\n+b\n" +
		"*** Update File: failed.txt\n@@\n-x\n+y\n" +
		"*** End Patch"
	args, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	sub.handleToolResult(&toolResult{
		CallID:   "call-1",
		Name:     tools.NameApplyPatch,
		ArgsJSON: string(args),
		Result:   "apply_patch partially applied: 1 change committed; 1 file group not applied.",
		Error:    errors.New("apply_patch partially applied: 1 change committed, 1 file group not applied: failed.txt: expected content not found"),
		Diff:     "--- a/committed.txt\n+++ b/committed.txt\n@@\n-a\n+b\n",
		TurnID:   sub.turn.ID,
		FileState: &message.ToolFileState{
			Writes: []message.TrackedFileState{{Path: "committed.txt", Exists: true}},
		},
	})

	if len(sub.turn.ChangedFiles) != 1 {
		t.Fatalf("ChangedFiles = %#v, want exactly one entry", sub.turn.ChangedFiles)
	}
	changed, _ := sub.turn.ChangedFiles[0].(map[string]any)
	paths, _ := changed["paths"].([]string)
	if len(paths) != 1 || paths[0] != "committed.txt" {
		t.Fatalf("changed paths = %#v, want only the committed file", paths)
	}
	var resultEvent *ToolResultEvent
	for _, evt := range drainAgentEvents(parent.Events()) {
		if got, ok := evt.(ToolResultEvent); ok && got.CallID == "call-1" {
			resultEvent = &got
			break
		}
	}
	if resultEvent == nil || resultEvent.FileState == nil || len(resultEvent.FileState.Writes) != 1 || resultEvent.FileState.Writes[0].Path != "committed.txt" {
		t.Fatalf("ToolResultEvent FileState = %#v, want only committed.txt", resultEvent)
	}
}
