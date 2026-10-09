package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func BenchmarkToolDiscoveryProjection(b *testing.B) {
	for _, count := range []int{64, 256} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			visible := []tools.Tool{tools.ShellTool{}}
			var history []message.Message
			for i := range count {
				tool := deferredTestTool{name: fmt.Sprintf("mcp_sample_%d", i), description: strings.Repeat("Sample records ", 100)}
				visible = append(visible, tool)
				def := llmToolDefinitionsFromVisibleTools([]tools.Tool{tool})[0]
				history = append(history, discoveryHistory(b, fmt.Sprint(i), message.ToolDiscoveryResult{Tools: []message.ToolDiscoveryEntry{{Name: tool.Name(), Status: message.ToolDiscoveryLoaded, Definition: &def}}})...)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if len(projectDiscoveredTools(visible, message.ToolDiscoveryHistory(history))) != count+1 {
					b.Fatal("loaded definitions were lost")
				}
			}
		})
	}
}

// Exercise the request surface with retained attachments and discovery results,
// rather than measuring the name projection in isolation.
func BenchmarkToolDiscoveryRequestSurface(b *testing.B) {
	for _, count := range []int{64, 256} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(1000000, 0.8), ruleset: permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}}
			a.tools.Register(tools.ShellTool{})
			for i := range count {
				tool := deferredTestTool{name: fmt.Sprintf("mcp_sample_%d", i), description: strings.Repeat("Sample records ", 100)}
				a.tools.Register(tool)
				def := llmToolDefinitionsFromVisibleTools([]tools.Tool{tool})[0]
				for _, msg := range discoveryHistory(b, fmt.Sprint(i), message.ToolDiscoveryResult{Tools: []message.ToolDiscoveryEntry{{Name: tool.Name(), Status: message.ToolDiscoveryLoaded, Definition: &def}}}) {
					a.ctxMgr.Append(msg)
				}
			}
			for range 16 {
				a.ctxMgr.Append(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: make([]byte, 256<<10), MimeType: "image/png"}}})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if len(a.mainVisibleLLMTools()) != count+1 {
					b.Fatal("loaded definitions were lost")
				}
			}
		})
	}
}
