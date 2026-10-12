package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// BenchmarkApplyPatchArgumentStream measures published snapshots, card layout,
// visible rendering and argument completion together. Setup is outside timing;
// use fixed iteration counts because every round constructs a fresh model.
func BenchmarkApplyPatchArgumentStream(b *testing.B) {
	for _, size := range []int{64 << 10, 512 << 10} {
		patch := "*** Begin Patch\n*** Add File: sample.txt\n" + strings.Repeat("+sample content\n", size/16) + "*** End Patch"
		args, err := json.Marshal(map[string]string{"patch": patch})
		if err != nil {
			b.Fatal(err)
		}
		envelope, err := json.Marshal(map[string]string{"input": patch})
		if err != nil {
			b.Fatal(err)
		}
		for _, shape := range []string{"bare", "envelope", "function"} {
			b.Run(fmt.Sprintf("%s/%dKiB", shape, size>>10), func(b *testing.B) {
				input := patch
				switch shape {
				case "envelope":
					input = string(envelope)
				case "function":
					input = string(args)
				}
				snapshots := make([]string, 0, len(input)/4096+1)
				for end := 16; end < len(input); end += 4096 {
					snapshots = append(snapshots, strings.Clone(input[:end]))
				}
				snapshots = append(snapshots, input)
				finalArgs := string(args)
				b.ReportAllocs()
				for b.Loop() {
					b.StopTimer()
					m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
					_ = m.View()
					b.StartTimer()
					_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{ID: "call-patch", Name: tools.NameApplyPatch}})
					for _, snapshot := range snapshots {
						event := agent.ToolCallUpdateEvent{ID: "call-patch", Name: tools.NameApplyPatch}
						if shape == "function" {
							event.ArgsJSON = snapshot
						} else {
							event.InputText = snapshot
						}
						_ = m.handleAgentEvent(agentEventMsg{event: event})
						_ = m.View()
					}
					_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
						ID: "call-patch", Name: tools.NameApplyPatch, ArgsJSON: finalArgs, ArgsStreamingDone: true,
					}})
					_ = m.View()
					block, ok := m.viewport.FindBlockByToolID("call-patch")
					if !ok || block.RawArgs != finalArgs {
						b.Fatal("final patch arguments were lost")
					}
				}
			})
		}
	}
}
