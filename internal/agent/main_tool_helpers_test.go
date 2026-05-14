package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestBuildToolExecutionBatchesKeepsMutationsAsBoundaries(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.GrepTool{})
	registry.Register(tools.WriteTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
		{ID: "2", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"README.md","content":"x"}`)},
		{ID: "3", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","path":"."}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 3 {
		t.Fatalf("len(batches) = %d, want 3", len(batches))
	}
	for i, wantID := range []string{"1", "2", "3"} {
		if len(batches[i].Calls) != 1 || batches[i].Calls[0].ID != wantID {
			t.Fatalf("batch[%d] = %#v, want single call %s", i, batches[i].Calls, wantID)
		}
	}
}

func TestBuildToolExecutionBatchesGroupsOnlyConsecutiveReadOnlyCalls(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.GrepTool{})
	registry.Register(tools.GlobTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
		{ID: "2", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","path":"docs"}`)},
		{ID: "3", Name: tools.NameGlob, Args: json.RawMessage(`{"path":"src","pattern":"**/*.go"}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 1 {
		t.Fatalf("len(batches) = %d, want 1", len(batches))
	}
	if len(batches[0].Calls) != 3 {
		t.Fatalf("len(batches[0].Calls) = %d, want 3", len(batches[0].Calls))
	}
}

func TestBuildToolExecutionBatchesSplitsDirectoryReadFromFileWrite(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.GrepTool{})
	registry.Register(tools.WriteTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","path":"."}`)},
		{ID: "2", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"internal/agent/main.go","content":"x"}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 2 {
		t.Fatalf("len(batches) = %d, want 2", len(batches))
	}
}
