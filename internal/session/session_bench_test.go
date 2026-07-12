package session

import (
	"encoding/json"
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
	for i := 0; i < messageCount; i++ {
		msg := ExportedMessage{
			Role:      message.RoleAssistant,
			Content:   content,
			Timestamp: s.CreatedAt.Add(time.Duration(i) * time.Microsecond),
		}
		if i%3 == 0 {
			msg.Role = message.RoleUser
		} else if i%3 == 1 {
			msg.ToolCalls = []ExportedToolCall{{
				ID: fmt.Sprintf("call-%d", i), Name: "read", Args: fmt.Sprintf(`{"path":"file-%d.go"}`, i),
			}}
		} else {
			msg.Role = message.RoleTool
			msg.ToolCallID = fmt.Sprintf("call-%d", i-1)
		}
		s.Messages = append(s.Messages, msg)
	}
	return s
}

func BenchmarkImportFromBytesLargeSession(b *testing.B) {
	fixture, err := json.Marshal(benchmarkExportedSession(5000))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := ImportFromBytes(fixture)
		if err != nil {
			b.Fatal(err)
		}
		if len(s.Messages) != 5000 {
			b.Fatalf("messages = %d, want 5000", len(s.Messages))
		}
	}
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
