package requesttrim

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// BenchmarkAnalyzeReadValidityReadEditPaths covers the path-matching workload
// that is absent from the generic reduction fixtures: many reads followed by
// edits, with normal FileState metadata available. The suffix index should
// keep this workload proportional to path segments rather than read*edit
// comparisons.
func BenchmarkAnalyzeReadValidityReadEditPaths(b *testing.B) {
	const count = 1000
	messages := make([]message.Message, 0, count*3)
	for i := range count {
		path := fmt.Sprintf("/tmp/worktree/pkg%d/internal/file%d.go", i%20, i)
		readID := fmt.Sprintf("read-%d", i)
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: readID, Name: tools.NameRead, Args: json.RawMessage(`{"path":"` + path + `"}`)}}},
			message.Message{Role: message.RoleTool, ToolCallID: readID, Content: "READ_RESULT lines=1-40 total=40\npackage p", FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: path, Exists: true}}}},
		)
	}
	for i := range count {
		path := fmt.Sprintf("pkg%d/internal/file%d.go", i%20, i)
		patchID := fmt.Sprintf("patch-%d", i)
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: patchID, Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: ` + path + `\n@@\n*** End Patch"}`)}}},
			message.Message{Role: message.RoleTool, ToolCallID: patchID, ToolStatus: "success", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: path, Exists: true}}}},
		)
	}
	callMeta := BuildToolCallMeta(messages)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		validity := analyzeReadValidity(messages, callMeta)
		if len(validity) != count {
			b.Fatalf("validity entries = %d, want %d", len(validity), count)
		}
	}
}

// BenchmarkAnalyzeReadValidityRepeatedPath guards the long-session case where
// one file is read repeatedly. Superseded detection must not compare every
// older read with every newer read.
func BenchmarkAnalyzeReadValidityRepeatedPath(b *testing.B) {
	const count = 1000
	messages := make([]message.Message, 0, count*2)
	for i := range count {
		readID := fmt.Sprintf("read-%d", i)
		start := i%100 + 1
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: readID, Name: tools.NameRead, Args: json.RawMessage(`{"path":"shared.go"}`)}}},
			message.Message{Role: message.RoleTool, ToolCallID: readID, Content: tools.FormatReadResultHeader(fmt.Sprintf("%d-%d", start, start+99), 200, "", "", "") + "\npackage p"},
		)
	}
	callMeta := BuildToolCallMeta(messages)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		validity := analyzeReadValidity(messages, callMeta)
		const distinctRanges = 100
		if len(validity) != count-distinctRanges {
			b.Fatalf("validity entries = %d, want %d", len(validity), count-distinctRanges)
		}
	}
}
