package recovery

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func BenchmarkMessageLogReader(b *testing.B) {
	for _, size := range []struct {
		name string
		n    int
	}{{"4KiB", 4 << 10}, {"1MiB", 1 << 20}} {
		raw, err := json.Marshal(message.Message{
			Role: message.RoleAssistant, Content: strings.Repeat("x", size.n),
			ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample", Args: json.RawMessage(`{"value":1e1000}`)}},
		})
		if err != nil {
			b.Fatal(err)
		}
		raw = append(raw, '\n')
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				reader := newMessageLogReader(bytes.NewReader(raw))
				var msg message.Message
				if err := reader.next(&msg); err != nil {
					b.Fatal(err)
				}
				if err := reader.next(&msg); err != io.EOF {
					b.Fatalf("end of log: %v", err)
				}
			}
		})
	}
}
