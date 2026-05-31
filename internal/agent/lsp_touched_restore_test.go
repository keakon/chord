package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestRebuildTouchedPathsFromMessagesTracksWritesApplyPatchesAndDeletes(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "write-1", Name: "Write", Args: mustToolArgs(t, map[string]any{"path": "foo.go"})},
			{ID: "patch-1", Name: "ApplyPatch", Args: mustToolArgs(t, map[string]any{"patch": "*** Begin Patch\n*** Update File: bar.go\n@@\n-old\n+new\n*** End Patch\n"})},
			{ID: "delete-1", Name: "Delete", Args: mustToolArgs(t, map[string]any{"paths": []string{"foo.go"}, "reason": "cleanup"})},
			{ID: "delete-2", Name: "Delete", Args: mustToolArgs(t, map[string]any{"paths": []string{"baz.go"}, "reason": "cleanup"})},
		}},
		{Role: "tool", ToolCallID: "write-1", Content: "ok"},
		{Role: "tool", ToolCallID: "patch-1", Content: "updated"},
		{Role: "tool", ToolCallID: "delete-1", Content: "Delete completed.\n\nDeleted (1):\n- foo.go"},
		{Role: "tool", ToolCallID: "delete-2", Content: "Cancelled"},
	}
	got := RebuildTouchedPathsFromMessages(msgs)
	want := []string{tools.ExtractApplyPatchPathFromArgs(mustToolArgs(t, map[string]any{"patch": "*** Begin Patch\n*** Update File: bar.go\n@@\n-old\n+new\n*** End Patch\n"}))}
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
