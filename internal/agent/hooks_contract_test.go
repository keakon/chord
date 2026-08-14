package agent

import (
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestApplyBeforeToolResultAppendHook(t *testing.T) {
	display, contextResult := applyBeforeToolResultAppendHook("display", "context", nil)
	if display != "display" || contextResult != "context" {
		t.Fatalf("nil hook changed results: display=%q context=%q", display, contextResult)
	}
	display, contextResult = applyBeforeToolResultAppendHook("display", "context", &hook.Result{Action: hook.ActionContinue})
	if display != "display" || contextResult != "context" {
		t.Fatalf("continue hook changed results: display=%q context=%q", display, contextResult)
	}
	display, contextResult = applyBeforeToolResultAppendHook("display", "context", &hook.Result{
		Action: hook.ActionModify,
		Data: map[string]any{
			"display_result": "display from hook",
			"context_result": "context from hook",
		},
	})
	if display != "display from hook" || contextResult != "context from hook" {
		t.Fatalf("modify hook results: display=%q context=%q", display, contextResult)
	}
	display, contextResult = applyBeforeToolResultAppendHook("display", "context", &hook.Result{Action: hook.ActionModify, Data: "ignored"})
	if display != "display" || contextResult != "context" {
		t.Fatalf("invalid modify payload changed results: display=%q context=%q", display, contextResult)
	}
}

func TestChangedFileSummaryDeleteUsesDeletedResultPaths(t *testing.T) {
	payload := &ToolResultPayload{
		Name:     tools.NameDelete,
		ArgsJSON: `{"paths":["old.txt"],"reason":"cleanup"}`,
		Result:   "Deleted (1):\n- old.txt\nAlready absent (1):\n- missing.txt",
	}
	summary := changedFileSummary(payload)
	if summary == nil {
		t.Fatal("changedFileSummary returned nil for deleted file")
	}
	if summary["path"] != "old.txt" || summary["tool"] != tools.NameDelete || summary["is_deleted"] != true {
		t.Fatalf("delete changed summary = %#v", summary)
	}
	paths, ok := summary["paths"].([]string)
	if !ok || len(paths) != 1 || paths[0] != "old.txt" {
		t.Fatalf("delete changed paths = %#v", summary["paths"])
	}

	payload.Result = "Already absent (1):\n- old.txt"
	if changedFileSummary(payload) != nil {
		t.Fatal("delete without deleted paths should not produce changed file summary")
	}
}

func TestChangedFileSummaryPartialPatchUsesCommittedFileState(t *testing.T) {
	payload := &ToolResultPayload{
		Name:     tools.NameApplyPatch,
		ArgsJSON: `{"patch":"*** Begin Patch\n*** Update File: committed.go\n@@\n-old\n+new\n*** Update File: failed.go\n@@\n-missing\n+never\n*** End Patch"}`,
		Diff:     "--- committed.go\n+++ committed.go\n",
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{
			{Path: "committed.go", Exists: true},
		}},
	}
	summary := changedFileSummary(payload)
	if summary == nil {
		t.Fatal("changedFileSummary returned nil")
	}
	paths, ok := summary["paths"].([]string)
	if !ok || len(paths) != 1 || paths[0] != "committed.go" {
		t.Fatalf("paths = %#v, want only committed.go", summary["paths"])
	}
}

func TestToolResultHookDataPartialPatchUsesCommittedFileState(t *testing.T) {
	const argsJSON = `{"patch":"*** Begin Patch\n*** Update File: committed.go\n@@\n-old\n+new\n*** Update File: failed.go\n@@\n-missing\n+never\n*** End Patch"}`
	fileState := &message.ToolFileState{Writes: []message.TrackedFileState{
		{Path: "committed.go", Exists: true},
	}}

	for name, data := range map[string]map[string]any{
		"before result append": buildBeforeToolResultAppendData(tools.NameApplyPatch, argsJSON, "raw", "display", "context", nil, nil, fileState),
		"tool result":          buildToolResultHookData(tools.NameApplyPatch, argsJSON, "context", nil, "diff", nil, fileState),
	} {
		paths, ok := data["paths"].([]string)
		if !ok || len(paths) != 1 || paths[0] != "committed.go" {
			t.Fatalf("%s paths = %#v, want only committed.go", name, data["paths"])
		}
		if data["path"] != "committed.go" {
			t.Fatalf("%s path = %#v, want committed.go", name, data["path"])
		}
	}
}

func TestAppendHookFeedbackAppendsUserMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.appendHookFeedback("automated feedback")
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) == 0 {
		t.Fatal("main hook feedback was not appended")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "automated feedback" {
		t.Fatalf("main hook feedback message = %#v", last)
	}

	sub := &SubAgent{ctxMgr: ctxmgr.NewManager(8192, 0)}
	sub.appendHookFeedback("sub feedback")
	subMsgs := sub.ctxMgr.Snapshot()
	if len(subMsgs) != 1 || subMsgs[0].Role != "user" || subMsgs[0].Content != "sub feedback" {
		t.Fatalf("sub hook feedback messages = %#v", subMsgs)
	}
}
