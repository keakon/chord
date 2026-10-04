package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// Compare every byte prefix with the existing tolerant display parser, including
// incomplete UTF-8 and escaped keys. This reader never validates execution args.
func TestStreamingToolArgumentsPrefixes(t *testing.T) {
	samples := []string{
		`{"path":"sample.txt","content":"long \"quoted\" text","path":"last.txt"}`,
		`{"content":[{"path":"nested","value":"escaped \\\" }"}],"pa\u0074h":"sample.txt"}`,
		`{"path":"first","path":null,"description":false,"command":""}`,
		`{"timeout_ms":1e2,"timeout_ms":1e3,"description":"ready"}`,
		`{"timeout_ms":15,"timeout_ms":1e,"path":"invalid"}`,
		`{"timeout_ms":00`,
		`{"path":00000000`,
		`{"content":"bad\q","path":"invalid"}`,
		`{"content":[1,],"path":"invalid"}`,
		`{"content":{],"path":"invalid"}`,
		" \t{\"path\":\"目录/🙂.txt\",\"content\":\"数据\"}\u2003",
		`{"path":17,"description":{"a":1},"timeout_ms":9007199254740993}`,
		`{"command":"one","command":"unfinished`,
	}
	for _, sample := range samples {
		t.Run(sample, func(t *testing.T) {
			s := &streamingToolArguments{readFields: true}
			for end := 0; end <= len(sample); end++ {
				prefix := sample[:end]
				s.update(prefix)
				_, want := parseToolArgs(prefix)
				display, _ := json.Marshal(s.values)
				_, got := parseToolArgs(string(display))
				for _, key := range []string{"path", "description", "command", "workdir", "timeout_ms", "yield_time_ms", "run_in_background"} {
					if got[key] != want[key] {
						t.Fatalf("prefix %q field %s = %q, want %q", prefix, key, got[key], want[key])
					}
				}
				if gotCount := s.count(); gotCount != utf8.RuneCountInString(strings.TrimSpace(prefix)) {
					t.Fatalf("prefix %q count = %d", prefix, gotCount)
				}
			}
		})
	}
}

func TestStreamingToolArgumentsReplacements(t *testing.T) {
	s := &streamingToolArguments{readFields: true}
	for _, raw := range []string{`{"path":"first"}`, `{"path":"other"}`, `{"command":"x"}`, `{"`, "", " \u2003🙂\xff", " \u2003🙂\xff \n"} {
		s.update(raw)
		fresh := &streamingToolArguments{readFields: true}
		fresh.update(raw)
		if !reflect.DeepEqual(s.values, fresh.values) || s.count() != fresh.count() {
			t.Fatalf("replacement %q retained stale data", raw)
		}
	}
}

func TestStreamingFilePathAfterLargeContent(t *testing.T) {
	body := strings.Repeat("value ", 10000)
	raw := `{"content":"` + body + `","path":"sample.txt"}`
	b := &Block{ToolName: tools.NameWrite}
	for end := 16; end < len(raw)-1; end += 4096 {
		if got := b.streamedDisplayArgs(raw[:end], ""); got != "" {
			t.Fatalf("premature path: %s", got)
		}
	}
	if got := b.streamedDisplayArgs(raw, ""); got != `{"path":"sample.txt"}` {
		t.Fatal(got)
	}
	if len(b.streamArgs.values) != 1 {
		t.Fatal("large content retained for display")
	}
	if got := b.streamedDisplayArgs(`{"content":"replacement`, ""); got != "" {
		t.Fatal("stale path after replacement")
	}
}

func TestStreamingArgumentsTerminalEvents(t *testing.T) {
	for _, terminal := range []string{"done", "queued", "running", "result", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
			const id = "stream-call"
			raw := `{"description":"Inspect data","command":"echo data"}`
			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{ID: id, Name: tools.NameShell, ArgsJSON: raw}})
			block, _ := m.viewport.FindBlockByToolID(id)
			if block.streamArgs == nil {
				t.Fatal("missing reader")
			}
			var event agent.AgentEvent
			switch terminal {
			case "done":
				event = agent.ToolCallUpdateEvent{ID: id, Name: tools.NameShell, ArgsJSON: raw, ArgsStreamingDone: true}
			case "queued":
				event = agent.ToolCallExecutionEvent{ID: id, Name: tools.NameShell, State: agent.ToolCallExecutionStateQueued}
			case "running":
				event = agent.ToolCallExecutionEvent{ID: id, Name: tools.NameShell, State: agent.ToolCallExecutionStateRunning}
			case "result":
				event = agent.ToolResultEvent{CallID: id, Name: tools.NameShell, Status: agent.ToolResultStatusSuccess, Result: "ok"}
			case "cancelled":
				event = agent.ToolResultEvent{CallID: id, Name: tools.NameShell, Status: agent.ToolResultStatusCancelled}
			}
			_ = m.handleAgentEvent(agentEventMsg{event: event})
			if block.streamArgs != nil || block.ToolProgress != nil {
				t.Fatal("stream state retained at terminal event")
			}
			if block.RawArgs != raw {
				t.Fatal("original arguments changed")
			}
			replacement := `{"command":"echo replacement"}`
			m.toolArgRenderState[id] = toolArgRenderState{lastAt: time.Time{}}
			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{ID: id, Name: tools.NameShell, ArgsJSON: replacement, ArgsStreamingDone: true}})
			if block.streamArgs != nil || block.ToolProgress != nil || block.Content != replacement {
				t.Fatal("final replacement was not authoritative")
			}
		})
	}
}

func TestLargeToolArgumentStreamHandlesInput(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
	snapshots := benchmarkToolArgumentSnapshots(tools.NameWrite, 512<<10, false)
	m.viewport.AppendBlock(&Block{ID: 900, Type: BlockAssistant, Content: strings.Repeat("line\n\n", 100)})
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.View()
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{ID: "input-stream", Name: tools.NameWrite, ArgsJSON: snapshots[0]}})
	for _, raw := range snapshots[1:] {
		m.toolArgRenderState["input-stream"] = toolArgRenderState{lastAt: time.Time{}}
		_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{ID: "input-stream", Name: tools.NameWrite, ArgsJSON: raw}})
		m.viewport.ScrollToBottom()
		before := m.viewport.offset
		_, _ = m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
		_, _ = m.Update(scrollFlushTickMsg{generation: m.scrollFlushGeneration})
		if m.viewport.offset >= before {
			t.Fatal("scroll was not processed during argument streaming")
		}
		_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
		_ = m.View()
	}
	if got := m.input.Value(); got != strings.Repeat("x", len(snapshots)-1) {
		t.Fatalf("input lost during streaming: %q", got)
	}
}

func FuzzStreamingToolArguments(f *testing.F) {
	for _, sample := range []string{
		`{"path":"sample.txt","content":[1,{"path":"nested"}],"path":"last.txt"}`,
		`{"timeout_ms":1e2,"timeout_ms":1e3,"command":"echo \\"quoted\\""}`,
		`{"pa\u0074h":"目录/🙂.txt","description":"ready"}`,
		`{"path":"first","path":false,"command":null}`,
	} {
		f.Add(sample, uint8(1))
	}
	f.Fuzz(func(t *testing.T, raw string, chunk uint8) {
		if len(raw) > 4096 {
			t.Skip()
		}
		s := &streamingToolArguments{readFields: true}
		step := int(chunk)%64 + 1
		for end := min(step, len(raw)); ; end = min(end+step, len(raw)) {
			s.update(raw[:end])
			_, want := parseToolArgs(raw[:end])
			display, _ := json.Marshal(s.values)
			_, got := parseToolArgs(string(display))
			for _, key := range []string{"path", "description", "command", "workdir", "timeout_ms", "yield_time_ms", "run_in_background"} {
				if got[key] != want[key] {
					t.Fatalf("prefix %q field %s = %q, want %q", raw[:end], key, got[key], want[key])
				}
			}
			if s.count() != utf8.RuneCountInString(strings.TrimSpace(raw[:end])) {
				t.Fatal("character count differs")
			}
			if end == len(raw) {
				break
			}
		}
	})
}

func TestStreamingEditPreviewWaitsForCompleteArguments(t *testing.T) {
	const raw = `{"path":"sample.txt","old_string":"source","new_string":"replacement"}`
	b := &Block{Type: BlockToolCall, ToolName: tools.NameEdit, ToolExecutionState: agent.ToolCallExecutionStateReceiving}
	b.RawArgs = raw[:len(raw)-2]
	b.Content = b.streamedDisplayArgs(b.RawArgs, "")
	if !b.editArgsIncomplete() {
		t.Fatal("incomplete edit considered readable")
	}
	if got := stripANSI(strings.Join(b.Render(120, ""), "\n")); strings.Contains(got, "replacement") {
		t.Fatal("unfinished edit preview rendered")
	}
	b.RawArgs = raw
	b.Content = b.streamedDisplayArgs(raw, "")
	b.ResultDone = true
	b.InvalidateCache()
	if b.editArgsIncomplete() {
		t.Fatal("complete edit preview suppressed")
	}
	if got := stripANSI(strings.Join(b.Render(120, ""), "\n")); !strings.Contains(got, "replacement") {
		t.Fatalf("complete edit preview missing: %s", got)
	}
}

func TestStreamingArgumentMetadataUpdateKeepsPayload(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	const raw = `{"path":"sample.txt","content":"unfinished`
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{ID: "metadata-call", Name: tools.NameWrite, ArgsJSON: raw}})
	block, _ := m.viewport.FindBlockByToolID("metadata-call")
	expectedContent, expectedProgress := block.Content, *block.ToolProgress
	m.toolArgRenderState["metadata-call"] = toolArgRenderState{lastAt: time.Time{}}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{ID: "metadata-call", Name: tools.NameWrite}})
	if block.RawArgs != raw || block.Content != expectedContent || block.ToolProgress == nil || *block.ToolProgress != expectedProgress {
		t.Fatal("metadata update discarded argument display")
	}
	m.toolArgRenderState["metadata-call"] = toolArgRenderState{lastAt: time.Time{}}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{ID: "metadata-call", Name: tools.NameWrite, ArgsJSON: " \n"}})
	if block.Content != "" || block.ToolProgress != nil || block.RawArgs != " \n" {
		t.Fatal("blank replacement retained stale display")
	}
}
