package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func benchmarkLargeSessionMessages(messageCount int) []message.Message {
	messages := make([]message.Message, 0, messageCount)
	content := strings.Repeat("session transcript line\n", 20)
	for i := 0; i < messageCount; i++ {
		switch i % 3 {
		case 0:
			messages = append(messages, message.Message{Role: message.RoleUser, Content: fmt.Sprintf("inspect file %d", i)})
		case 1:
			id := fmt.Sprintf("call-%d", i)
			messages = append(messages, message.Message{
				Role:    message.RoleAssistant,
				Content: content,
				ToolCalls: []message.ToolCall{{
					ID: id, Name: tools.NameRead, Args: []byte(fmt.Sprintf(`{"path":"file-%d.go"}`, i)),
				}},
			})
		case 2:
			messages = append(messages, message.Message{
				Role: message.RoleTool, ToolCallID: fmt.Sprintf("call-%d", i-1), Content: content,
			})
		}
	}
	return messages
}

func BenchmarkMessagesToBlocksLargeSession(b *testing.B) {
	messages := benchmarkLargeSessionMessages(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		nextID := 0
		blocks := messagesToBlocks(messages, &nextID)
		if len(blocks) == 0 || nextID != len(blocks) {
			b.Fatalf("blocks = %d, next ID = %d", len(blocks), nextID)
		}
	}
}
