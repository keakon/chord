package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func benchmarkToolArgumentSnapshots(name string, size int, lateLabel bool) []string {
	body, _ := json.Marshal(strings.Repeat("value ", (size+5)/6))
	label, field := `"path":"sample.txt"`, "content"
	switch name {
	case tools.NameEdit:
		field = "new_string"
		label += `,"old_string":"source"`
	case tools.NameShell:
		field = "command"
		label = `"description":"Inspect generated data"`
	}
	args := "{" + label + ",\"" + field + "\":" + string(body) + "}"
	if lateLabel {
		args = "{\"" + field + "\":" + string(body) + "," + label + "}"
	}
	snapshots := make([]string, 0, len(args)/4096+2)
	for end := 16; end < len(args); end += 4096 {
		snapshots = append(snapshots, args[:end])
	}
	return append(snapshots, args)
}

// BenchmarkToolArgumentStream exercises published argument snapshots and real
// card rendering. Model setup and snapshot construction are outside timing.
func BenchmarkToolArgumentStream(b *testing.B) {
	for _, name := range []string{tools.NameWrite, tools.NameEdit, tools.NameShell} {
		for _, size := range []int{64 << 10, 512 << 10} {
			for _, late := range []bool{false, true} {
				for _, storage := range []string{"shared", "independent"} {
					b.Run(fmt.Sprintf("%s/%dKiB/late=%t/%s", name, size>>10, late, storage), func(b *testing.B) {
						snapshots := benchmarkToolArgumentSnapshots(name, size, late)
						if storage == "independent" {
							for i, snapshot := range snapshots {
								snapshots[i] = strings.Clone(snapshot)
							}
						}
						b.ReportAllocs()
						for b.Loop() {
							b.StopTimer()
							m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
							_ = m.View()
							b.StartTimer()
							_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
								ID: "call-arguments", Name: name, ArgsJSON: snapshots[0],
							}})
							for _, snapshot := range snapshots[1:] {
								// Each snapshot represents a provider publication after the render interval.
								m.toolArgRenderState["call-arguments"] = toolArgRenderState{lastAt: time.Time{}}
								_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
									ID: "call-arguments", Name: name, ArgsJSON: snapshot,
								}})
								_ = m.View()
							}
							block, ok := m.viewport.FindBlockByToolID("call-arguments")
							if !ok || block.RawArgs != snapshots[len(snapshots)-1] {
								b.Fatal("streamed arguments were lost")
							}
						}
					})
				}
			}
		}
	}
}
