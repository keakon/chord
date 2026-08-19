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
		switch i % 3 {
		case 0:
			msg.Role = message.RoleUser
		case 1:
			msg.ToolCalls = []message.ToolCall{{ID: fmt.Sprintf("call-%d", i), Name: "read", Args: json.RawMessage(fmt.Sprintf(`{"path":"file-%d.go"}`, i))}}
		default:
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

func BenchmarkFindMostRecentSessionCachedActivity(b *testing.B) {
	sessionsDir := b.TempDir()
	const sessionCount = 200
	largeAggregate := strings.Repeat(`"provider/model":{"llm_calls":1},`, 500)
	for i := range sessionCount {
		sessionDir := filepath.Join(sessionsDir, fmt.Sprintf("202608201200%05d", i))
		if err := os.Mkdir(sessionDir, 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, identity.MainSessionLogFilename), []byte("{}\n"), 0o600); err != nil {
			b.Fatal(err)
		}
		summary := fmt.Sprintf(`{"last_updated_at":"2026-08-20T12:00:%02dZ","by_model_ref":{%s"last/model":{"llm_calls":1}}}`, i%60, largeAggregate)
		if err := os.WriteFile(filepath.Join(sessionDir, "usage-summary.json"), []byte(summary), 0o600); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if got := FindMostRecentSession(sessionsDir, ""); got == "" {
			b.Fatal("FindMostRecentSession returned empty path")
		}
	}
}
