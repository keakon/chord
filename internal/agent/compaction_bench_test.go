package agent

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func benchmarkContextReductionMessages(toolResults int) []message.Message {
	messages := make([]message.Message, 0, toolResults*3+1)
	output := strings.Repeat("line from source file\n", 100)
	for i := range toolResults {
		id := fmt.Sprintf("read-%d", i)
		args, _ := json.Marshal(map[string]any{"url": fmt.Sprintf("https://example.com/file-%d", i)})
		messages = append(messages,
			message.Message{Role: message.RoleUser, Content: fmt.Sprintf("inspect file %d", i)},
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameWebFetch, Args: args}}},
			message.Message{Role: message.RoleTool, ToolCallID: id, Content: output},
		)
	}
	return append(messages, message.Message{Role: message.RoleUser, Content: "continue"})
}

func benchmarkContextReductionAgent() *MainAgent {
	cfg := config.DefaultConfig()
	cfg.Context.Reduction.ReadLikeAgeTurns = 1
	cfg.Context.Reduction.ReadLikeOutputBytes = 80
	cfg.Context.Reduction.MinToolResultsPrune = 1
	cfg.Context.Reduction.MinIncrementalTokens = 2048
	a := &MainAgent{
		projectConfig: cfg,
		turn:          &Turn{ID: 1},
	}
	a.freezeToolSurfaceFromDefinitions(nil)
	return a
}

func BenchmarkPrepareMessagesForLLMCold(b *testing.B) {
	for _, toolResults := range []int{100, 1000} {
		b.Run(fmt.Sprintf("tool_results_%d", toolResults), func(b *testing.B) {
			a := benchmarkContextReductionAgent()
			messages := benchmarkContextReductionMessages(toolResults)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				prepared := a.prepareMessagesForLLMWithOptions(messages, false)
				if len(prepared) != len(messages) {
					b.Fatalf("prepared messages = %d, want %d", len(prepared), len(messages))
				}
			}
		})
	}
}

func TestPrepareMessagesForLLMColdAllocsGuard(t *testing.T) {
	a := benchmarkContextReductionAgent()
	messages := benchmarkContextReductionMessages(100)
	var prepared []message.Message
	allocs := testing.AllocsPerRun(100, func() {
		prepared = a.prepareMessagesForLLMWithOptions(messages, false)
		if len(prepared) != len(messages) {
			t.Fatalf("prepared messages = %d, want %d", len(prepared), len(messages))
		}
	})
	if !hasReductionSavings(a.GetContextReductionStats()) {
		t.Fatal("allocation guard fixture did not produce reduction savings")
	}
	reduced := false
	for _, msg := range prepared {
		if msg.Role == message.RoleTool && msg.Content != strings.Repeat("line from source file\n", 100) {
			reduced = true
			break
		}
	}
	if !reduced {
		t.Fatal("allocation guard fixture did not reduce any tool result")
	}
	// Parsed read requests are cached in tool-call metadata so validity analysis
	// and reduction do not decode the same arguments twice. Keep the original
	// cold-path budget to prevent that shared work from regressing again.
	maxAllocs := 1700.0
	mode := "normal"
	if testBinaryBuiltWithRace() {
		// Race instrumentation adds allocations to this path. Keep a separate
		// budget so normal builds retain the tighter performance guard while
		// race builds still catch meaningful allocation regressions.
		maxAllocs = 1750
		mode = "race"
	}
	if allocs > maxAllocs {
		t.Fatalf("cold context reduction allocs = %.0f, want ≤%.0f (%s build)", allocs, maxAllocs, mode)
	}
}

func testBinaryBuiltWithRace() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, setting := range info.Settings {
		if setting.Key == "-race" {
			return setting.Value == "true"
		}
	}
	return false
}

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
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: patchID, Name: tools.NameApplyPatch, Args: json.RawMessage(`{"path":"` + path + `","patch":"@@"}`)}}},
			message.Message{Role: message.RoleTool, ToolCallID: patchID, ToolStatus: "success", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: path, Exists: true}}}},
		)
	}
	callMeta := buildToolCallMeta(messages)
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
	callMeta := buildToolCallMeta(messages)
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

// BenchmarkPrepareMessagesForLLMStablePrefixReuse measures the intended steady
// state: an unchanged prefix is detected via the stored shape source and the
// previous reduced surface is reused without re-running the reduction scan.
func BenchmarkPrepareMessagesForLLMStablePrefixReuse(b *testing.B) {
	for _, toolResults := range []int{100, 1000} {
		b.Run(fmt.Sprintf("tool_results_%d", toolResults), func(b *testing.B) {
			a := benchmarkContextReductionAgent()
			messages := benchmarkContextReductionMessages(toolResults)
			first := a.prepareMessagesForLLM(messages)
			if !hasReductionSavings(a.GetContextReductionStats()) {
				b.Fatal("benchmark fixture did not produce reduction savings")
			}
			withTail := append(append([]message.Message(nil), messages...), message.Message{Role: message.RoleAssistant, Content: "small follow-up"})
			warm := a.prepareMessagesForLLM(withTail)
			if len(warm) != len(withTail) {
				b.Fatalf("warm prepared messages = %d, want %d", len(warm), len(withTail))
			}
			if !a.GetContextReductionStats().ReusedStable {
				b.Fatal("benchmark fixture did not engage stable-prefix reuse")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				prepared := a.prepareMessagesForLLM(withTail)
				if len(prepared) != len(withTail) || len(first) != len(messages) {
					b.Fatalf("prepared messages = %d, want %d", len(prepared), len(withTail))
				}
			}
		})
	}
}
