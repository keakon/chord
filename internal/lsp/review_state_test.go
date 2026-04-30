package lsp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestRebuildReviewSnapshotsFromMessagesUsesPathAndPaths(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "write-1", Name: "Write", Args: json.RawMessage(`{"path":"a.go","content":"package main"}`)},
				{ID: "edit-1", Name: "Edit", Args: json.RawMessage(`{"path":"b.go","old_string":"old","new_string":"new"}`)},
				{ID: "delete-1", Name: "Delete", Args: json.RawMessage(`{"paths":["a.go"],"reason":"cleanup"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "write-1",
			Content:    "Write completed.",
			LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 2, Warnings: 1}},
		},
		{
			Role:       "tool",
			ToolCallID: "edit-1",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 0, Warnings: 3}},
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
				{ID: "edit-stale", Name: "Edit", Args: json.RawMessage(`{"path":"a.go","old_string":"old","new_string":"bad"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "edit-stale",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 1, Warnings: 0}},
		},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "edit-clean", Name: "Edit", Args: json.RawMessage(`{"path":"a.go","old_string":"bad","new_string":"good"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "edit-clean",
			Content:    "Edit completed.",
			LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 0, Warnings: 0}},
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
