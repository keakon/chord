package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestBuildFileEvidenceViewSharesReadValidity(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", Content: "READ_RESULT lines=1-10 total=10\nold", ToolStatus: "success", FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "a.go", SHA256: "old", Exists: true}}}},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "edit", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "edit", Content: "updated", ToolStatus: "success", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", SHA256: "new", Exists: true, ChangedStart: 4, ChangedEnd: 4}}}},
	}

	view := buildFileEvidenceView(messages, "session-a", "current")
	observations := view["a.go"]
	if len(observations) != 2 {
		t.Fatalf("observations = %#v", observations)
	}
	if observations[0].Validity != fileEvidenceStale || observations[0].SourceRef.LegacyOrdinal != 1 {
		t.Fatalf("read observation = %#v", observations[0])
	}
	if observations[1].Operation != "write" || observations[1].ObservedRevision != "new" {
		t.Fatalf("write observation = %#v", observations[1])
	}
	if got := view.validityByMessage()[1]; !got.Invalidated || got.Superseded {
		t.Fatalf("validity = %#v", got)
	}
}

func TestBuildFileEvidenceViewMarksUnknownWriteConservatively(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", Content: "READ_RESULT lines=1-10 total=10\nold", ToolStatus: "success", FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "a.go", SHA256: "old", Exists: true}}}},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "write", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "write", Content: "updated", ToolStatus: "success", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", SHA256: "new", Exists: true}}}},
	}

	view := buildFileEvidenceView(messages, "session-a", "current")
	if got := view["a.go"][0].Validity; got != fileEvidenceStale {
		t.Fatalf("unknown-range read validity = %q, want stale", got)
	}
}

func TestBuildFileEvidenceViewIncludesCommittedFilesFromPartialError(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "patch", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\\n*** End Patch"}`)}}},
		{Role: message.RoleTool, ToolCallID: "patch", Content: "partially applied\n\nError: one file failed", ToolStatus: message.ToolStatusError, FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", SHA256: "new", Exists: true}}}},
	}

	view := buildFileEvidenceView(messages, "session-a", "current")
	observations := view["a.go"]
	if len(observations) != 1 || observations[0].Operation != "write" || observations[0].ObservedRevision != "new" {
		t.Fatalf("observations = %#v, want committed partial write", observations)
	}
}
