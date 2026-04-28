package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestRebuildTouchedPathsFromMessagesTracksWritesEditsAndDeletes(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "write-1", Name: "Write", Args: mustToolArgs(t, map[string]any{"path": "foo.go"})},
			{ID: "edit-1", Name: "Edit", Args: mustToolArgs(t, map[string]any{"path": "bar.go"})},
			{ID: "delete-1", Name: "Delete", Args: mustToolArgs(t, map[string]any{"paths": []string{"foo.go"}, "reason": "cleanup"})},
			{ID: "delete-2", Name: "Delete", Args: mustToolArgs(t, map[string]any{"paths": []string{"baz.go"}, "reason": "cleanup"})},
		}},
		{Role: "tool", ToolCallID: "write-1", Content: "ok"},
		{Role: "tool", ToolCallID: "edit-1", Content: "updated"},
		{Role: "tool", ToolCallID: "delete-1", Content: "Delete completed.\n\nDeleted (1):\n- foo.go"},
		{Role: "tool", ToolCallID: "delete-2", Content: "Cancelled"},
	}
	got := RebuildTouchedPathsFromMessages(msgs)
	want := []string{"bar.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RebuildTouchedPathsFromMessages() = %#v, want %#v", got, want)
	}
}

func mustToolArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return b
}
