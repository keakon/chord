package recovery

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// BenchmarkInputDeliveryMetadataWithAttachments measures durable queue control
// with existing session-owned attachments, including validation and file sync.
func BenchmarkInputDeliveryMetadataWithAttachments(b *testing.B) {
	for _, count := range []int{1, 16} {
		b.Run(fmt.Sprintf("attachments_%d", count), func(b *testing.B) {
			dir := b.TempDir()
			manager := NewRecoveryManager(dir)
			b.Cleanup(func() { _ = manager.Close() })
			state := &InputDeliveryState{SessionID: filepath.Base(dir), Revision: 1, NextSequence: int64(count)}
			for i := range count {
				state.Inputs = append(state.Inputs, PendingInput{InputID: fmt.Sprintf("input-%d", i), OwnerTaskID: identity.MainAgentID, Sequence: int64(i + 1), Revision: 1, Stage: PendingInputStageQueued, DeliveryPolicy: InputDeliveryAfterCompletion, Content: "sample task", Parts: []message.ContentPart{{Type: message.ContentPartImage, MimeType: "image/png", Data: make([]byte, 4096)}}})
			}
			var err error
			state, err = manager.SaveInputDelivery(0, state)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				next := state.Clone()
				next.Revision++
				state, err = manager.SaveInputDelivery(state.Revision, next)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
