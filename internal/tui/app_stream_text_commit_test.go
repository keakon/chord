package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestStreamTextCommitReplacesLiveCardText(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "damaged ", TurnID: 1, RequestSeq: 1})
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "\ufffd tail", TurnID: 1, RequestSeq: 1})
	m.currentAssistantBlock.syncStreamingContent()
	m.currentAssistantBlock.Render(80, "")
	handled, _ := m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{Text: "clean terminal text", TurnID: 1, RequestSeq: 1})
	if !handled {
		t.Fatal("StreamTextCommitEvent must be handled")
	}
	block := m.currentAssistantBlock
	if block == nil {
		t.Fatal("streaming assistant block missing")
	}
	if block.Content != "clean terminal text" {
		t.Fatalf("card content = %q, want the committed terminal text", block.Content)
	}
	if block.streamContentBuilder != nil {
		t.Fatal("commit must clear the stream content builder so later deltas cannot resurrect damaged text")
	}
	rendered := strings.Join(block.Render(80, ""), "\n")
	if !strings.Contains(rendered, "clean terminal text") || strings.Contains(rendered, "damaged") {
		t.Fatalf("cached rendering did not adopt terminal text: %q", rendered)
	}
	if !block.Streaming {
		t.Fatal("commit must not settle the card; the segment end still owns settling")
	}
}

func TestStreamTextCommitEmptyTextRemovesCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "streamed", TurnID: 1, RequestSeq: 1})
	m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{TurnID: 1, RequestSeq: 1})
	if m.currentAssistantBlock != nil || len(m.viewport.blocks) != 0 {
		t.Fatal("empty confirmation must remove streamed card")
	}
	// A duplicate empty commit must not reopen a blank card.
	m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{TurnID: 1, RequestSeq: 1})
	if len(m.viewport.blocks) != 0 {
		t.Fatal("duplicate empty commit created a card")
	}
}

func TestStreamTextCommitWithoutDeltaCreatesCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	evt := agent.StreamTextCommitEvent{Text: "terminal text", TurnID: 1, RequestSeq: 1}
	m.handleAgentEvent(agentEventMsg{event: evt})
	m.handleAgentEvent(agentEventMsg{event: evt})
	m.handleAgentEvent(agentEventMsg{event: agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}})
	if len(m.viewport.blocks) != 1 || m.viewport.blocks[0].Content != evt.Text || m.viewport.blocks[0].Streaming {
		t.Fatal("terminal-only response must produce one settled card")
	}
}

func TestStreamTextCommitFindsSettledSegmentCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "damaged \ufffd", TurnID: 1, RequestSeq: 1})
	m.handleStreamingAgentEvent(agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1})
	if m.currentAssistantBlock != nil {
		t.Fatal("segment end must settle the live card")
	}
	// The producer emits the commit after the final delta but a reordered
	// delivery may land it after the segment end: it must still find and fix
	// the settled card instead of opening a second one.
	handled, _ := m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{Text: "clean text", TurnID: 1, RequestSeq: 1})
	if !handled {
		t.Fatal("late commit must be handled")
	}
	found := false
	for _, b := range m.viewport.blocks {
		if b != nil && b.Type == BlockAssistant && b.StreamTurnID == 1 && b.StreamRequestSeq == 1 {
			found = true
			if b.Content != "clean text" {
				t.Fatalf("settled card content = %q, want committed text", b.Content)
			}
		}
	}
	if !found {
		t.Fatal("late commit must locate the settled segment card")
	}
}

func TestStreamTextCommitOlderSegmentDoesNotTouchNewerCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "older segment", TurnID: 1, RequestSeq: 1})
	m.handleStreamingAgentEvent(agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1})
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "newer segment streaming", TurnID: 1, RequestSeq: 2})
	handled, _ := m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{Text: "older final", TurnID: 1, RequestSeq: 1})
	if !handled {
		t.Fatal("older-segment commit must be handled")
	}
	if m.currentAssistantBlock == nil || m.currentAssistantBlock.Content != "newer segment streaming" {
		content := "<nil>"
		if m.currentAssistantBlock != nil {
			content = m.currentAssistantBlock.Content
		}
		t.Fatalf("live card content = %q, want the newer segment untouched", content)
	}
	// The older segment's settled card still gets its replacement.
	for _, b := range m.viewport.blocks {
		if b != nil && b.Type == BlockAssistant && b.StreamRequestSeq == 1 {
			if b.Content != "older final" {
				t.Fatalf("older card content = %q, want its committed text", b.Content)
			}
		}
	}
}

func TestStreamTextCommitMissingStaleCardDoesNotReopen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Model)
	}{
		{"settled", func(m *Model) { m.handleStreamingAgentEvent(agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}) }},
		{"newer assistant", func(m *Model) {
			m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "new", TurnID: 1, RequestSeq: 2})
		}},
		{"newer thinking", func(m *Model) {
			m.handleStreamingAgentEvent(agent.StreamThinkingDeltaEvent{Text: "new", TurnID: 1, RequestSeq: 2})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			tc.setup(&m)
			before := len(m.viewport.blocks)
			m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{Text: "old", TurnID: 1, RequestSeq: 1})
			if len(m.viewport.blocks) != before {
				t.Fatal("stale commit opened a card")
			}
		})
	}
}

func TestStreamTextCommitReplacesSpilledCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	store, err := newViewportSpillStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m.viewport.spill = store
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "damaged text", TurnID: 1, RequestSeq: 1})
	b := m.currentAssistantBlock
	m.handleStreamingAgentEvent(agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1})
	if !m.viewport.spillBlock(b) {
		t.Fatal("spill failed")
	}
	m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{Text: "clean text", TurnID: 1, RequestSeq: 1})
	if err := b.ensureMaterialized(); err != nil {
		t.Fatal(err)
	}
	if b.Content != "clean text" {
		t.Fatalf("after reload: %q", b.Content)
	}
	// Re-eviction must serialize corrected content, not reuse the old snapshot.
	if !m.viewport.spillBlock(b) {
		t.Fatal("second spill failed")
	}
	if err := b.ensureMaterialized(); err != nil {
		t.Fatal(err)
	}
	if b.Content != "clean text" {
		t.Fatalf("after second reload: %q", b.Content)
	}
}

func TestStreamTextCommitUnchangedPreservesRenderCache(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "confirmed text", TurnID: 1, RequestSeq: 1})
	b := m.currentAssistantBlock
	b.syncStreamingContent()
	b.lineCache = []string{"cached line"}
	m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{Text: "confirmed text", TurnID: 1, RequestSeq: 1})
	if b.streamContentBuilder != nil || len(b.lineCache) != 1 {
		t.Fatal("unchanged confirmation invalidated rendering or retained builder")
	}
}

func TestStreamTextCommitLateEmptyRemovesSettledCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "draft", TurnID: 1, RequestSeq: 1})
	m.handleStreamingAgentEvent(agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1})
	m.handleStreamingAgentEvent(agent.StreamTextCommitEvent{TurnID: 1, RequestSeq: 1})
	if len(m.viewport.blocks) != 0 {
		t.Fatal("late empty confirmation retained the settled card")
	}
}

// Empty tool replies must not scan a growing conversation. Setup is outside
// timing; each iteration confirms a response with no assistant card.
func BenchmarkStreamTextCommitToolOnly(b *testing.B) {
	for _, size := range []int{10, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			m := NewModelWithSize(nil, 80, 24)
			for i := range size {
				m.viewport.blocks = append(m.viewport.blocks, &Block{ID: i, Type: BlockAssistant})
			}
			evt := agent.StreamTextCommitEvent{TurnID: 1, RequestSeq: 1}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				m.commitStreamText(evt)
			}
		})
	}
}
