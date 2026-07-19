package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func benchmarkRecoveryJSONL(b *testing.B, messageCount int) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), identity.MainSessionLogFilename)
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	content := strings.Repeat("recovery transcript line\n", 20)
	enc := json.NewEncoder(f)
	for i := range messageCount {
		msg := message.Message{Role: message.RoleAssistant, Content: content}
		if i%3 == 0 {
			msg.Role = message.RoleUser
		} else if i%3 == 1 {
			msg.ToolCalls = []message.ToolCall{{ID: fmt.Sprintf("call-%d", i), Name: "read", Args: json.RawMessage(fmt.Sprintf(`{"path":"file-%d.go"}`, i))}}
		} else {
			msg.Role = message.RoleTool
			msg.ToolCallID = fmt.Sprintf("call-%d", i-1)
		}
		if err := enc.Encode(msg); err != nil {
			b.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	return path
}

func BenchmarkLoadMessagesLargeSession(b *testing.B) {
	path := benchmarkRecoveryJSONL(b, 5000)
	rm := NewRecoveryManager(filepath.Dir(path))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		messages, err := rm.LoadMessages("main")
		if err != nil {
			b.Fatal(err)
		}
		if len(messages) != 5000 {
			b.Fatalf("messages = %d, want 5000", len(messages))
		}
	}
}

func BenchmarkLoadMessagesLargeSessionCountCacheHit(b *testing.B) {
	path := benchmarkRecoveryJSONL(b, 5000)
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	messageCountCache.Store(path, messageCountEntry{size: info.Size(), modTime: info.ModTime(), count: 5000})
	rm := NewRecoveryManager(filepath.Dir(path))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		messages, err := rm.LoadMessages("main")
		if err != nil {
			b.Fatal(err)
		}
		if len(messages) != 5000 {
			b.Fatalf("messages = %d, want 5000", len(messages))
		}
	}
}

func BenchmarkLoadMessagesBySize(b *testing.B) {
	for _, messageCount := range []int{1, 10, 100, 1000, 5000} {
		b.Run(fmt.Sprintf("messages_%d", messageCount), func(b *testing.B) {
			path := benchmarkRecoveryJSONL(b, messageCount)
			rm := NewRecoveryManager(filepath.Dir(path))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				messages, err := rm.LoadMessages("main")
				if err != nil {
					b.Fatal(err)
				}
				if len(messages) != messageCount {
					b.Fatalf("messages = %d, want %d", len(messages), messageCount)
				}
			}
		})
	}
}
