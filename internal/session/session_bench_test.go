package session

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func benchmarkExportedSession(messageCount int) *ExportedSession {
	s := &ExportedSession{
		Version:   CurrentVersion,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		Messages:  make([]ExportedMessage, 0, messageCount),
	}
	content := strings.Repeat("session transcript line\n", 20)
	for i := range messageCount {
		msg := ExportedMessage{
			Role:      message.RoleAssistant,
			Content:   content,
			Timestamp: s.CreatedAt.Add(time.Duration(i) * time.Microsecond),
		}
		switch i % 3 {
		case 0:
			msg.Role = message.RoleUser
		case 1:
			msg.ToolCalls = []ExportedToolCall{{
				ID: fmt.Sprintf("call-%d", i), Name: "read", Args: fmt.Sprintf(`{"path":"file-%d.go"}`, i),
			}}
		default:
			msg.Role = message.RoleTool
			msg.ToolCallID = fmt.Sprintf("call-%d", i-1)
		}
		s.Messages = append(s.Messages, msg)
	}
	return s
}

func BenchmarkExportedSessionToMessagesLargeSession(b *testing.B) {
	s := benchmarkExportedSession(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		messages := s.ToMessages()
		if len(messages) != 5000 {
			b.Fatalf("messages = %d, want 5000", len(messages))
		}
	}
}
