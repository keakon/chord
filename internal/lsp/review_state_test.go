package lsp

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func TestRebuildReviewSnapshotsFromMessagesUsesPathAndPaths(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "write-1", Name: toolname.Write, Args: json.RawMessage(`{"path":"a.go","content":"package main"}`)},
				{ID: "patch-1", Name: toolname.Edit, Args: json.RawMessage(`{"path":"b.go","patch":"@@\n-old\n+new\n"}`)},
				{ID: "delete-1", Name: toolname.Delete, Args: json.RawMessage(`{"paths":["a.go"],"reason":"cleanup"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "write-1",
			Content:    "Write completed.",
			LSPReviews: []message.LSPReview{{Path: "a.go", ServerID: "gopls", Errors: 2, Warnings: 1}},
		},
		{
			Role:       "tool",
			ToolCallID: "patch-1",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{Path: "b.go", ServerID: "gopls", Errors: 0, Warnings: 3}},
		},
		{
			Role:       "tool",
			ToolCallID: "delete-1",
			Content:    "Deleted (1):\n- a.go",
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{
		{Path: "b.go", ServerID: "gopls", Errors: 0, Warnings: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}

func TestRebuildReviewSnapshotsFromMessagesCleanReviewOverwritesStaleDiagnostics(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "patch-stale", Name: toolname.Edit, Args: json.RawMessage(`{"path":"a.go","patch":"@@\n-old\n+bad\n"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "patch-stale",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{Path: "a.go", ServerID: "gopls", Errors: 1, Warnings: 0}},
		},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "patch-clean", Name: toolname.Edit, Args: json.RawMessage(`{"path":"a.go","patch":"@@\n-bad\n+good\n"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "patch-clean",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{Path: "a.go", ServerID: "gopls", Errors: 0, Warnings: 0}},
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{
		{Path: "a.go", ServerID: "gopls", Errors: 0, Warnings: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}

func TestRebuildReviewSnapshotsFromMessagesRestoresLegacySingleFileReview(t *testing.T) {
	msgs := []message.Message{
		{
			Role:      "assistant",
			ToolCalls: []message.ToolCall{{ID: "edit", Name: toolname.Edit, Args: json.RawMessage(`{"path":"a.go","patch":"@@\n-old\n+new\n"}`)}},
		},
		{
			Role:       "tool",
			ToolCallID: "edit",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 1}},
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{{Path: "a.go", ServerID: "gopls", Errors: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}

func TestRebuildReviewSnapshotsFromMessagesApplyPatchCleanReview(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "patch-clean", Name: toolname.ApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+good\n*** End Patch"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "patch-clean",
			Content:    "Applied patch:\nM a.go",
			FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", Exists: true}}},
			LSPReviews: []message.LSPReview{{Path: "a.go", ServerID: "gopls", Errors: 0, Warnings: 0}},
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{{Path: "a.go", ServerID: "gopls", Errors: 0, Warnings: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}

func TestRebuildReviewSnapshotsFromMessagesApplyPatchKeepsPerFileCounts(t *testing.T) {
	msgs := []message.Message{
		{
			Role:      "assistant",
			ToolCalls: []message.ToolCall{{ID: "patch-many", Name: toolname.ApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.go\n@@\n-a\n+b\n*** Update File: b.go\n@@\n-c\n+d\n*** End Patch"}`)}},
		},
		{
			Role:       "tool",
			ToolCallID: "patch-many",
			Content:    "Applied patch:\nM a.go\nM b.go",
			FileState: &message.ToolFileState{Writes: []message.TrackedFileState{
				{Path: "a.go", Exists: true},
				{Path: "b.go", Exists: true},
			}},
			LSPReviews: []message.LSPReview{
				{Path: "a.go", ServerID: "gopls", Errors: 2, Warnings: 1},
				{Path: "b.go", ServerID: "gopls", Errors: 0, Warnings: 3},
			},
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{
		{Path: "a.go", ServerID: "gopls", Errors: 2, Warnings: 1},
		{Path: "b.go", ServerID: "gopls", Errors: 0, Warnings: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}

func TestRebuildReviewSnapshotsFromMessagesApplyPatchMoveRemovesSource(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "edit-old", Name: toolname.Edit, Args: json.RawMessage(`{"path":"old.go","patch":"@@\n-old\n+bad\n"}`)},
				{ID: "move", Name: toolname.ApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: old.go\n*** Move to: new.go\n@@\n-bad\n+good\n*** End Patch"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "edit-old",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{Path: "old.go", ServerID: "gopls", Errors: 1}},
		},
		{
			Role:       "tool",
			ToolCallID: "move",
			Content:    "Applied patch:\nR old.go -> new.go",
			FileState: &message.ToolFileState{
				Writes:  []message.TrackedFileState{{Path: "new.go", Exists: true}},
				Deletes: []message.TrackedFileState{{Path: "old.go", Exists: false}},
			},
			LSPReviews: []message.LSPReview{{Path: "new.go", ServerID: "gopls"}},
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{{Path: "new.go", ServerID: "gopls"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}

func TestRebuildReviewSnapshotsFromMessagesApplyPatchDeleteRemovesSnapshot(t *testing.T) {
	absPath := filepath.Join(t.TempDir(), "a.go")
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "edit", Name: toolname.Edit, Args: json.RawMessage(`{"path":"a.go","patch":"@@\n-old\n+bad\n"}`)},
				{ID: "delete", Name: toolname.ApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Delete File: a.go\n*** End Patch"}`)},
			},
		},
		{Role: "tool", ToolCallID: "edit", Content: "Edit completed.", LSPReviews: []message.LSPReview{{Path: "a.go", ServerID: "gopls", Errors: 1}}},
		{
			Role:       "tool",
			ToolCallID: "delete",
			Content:    "Applied patch:\nD a.go",
			FileState:  &message.ToolFileState{Deletes: []message.TrackedFileState{{Path: absPath, Exists: false}}},
		},
	}

	if got := RebuildReviewSnapshotsFromMessages(msgs); got != nil {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want nil", got)
	}
}

func TestRebuildReviewSnapshotsFromMessagesCanonicalizesReviewToFileStatePath(t *testing.T) {
	absPath := filepath.Join(t.TempDir(), "pkg", "a.go")
	msgs := []message.Message{
		{
			Role:      "assistant",
			ToolCalls: []message.ToolCall{{ID: "edit", Name: toolname.Edit, Args: json.RawMessage(`{"path":"pkg/a.go","patch":"@@\n-old\n+new\n"}`)}},
		},
		{
			Role:       "tool",
			ToolCallID: "edit",
			Content:    "Edit completed.",
			FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: absPath, Exists: true}}},
			LSPReviews: []message.LSPReview{{Path: "pkg/a.go", ServerID: "gopls", Errors: 1}},
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{{Path: absPath, ServerID: "gopls", Errors: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}

func TestRebuildReviewSnapshotsFromMessagesAcceptsStructuredPatchSuccess(t *testing.T) {
	msgs := []message.Message{
		{
			Role:      "assistant",
			ToolCalls: []message.ToolCall{{ID: "patch", Name: toolname.ApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n*** End Patch"}`)}},
		},
		{
			Role:       "tool",
			ToolCallID: "patch",
			Content:    "ApplyPatch tool completed",
			ToolStatus: message.ToolStatusSuccess,
			FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", Exists: true}}},
			LSPReviews: []message.LSPReview{{Path: "a.go", ServerID: "gopls", Warnings: 1}},
		},
	}

	got := RebuildReviewSnapshotsFromMessages(msgs)
	want := []ReviewedFileSnapshot{{Path: "a.go", ServerID: "gopls", Warnings: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildReviewSnapshotsFromMessages() = %#v, want %#v", got, want)
	}
}
