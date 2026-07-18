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
	for i := 0; i < toolResults; i++ {
		id := fmt.Sprintf("read-%d", i)
		args, _ := json.Marshal(map[string]any{"path": fmt.Sprintf("file-%d.go", i)})
		messages = append(messages,
			message.Message{Role: message.RoleUser, Content: fmt.Sprintf("inspect file %d", i)},
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameRead, Args: args}}},
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

// BenchmarkPrepareMessagesForLLMForcePruneRescan measures the bookkeeping-heavy
// worst case: a stable surface exists but context pressure (no input budget →
// usage 1.0 ≥ ForcePruneUsage) rejects reuse, so every call pays the full
// reduction scan plus surface snapshot/remember costs.
func BenchmarkPrepareMessagesForLLMForcePruneRescan(b *testing.B) {
	for _, toolResults := range []int{100, 1000} {
		b.Run(fmt.Sprintf("tool_results_%d", toolResults), func(b *testing.B) {
			a := benchmarkContextReductionAgent()
			messages := benchmarkContextReductionMessages(toolResults)
			first := a.prepareMessagesForLLM(messages)
			if !hasReductionSavings(a.GetContextReductionStats()) {
				b.Fatal("benchmark fixture did not produce reduction savings")
			}
			withTail := append(append([]message.Message(nil), messages...), message.Message{Role: message.RoleAssistant, Content: "small follow-up"})
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

// benchmarkContextReductionAgentNoPressure disables the usage-pressure
// rejections so the stable-prefix reuse fast path can engage even though the
// fixture agent has no LLM client (input budget 0 → usage reported as 1.0).
func benchmarkContextReductionAgentNoPressure() *MainAgent {
	a := benchmarkContextReductionAgent()
	a.projectConfig.Context.Reduction.HighPressureUsage = 4
	a.projectConfig.Context.Reduction.ForcePruneUsage = 4
	return a
}

// BenchmarkPrepareMessagesForLLMStablePrefixReuse measures the intended steady
// state: an unchanged prefix is detected via the stored shape source and the
// previous reduced surface is reused without re-running the reduction scan.
func BenchmarkPrepareMessagesForLLMStablePrefixReuse(b *testing.B) {
	for _, toolResults := range []int{100, 1000} {
		b.Run(fmt.Sprintf("tool_results_%d", toolResults), func(b *testing.B) {
			a := benchmarkContextReductionAgentNoPressure()
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
