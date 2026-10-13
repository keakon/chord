package tui

import (
	"context"
	"os"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

// The listener is outside the timed workload. Each state renders the same
// transcript and receives the same deltas from an already started response.
func BenchmarkTakeoverStreamingLongTranscript(b *testing.B) {
	for _, state := range []string{"normal", "listener_idle", "draining"} {
		b.Run(state, func(b *testing.B) {
			if state != "normal" {
				if !takeover.Supported() {
					b.Skip("local takeover requires macOS or Linux")
				}
				dir, err := os.MkdirTemp("/tmp", "ctb-")
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { os.RemoveAll(dir) })
				workspace, err := takeover.CanonicalWorkspace(dir)
				if err != nil {
					b.Fatal(err)
				}
				s, err := takeover.StartServer(takeover.ServerOptions{
					StateDir: dir,
					Record:   takeover.Record{Protocol: takeover.ProtocolVersion, RuntimeID: "sample-runtime", SessionID: "sample-session", Workspace: workspace, OwnerKind: takeover.OwnerTUI},
					Start: func(context.Context, recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
						return recovery.SessionPauseStatus{}, nil
					},
					Status:    func(string) (recovery.SessionPauseStatus, error) { return recovery.SessionPauseStatus{}, nil },
					RetrySave: func(context.Context, string) error { return nil },
				})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() {
					if err := s.Close(); err != nil {
						b.Error(err)
					}
				})
			}
			deltas := repeatedStreamDeltas(128, "alpha beta gamma ")
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				m := benchmarkModelForView()
				m.viewport = benchmarkLargeViewport(5000)
				m.viewport.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
				if state == "draining" {
					m.takeover.status = recovery.SessionPauseStatus{State: recovery.SessionPauseDraining}
					m.takeover.sourceFrozen = true
				}
				_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "seed"}})
				m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
				_ = m.View()
				b.StartTimer()
				for i, delta := range deltas {
					_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: delta}})
					if (i+1)%32 == 0 {
						m.handleStreamFlushTick(streamFlushTickMsg{generation: m.streamFlushGeneration})
					}
					_ = m.View()
				}
			}
		})
	}
}
