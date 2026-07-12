package agent

import (
	"encoding/json"
	"fmt"
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
	allocs := testing.AllocsPerRun(10, func() {
		prepared := a.prepareMessagesForLLMWithOptions(messages, false)
		if len(prepared) != len(messages) {
			t.Fatalf("prepared messages = %d, want %d", len(prepared), len(messages))
		}
	})
	const maxAllocs = 1700
	if allocs > maxAllocs {
		t.Fatalf("cold context reduction allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

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
