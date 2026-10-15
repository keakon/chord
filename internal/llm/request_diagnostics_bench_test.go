package llm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// Exercise the production normalization, MCP and provider wrapper path. The
// fixture is immutable and constructed outside the timer in both variants.
func BenchmarkRequestDiagnostics(b *testing.B) {
	for _, size := range []struct {
		name                   string
		messages, tools, bytes int
		attachments            bool
	}{{"small", 20, 10, 100, false}, {"ordinary", 500, 100, 2048, false}, {"limit", 4096, 256, 256, true}} {
		msgs := make([]message.Message, size.messages)
		defs := make([]message.ToolDefinition, size.tools)
		text := strings.Repeat("x", size.bytes)
		image := make([]byte, 64<<10)
		for i := range msgs {
			msgs[i] = message.Message{Role: message.RoleUser, Content: text}
			if size.attachments && i%20 == 0 {
				msgs[i].Parts = []message.ContentPart{{Type: message.ContentPartText, Text: text}, {Type: message.ContentPartImage, Data: image}}
			}
		}
		for i := range defs {
			defs[i] = message.ToolDefinition{Name: fmt.Sprintf("tool-%d", i), Description: "sample tool", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []any{"first", "second"}}}}}
		}
		for _, cold := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				name := fmt.Sprintf("%s/cold=%t/observer=%t", size.name, cold, enabled)
				b.Run(name, func(b *testing.B) {
					cfg := diagnosticProviderConfig(config.ProviderTypeChatCompletions)
					p := &diagnosticRecordingProvider{response: &message.Response{Content: "sample"}}
					client := NewClient(cfg, p, "test-model", 100, "system")
					defer client.Close()
					collector, _ := diagnosticTestCollector()
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						opts := CompleteStreamOptions{}
						if enabled {
							if cold {
								collector, _ = diagnosticTestCollector()
							}
							opts.Diagnostics = collector.BeginRequest(0, 0)
						}
						if _, err := client.CompleteStreamWithOptions(context.Background(), msgs, defs, nil, opts); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
