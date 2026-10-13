package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func BenchmarkDirectoryInstructionNoRules(b *testing.B) {
	for _, n := range []int{20, 500, 4096} {
		msgs := make([]message.Message, n)
		for i := range msgs {
			msgs[i] = message.Message{Role: message.RoleUser, Content: strings.Repeat("x", 2048)}
		}
		defs := make([]message.ToolDefinition, 100)
		for i := range defs {
			defs[i] = message.ToolDefinition{Name: fmt.Sprintf("tool-%d", i), InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}
		}
		r := &directoryInstructionRuntime{}
		r.view.Store(&directoryInstructionView{})
		prepare := prepareDirectoryInstructionAttempt(r, 1)
		surface := llm.RequestAttempt{Messages: msgs, Tools: defs, InputBudget: 1000000}
		b.Run(fmt.Sprintf("project/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := projectDirectoryInstructionHistory(msgs, r); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("attempt/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := prepare(context.Background(), surface); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
