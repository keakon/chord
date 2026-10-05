package agent

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// BenchmarkContextSnapshotAndStablePrepare includes the snapshot ownership
// cost in the same request preparation path as stable-prefix reduction.
func BenchmarkContextSnapshotAndStablePrepare(b *testing.B) {
	for _, fixture := range []struct{ toolResults, images int }{
		{100, 0}, {1000, 0}, {100, 8}, {1000, 32},
	} {
		b.Run(fmt.Sprintf("tool_results_%d/images_%d", fixture.toolResults, fixture.images), func(b *testing.B) {
			a := benchmarkContextReductionAgent()
			a.ctxMgr = ctxmgr.NewManager(1000000, 0)
			messages := benchmarkContextReductionMessages(fixture.toolResults)
			for i := range fixture.images {
				user := &messages[i*3]
				user.Parts = []message.ContentPart{
					{Type: message.ContentPartText, Text: user.Content},
					{Type: message.ContentPartImage, MimeType: "image/png", Data: make([]byte, 1<<20)},
				}
			}
			a.ctxMgr.RestoreMessages(messages)
			a.prepareMessagesForLLM(a.ctxMgr.Snapshot())
			a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "small follow-up"})
			a.prepareMessagesForLLM(a.ctxMgr.Snapshot())
			if !a.GetContextReductionStats().ReusedStable {
				b.Fatal("fixture did not engage stable-prefix reuse")
			}
			want := a.ctxMgr.MessageCount()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				prepared := a.prepareMessagesForLLM(a.ctxMgr.Snapshot())
				if len(prepared) != want {
					b.Fatalf("prepared %d messages, want %d", len(prepared), want)
				}
			}
		})
	}
}
