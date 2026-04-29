package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/runtimecache"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func modelSelectKey(text string) tea.KeyPressMsg {
	if text == "" {
		return tea.KeyPressMsg(tea.Key{})
	}
	runes := []rune(text)
	return tea.KeyPressMsg(tea.Key{Text: text, Code: runes[0]})
}

func ctrlKeyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: r, Mod: tea.ModCtrl})
}

func TestIdleEventFinalizesStreamingAssistantWithoutDuplicateCommittedBlock(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "hello world"}})
	applyTestCmd(t, &m, cmd)
	if got := len(m.viewport.visibleBlocks()); got != 1 {
		t.Fatalf("streaming block count = %d, want 1", got)
	}

	backend.messages = []message.Message{{Role: "assistant", Content: "hello world"}}
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	applyTestCmd(t, &m, cmd)
	m.rebuildViewportFromMessagesWithReason("idle_committed_assistant")

	blocks := m.viewport.visibleBlocks()
	if got := len(blocks); got != 1 {
		t.Fatalf("block count after idle+rebuild = %d, want 1", got)
	}
	if blocks[0].Type != BlockAssistant {
		t.Fatalf("block type = %v, want assistant", blocks[0].Type)
	}
	if plain := strings.TrimSpace(stripANSI(blocks[0].Content)); !strings.Contains(plain, "hello world") {
		t.Fatalf("assistant content = %q, want committed text", plain)
	}
}

func TestTranscriptRebuildReplacesStreamingAssistantInsteadOfDuplicatingIt(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "partial reply"}})
	applyTestCmd(t, &m, cmd)
	if got := len(m.viewport.visibleBlocks()); got != 1 {
		t.Fatalf("streaming block count = %d, want 1", got)
	}

	backend.messages = []message.Message{{Role: "assistant", Content: "partial reply"}}
	m.rebuildViewportFromMessagesWithReason("committed_assistant_after_stream")

	blocks := m.viewport.visibleBlocks()
	if got := len(blocks); got != 1 {
		t.Fatalf("block count after transcript rebuild = %d, want 1", got)
	}
	if blocks[0].Type != BlockAssistant {
		t.Fatalf("block type = %v, want assistant", blocks[0].Type)
	}
	if blocks[0].Streaming {
		t.Fatal("committed assistant block should not remain streaming after rebuild")
	}
	if plain := strings.TrimSpace(stripANSI(blocks[0].Content)); !strings.Contains(plain, "partial reply") {
		t.Fatalf("assistant content = %q, want committed text", plain)
	}
}

func TestStreamRollbackRemovesStreamingAssistantBeforeCommittedRebuild(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "temporary reply"}})
	applyTestCmd(t, &m, cmd)
	if got := len(m.viewport.visibleBlocks()); got != 1 {
		t.Fatalf("streaming block count = %d, want 1", got)
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{Reason: "retry", AgentID: ""}})
	applyTestCmd(t, &m, cmd)
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("block count after rollback = %d, want 0", got)
	}

	backend.messages = []message.Message{{Role: "assistant", Content: "final reply"}}
	m.rebuildViewportFromMessagesWithReason("rollback_then_rebuild")
	blocks := m.viewport.visibleBlocks()
	if got := len(blocks); got != 1 {
		t.Fatalf("block count after rollback+rebuild = %d, want 1", got)
	}
	if plain := strings.TrimSpace(stripANSI(blocks[0].Content)); !strings.Contains(plain, "final reply") {
		t.Fatalf("assistant content = %q, want final committed text", plain)
	}
}

func TestSessionRestoreRebuildDoesNotDuplicateExistingStreamingAssistant(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "restored reply"}})
	applyTestCmd(t, &m, cmd)
	if got := len(m.viewport.visibleBlocks()); got != 1 {
		t.Fatalf("streaming block count = %d, want 1", got)
	}

	backend.messages = []message.Message{{Role: "assistant", Content: "restored reply"}}
	m.Update(sessionRestoredRebuildMsg{reason: "session_restored"})
	blocks := m.viewport.visibleBlocks()
	if got := len(blocks); got != 1 {
		t.Fatalf("block count after session restore rebuild = %d, want 1", got)
	}
	if blocks[0].Streaming {
		t.Fatal("restored committed assistant block should not remain streaming")
	}
	if plain := strings.TrimSpace(stripANSI(blocks[0].Content)); !strings.Contains(plain, "restored reply") {
		t.Fatalf("assistant content = %q, want restored committed text", plain)
	}
}

func TestVeryShortAssistantPrefixBeforeToolCallIsDroppedAcrossFinalizeAndRebuild(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	// Build a completed tool card first; this is the context where a very-short
	// assistant prefix ("Okay"/"Sure"/"Let") should be treated as an orphan.
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-1",
		Name:     "Read",
		ArgsJSON: `{"file_path":"a.go"}`,
	}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-1",
		Name:     "Read",
		ArgsJSON: `{"file_path":"a.go"}`,
		Result:   "ok",
		Status:   agent.ToolResultStatusSuccess,
	}})
	applyTestCmd(t, &m, cmd)

	// Stream a very-short assistant prefix, then start the next tool call.
	// ToolCallStartEvent finalizes the assistant block; the orphan prefix must
	// be dropped instead of persisting as a standalone assistant card.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "Okay"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-2",
		Name:     "Read",
		ArgsJSON: `{"file_path":"b.go"}`,
	}})
	applyTestCmd(t, &m, cmd)

	for _, b := range m.viewport.visibleBlocks() {
		if b.Type != BlockAssistant {
			continue
		}
		if strings.Contains(stripANSI(b.Content), "Okay") {
			t.Fatalf("unexpected orphan short assistant prefix kept before rebuild: %q", stripANSI(b.Content))
		}
	}

	backend.messages = []message.Message{{Role: "assistant", Content: "final response"}}
	m.rebuildViewportFromMessagesWithReason("tool_short_prefix_finalize_rebuild")

	blocks := m.viewport.visibleBlocks()
	if got := len(blocks); got != 1 {
		t.Fatalf("block count after rebuild = %d, want 1 committed assistant block", got)
	}
	if blocks[0].Type != BlockAssistant {
		t.Fatalf("block type after rebuild = %v, want assistant", blocks[0].Type)
	}
	plain := strings.TrimSpace(stripANSI(blocks[0].Content))
	if !strings.Contains(plain, "final response") {
		t.Fatalf("assistant content after rebuild = %q, want committed final response", plain)
	}
	if strings.Contains(plain, "Okay") {
		t.Fatalf("assistant content after rebuild should not retain orphan short prefix, got %q", plain)
	}
}

func TestSessionRestoredRebuildSchedulesStartupDeferredTranscriptPreheat(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+32)
	for i := 0; i < startupTranscriptWindowMinBlocks+32; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	next := applyTestCmd(t, &m, cmd)
	if next == nil {
		t.Fatal("session restore rebuild should schedule follow-up commands")
	}
	msg := next()
	if _, ok := msg.(startupDeferredPreheatTickMsg); !ok {
		t.Fatalf("follow-up msg = %T, want startupDeferredPreheatTickMsg", msg)
	}
}

func TestStartupDeferredTranscriptPreheatPopulatesHaloMetadata(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 32))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	next := applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for preheat test")
	}
	leftIdx := state.windowStart - startupDeferredTranscriptPreheatHalo
	if leftIdx < 0 {
		leftIdx = 0
	}
	rightIdx := state.windowEnd + min(startupDeferredTranscriptPreheatHalo, len(state.allBlocks)-state.windowEnd) - 1
	if rightIdx < state.windowEnd {
		rightIdx = -1
	}
	if leftIdx >= state.windowStart {
		t.Fatalf("left halo index %d should be before windowStart %d", leftIdx, state.windowStart)
	}
	if rightIdx >= 0 && rightIdx < state.windowEnd {
		t.Fatalf("right halo index %d should be at/after windowEnd %d", rightIdx, state.windowEnd)
	}
	state.blockMeta[leftIdx].SearchableText = ""
	state.blockMeta[leftIdx].LineCounts = nil
	if rightIdx >= 0 {
		state.blockMeta[rightIdx].SearchableText = ""
		state.blockMeta[rightIdx].LineCounts = nil
	}

	preheatMsg := next().(startupDeferredPreheatTickMsg)
	cmd = m.handleStartupDeferredTranscriptPreheat(preheatMsg)
	if cmd == nil {
		t.Fatal("preheat tick should reschedule while deferred transcript remains active")
	}
	if state.blockMeta[leftIdx].SearchableText == "" || startupDeferredBlockLineCount(state.blockMeta[leftIdx], m.viewport.width) <= 0 {
		t.Fatalf("left halo metadata not preheated: %#v", state.blockMeta[leftIdx])
	}
	if rightIdx >= 0 {
		if state.blockMeta[rightIdx].SearchableText == "" || startupDeferredBlockLineCount(state.blockMeta[rightIdx], m.viewport.width) <= 0 {
			t.Fatalf("right halo metadata not preheated: %#v", state.blockMeta[rightIdx])
		}
	}
}

func TestDeferredWindowSwitchRestartsPreheatForNewHalo(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 24))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	next := applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for preheat restart test")
	}
	initialGen := m.startupDeferredPreheatGeneration

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred after gg")
	}
	if m.startupDeferredPreheatGeneration <= initialGen {
		t.Fatalf("startupDeferredPreheatGeneration = %d, want > %d after window switch", m.startupDeferredPreheatGeneration, initialGen)
	}
	leftIdx := state.windowStart - startupDeferredTranscriptPreheatHalo
	if leftIdx < 0 {
		leftIdx = 0
	}
	rightIdx := state.windowEnd + min(startupDeferredTranscriptPreheatHalo, len(state.allBlocks)-state.windowEnd) - 1
	if rightIdx < state.windowEnd {
		rightIdx = -1
	}
	if rightIdx < 0 {
		t.Fatal("top window should have a right halo to preheat")
	}
	state.blockMeta[rightIdx].SearchableText = ""
	state.blockMeta[rightIdx].LineCounts = nil
	if leftIdx < state.windowStart {
		state.blockMeta[leftIdx].SearchableText = ""
		state.blockMeta[leftIdx].LineCounts = nil
	}

	_ = next // original scheduled tick should now be stale
	cmd = m.handleStartupDeferredTranscriptPreheat(startupDeferredPreheatTickMsg{generation: m.startupDeferredPreheatGeneration})
	if cmd == nil {
		t.Fatal("restarted preheat tick should reschedule while deferred transcript remains active")
	}
	if state.blockMeta[rightIdx].SearchableText == "" || startupDeferredBlockLineCount(state.blockMeta[rightIdx], m.viewport.width) <= 0 {
		t.Fatalf("right halo metadata not preheated after window switch: %#v", state.blockMeta[rightIdx])
	}
}

func TestDeferredStartupTranscriptSearchRevealExpandsToolCallContent(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := 0; i < startupTranscriptWindowMinBlocks+110; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "Read",
			Args: []byte(`{"path":"internal/tui/app.go","limit":20,"offset":0}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-1", Content: "1\talpha line\n2\tbeta line\n3\tgamma line\n4\tdelta line\n5\tepsilon line\n6\tzeta line\n7\teta line\n8\ttheta line\n9\tiota line\n10\tkappa line\n11\tneedle line\n12\tomega line"},
	)
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for search reveal test")
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("needle line")
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("search should find tool-call content")
	}
	if !m.maybeScrollToSearchMatch(match, "search_enter") {
		t.Fatal("maybeScrollToSearchMatch should succeed for tool-call match")
	}
	var toolBlock *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.ID == match.BlockID {
			toolBlock = m.viewport.GetFocusedBlock(b.ID)
			break
		}
	}
	if toolBlock == nil {
		t.Fatal("matched tool block should exist in visible window")
	}
	if toolBlock.ToolName != "Read" {
		t.Fatalf("matched tool = %q, want Read", toolBlock.ToolName)
	}
	if toolBlock.Collapsed {
		t.Fatal("search should reveal collapsed Read tool content")
	}
	if !toolBlock.ReadContentExpanded {
		t.Fatal("search should fully expand Read content")
	}
	plain := stripANSI(strings.Join(toolBlock.Render(m.viewport.width, ""), "\n"))
	if !strings.Contains(plain, "needle line") {
		t.Fatalf("rendered search match = %q, want visible needle line", plain)
	}
	viewportPlain := stripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex()))
	if !strings.Contains(viewportPlain, "needle line") {
		t.Fatalf("viewport render should include revealed search content, got %q", viewportPlain)
	}
}

func TestApplyStartupDeferredTranscriptWindowPreservesColdBlocksForLaterMaterialization(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := 0; i < startupTranscriptWindowMinBlocks+110; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-bash-window-1",
			Name: "Bash",
			Args: []byte(`{"command":"go test ./internal/tui -count=1","timeout":120}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-bash-window-1", Content: "ok chord/internal/tui 0.711s\nneedle output\nPASS"},
	)
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for cold window preservation test")
	}
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing")
	}
	var targetIndex int = -1
	for i, block := range state.allBlocks {
		if block != nil && block.ToolID == "call-bash-window-1" {
			targetIndex = i
			break
		}
	}
	if targetIndex < 0 {
		t.Fatal("expected deferred bash block in state")
	}
	target := state.allBlocks[targetIndex]
	if !m.viewport.spillBlock(target) {
		t.Fatal("expected deferred target block to spill")
	}
	if !target.spillCold {
		t.Fatal("expected deferred target block to remain cold after spill")
	}
	start, end := startupDeferredWindowRange(len(state.allBlocks), targetIndex)
	if m.applyStartupDeferredTranscriptWindow(start, end, "test_window") {
		// switched windows
	} else if state.windowStart != start || state.windowEnd != end {
		t.Fatal("expected target window to be active after applyStartupDeferredTranscriptWindow")
	}
	var visible *Block
	for _, block := range m.viewport.blocks {
		if block != nil && block.ID == target.ID {
			visible = block
			break
		}
	}
	if visible == nil {
		t.Fatal("expected target block in visible window")
	}
	if !visible.spillCold {
		t.Fatal("window apply should preserve cold state so render can materialize full content")
	}
}

func TestDeferredStartupTranscriptPageUpSwitchesWindowBeforeExactTopOffset(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+140)
	for i := 0; i < startupTranscriptWindowMinBlocks+140; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for page-up threshold test")
	}
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing")
	}
	startBefore, endBefore := state.windowStart, state.windowEnd
	m.viewport.offset = startupDeferredPageUpSwitchThreshold(m.viewport.height)
	cmd = m.handleNormalKey(ctrlKeyPress('b'))
	applyTestCmd(t, &m, cmd)
	if state.windowStart >= startBefore || state.windowEnd >= endBefore {
		t.Fatalf("page-up should switch to previous window before exact top; got window=[%d,%d), want start<%d", state.windowStart, state.windowEnd, startBefore)
	}
}

func TestDeferredStartupTranscriptScrollUpSwitchesWindowBeforeExactTopOffset(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+140)
	for i := 0; i < startupTranscriptWindowMinBlocks+140; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for scroll-up threshold test")
	}
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing")
	}
	startBefore, endBefore := state.windowStart, state.windowEnd
	m.viewport.offset = startupDeferredPageUpSwitchThreshold(m.viewport.height)
	cmd = m.handleNormalKey(modelSelectKey("k"))
	applyTestCmd(t, &m, cmd)
	if state.windowStart >= startBefore || state.windowEnd >= endBefore {
		t.Fatalf("scroll-up should switch to previous window before exact top; got window=[%d,%d), want start<%d", state.windowStart, state.windowEnd, startBefore)
	}
}

func TestDeferredStartupTranscriptMouseWheelUpSwitchesWindowBeforeExactTopOffset(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+140)
	for i := 0; i < startupTranscriptWindowMinBlocks+140; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for mouse-wheel threshold test")
	}
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing")
	}
	startBefore, endBefore := state.windowStart, state.windowEnd
	m.viewport.offset = startupDeferredPageUpSwitchThreshold(m.viewport.height)

	updated, cmd := m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("mouse wheel should schedule a scroll flush command")
	}
	model.consumeScrollFlush(scrollFlushTickMsg{generation: model.scrollFlushGeneration})

	state = model.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript should remain active after mouse-wheel switch")
	}
	if state.windowStart >= startBefore || state.windowEnd >= endBefore {
		t.Fatalf("mouse-wheel up should switch to previous window before exact top; got window=[%d,%d), want start<%d", state.windowStart, state.windowEnd, startBefore)
	}
}

func TestDeferredStartupTranscriptSearchRevealMaterializesColdCompactToolOutput(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := 0; i < startupTranscriptWindowMinBlocks+110; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-bash-cold-1",
			Name: "Bash",
			Args: []byte(`{"command":"go test ./internal/tui -count=1","timeout":120}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-bash-cold-1", Content: "ok chord/internal/tui 0.711s\nneedle output\nPASS"},
	)
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for cold compact tool search reveal test")
	}
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing")
	}
	var target *Block
	for _, block := range state.allBlocks {
		if block != nil && block.ToolID == "call-bash-cold-1" {
			target = block
			break
		}
	}
	if target == nil {
		t.Fatal("expected cold compact tool block in startup deferred transcript state")
	}
	if !m.viewport.spillBlock(target) {
		t.Fatal("expected test target block to spill cold")
	}
	if !target.spillCold {
		t.Fatal("expected target block to be cold before search reveal")
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("needle output")
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("search should find cold compact tool output")
	}
	if !m.maybeScrollToSearchMatch(match, "search_enter") {
		t.Fatal("maybeScrollToSearchMatch should succeed for cold compact tool match")
	}
	block := m.viewport.GetFocusedBlock(match.BlockID)
	if block == nil {
		t.Fatal("expected focused block after search reveal")
	}
	if block.spillCold {
		t.Fatal("search reveal should materialize cold matched block")
	}
	viewportPlain := stripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex()))
	if !strings.Contains(viewportPlain, "needle output") {
		t.Fatalf("viewport render should include materialized cold compact tool output, got %q", viewportPlain)
	}
}

func TestDeferredStartupTranscriptSearchSkipsInvisibleThinkingBlocks(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110)
	for i := 0; i < startupTranscriptWindowMinBlocks+110; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages, message.Message{
		Role:           "assistant",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: ""}},
		ToolCalls: []message.ToolCall{{
			ID:   "call-edit-1",
			Name: "Edit",
			Args: []byte(`{"path":"internal/tui/search_test.go","old_string":"before","new_string":"needle output"}`),
		}},
	})
	messages = append(messages, message.Message{Role: "tool", ToolCallID: "call-edit-1", Content: "success"})
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for invisible thinking search test")
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("needle output")
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("search should find edit tool content")
	}
	if !m.maybeScrollToSearchMatch(match, "search_enter") {
		t.Fatal("maybeScrollToSearchMatch should succeed after skipping invisible thinking block")
	}
	if block := m.viewport.GetFocusedBlock(match.BlockID); block == nil || block.Type != BlockToolCall {
		t.Fatalf("focused search match block = %#v, want tool-call", block)
	}
}

func TestDeferredStartupTranscriptSearchSkipsDiagnosticArtifactBlocks(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+4)
	for i := 0; i < startupTranscriptWindowMinBlocks+110; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-dump-artifact-read-1",
			Name: "Read",
			Args: []byte(`{"path":"~/.local/state/chord/logs/tui-dumps/tui-dump-20260403.log","limit":160,"offset":0}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-dump-artifact-read-1", Content: "1\tfind artifact line\n2\tdebug dump line"},
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-real-search-1",
			Name: "Bash",
			Args: []byte(`{"command":"echo real find result","description":"search real transcript","timeout":30}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-real-search-1", Content: "real find result"},
	)
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for diagnostic artifact search test")
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("find")
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("search should find non-diagnostic match")
	}
	if !m.maybeScrollToSearchMatch(match, "search_enter") {
		t.Fatal("maybeScrollToSearchMatch should succeed for non-diagnostic match")
	}
	block := m.viewport.GetFocusedBlock(match.BlockID)
	if block == nil {
		t.Fatal("expected focused block after search")
	}
	if block.ToolID != "call-real-search-1" {
		t.Fatalf("focused block tool id = %q, want %q", block.ToolID, "call-real-search-1")
	}
	viewportPlain := stripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex()))
	if !strings.Contains(viewportPlain, "real find result") {
		t.Fatalf("viewport render should include non-diagnostic search content, got %q", viewportPlain)
	}
	if strings.Contains(viewportPlain, "find artifact line") && !strings.Contains(viewportPlain, "real find result") {
		t.Fatalf("viewport render should focus the non-diagnostic match, got %q", viewportPlain)
	}
}

func TestDeferredStartupTranscriptSearchRevealExpandsCompactToolOutput(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := 0; i < startupTranscriptWindowMinBlocks+110; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-bash-1",
			Name: "Bash",
			Args: []byte(`{"command":"python3 - <<'PY'\nprint('alpha')\nPY","description":"Run scripted search","timeout":120}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-bash-1", Content: "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\niota\nkappa\nneedle output\nomega"},
	)
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for compact tool search reveal test")
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("needle output")
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("search should find compact tool output")
	}
	if !m.maybeScrollToSearchMatch(match, "search_enter") {
		t.Fatal("maybeScrollToSearchMatch should succeed for compact tool match")
	}
	viewportPlain := stripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex()))
	if !strings.Contains(viewportPlain, "needle output") {
		t.Fatalf("viewport render should include compact tool output, got %q", viewportPlain)
	}
	var toolBlock *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.ID == match.BlockID {
			toolBlock = m.viewport.GetFocusedBlock(b.ID)
			break
		}
	}
	if toolBlock == nil {
		t.Fatal("matched compact tool block should exist in visible window")
	}
	if toolBlock.ToolName != "Bash" {
		t.Fatalf("matched tool = %q, want Bash", toolBlock.ToolName)
	}
	if !toolBlock.ToolCallDetailExpanded {
		t.Fatal("search should expand compact tool detail")
	}
}

func TestEscClearsActiveSearchPillInNormalMode(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "/grep [1/1]") {
		t.Fatalf("status bar should show search session before ESC, got %q", plain)
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after ESC = %+v, want cleared", m.search.State)
	}
}

func TestDeferredStartupTranscriptSendDraftExtendsVisibleTailWindow(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+120)
	for i := 0; i < startupTranscriptWindowMinBlocks+120; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for sendDraft test")
	}
	before := len(m.viewport.visibleBlocks())
	if before != startupTranscriptTailBlocks {
		t.Fatalf("len(visibleBlocks()) before draft = %d, want %d", before, startupTranscriptTailBlocks)
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("message-020")
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("search should find deferred transcript block before draft")
	}
	if !m.maybeScrollToSearchMatch(match, "search_enter") {
		t.Fatal("maybeScrollToSearchMatch should succeed before draft")
	}

	_ = m.sendDraft(queuedDraft{Content: "new user message", QueuedAt: time.Now()})
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != before+1 {
		t.Fatalf("len(visibleBlocks()) after draft = %d, want %d", len(blocks), before+1)
	}
	last := blocks[len(blocks)-1]
	if last.Type != BlockUser || last.Content != "new user message" {
		t.Fatalf("last visible block = %#v, want appended user block", last)
	}
	if state := m.startupDeferredTranscript; state == nil || state.windowEnd != len(state.allBlocks) {
		t.Fatalf("startupDeferredTranscript after draft = %+v, want windowEnd synced to allBlocks", state)
	}
}

func TestDeferredStartupTranscriptForkKeepsOriginalVisibleMsgIndexAfterSendDraft(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+40)
	for i := 0; i < startupTranscriptWindowMinBlocks+40; i++ {
		if i%2 == 0 {
			messages = append(messages, message.Message{Role: "user", Content: fmt.Sprintf("user-%03d", i)})
			continue
		}
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("assistant-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for fork regression test")
	}

	var target *Block
	for _, block := range m.viewport.visibleBlocks() {
		if block != nil && block.Type == BlockUser && !block.IsUserLocalShell() {
			target = block
			break
		}
	}
	if target == nil {
		t.Fatal("expected visible user block in deferred transcript tail window")
	}
	targetID := target.ID
	wantMsgIndex := target.MsgIndex
	if wantMsgIndex < 0 {
		t.Fatalf("target.MsgIndex = %d, want valid main transcript index", wantMsgIndex)
	}

	m.focusedBlockID = targetID
	m.refreshBlockFocus()
	_ = m.sendDraft(queuedDraft{Content: "new user message", QueuedAt: time.Now()})

	target = m.viewport.GetFocusedBlock(targetID)
	if target == nil {
		t.Fatal("focused target block disappeared after sendDraft")
	}
	if got := target.MsgIndex; got != wantMsgIndex {
		t.Fatalf("target.MsgIndex after sendDraft = %d, want %d", got, wantMsgIndex)
	}

	m.inflightDraft = nil
	m.mode = ModeNormal
	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'e'}))
	if cmd == nil {
		t.Fatal("expected pending chord command on first e")
	}
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'e'}))
	if got := backend.forkMsgIndices; len(got) != 1 || got[0] != wantMsgIndex {
		t.Fatalf("ForkSession() calls after deferred ee = %+v, want [%d]", got, wantMsgIndex)
	}
}

func TestSearchPillShowsAcrossModesWhileSearchSessionActive(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}, {BlockIndex: 1}}
	m.search.State.Current = 0

	m.mode = ModeNormal
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [1/2]") {
		t.Fatalf("normal-mode status bar should show search pill, got %q", plain)
	}
	m.mode = ModeInsert
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [1/2]") {
		t.Fatalf("insert-mode status bar should show search pill, got %q", plain)
	}
	m.mode = ModeSearch
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [1/2]") {
		t.Fatalf("search-mode status bar should show search pill, got %q", plain)
	}
}

func TestHandleInsertKeyRoutesNewSessionCommandViaControlAPI(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/new")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if backend.newSessionCalls != 1 {
		t.Fatalf("NewSession() calls = %d, want 1", backend.newSessionCalls)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() should not be used, got %d calls", got)
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("viewport should not append a user block for /new, got %d blocks", got)
	}
}

func TestHandleAgentEventLoopNoticeNearBottomScrollsToBottom(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 8)
	for i := 0; i < 6; i++ {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("alpha\n", 2)})
	}
	m.viewport.ScrollToBottom()
	m.viewport.offset = max(0, m.viewport.offset-1)
	m.viewport.sticky = false

	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP",
		Text:  strings.Repeat("continue\n", 4),
	}})

	if !m.viewport.atBottom() {
		t.Fatalf("viewport should scroll to bottom after near-bottom loop notice append: offset=%d total=%d height=%d", m.viewport.offset, m.viewport.TotalLines(), m.viewport.height)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 || blocks[len(blocks)-1].Type != BlockStatus {
		t.Fatalf("last block = %#v, want trailing BlockStatus", blocks)
	}
}

func TestHandleInsertKeyRoutesResumeIDViaControlAPI(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/resume 123")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if len(backend.resumeIDs) != 1 || backend.resumeIDs[0] != "123" {
		t.Fatalf("ResumeSessionID() calls = %+v, want [123]", backend.resumeIDs)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() should not be used, got %d calls", got)
	}
	if !m.sessionSwitch.active() {
		t.Fatal("session switch indicator should be active after /resume <id>")
	}
	if m.sessionSwitch.kind != "resume" || m.sessionSwitch.sessionID != "123" {
		t.Fatalf("sessionSwitch = %+v, want kind=resume session=123", m.sessionSwitch)
	}
}

func TestHandleAgentEventLoopNoticeCreatesStatusCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP",
		Text:  "Target:\n- Continue and finish all remaining tasks in the current session.",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockStatus {
		t.Fatalf("block type = %v, want BlockStatus", blocks[0].Type)
	}
	if blocks[0].StatusTitle != "LOOP" {
		t.Fatalf("StatusTitle = %q, want %q", blocks[0].StatusTitle, "LOOP")
	}
	plain := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "LOOP") || !strings.Contains(plain, "Target:") {
		t.Fatalf("rendered card = %q, want LOOP status card", plain)
	}
	if !strings.Contains(plain, "  Target:") || !strings.Contains(plain, "  • Continue and finish all remaining tasks in the current session.") {
		t.Fatalf("rendered card = %q, want indented LOOP body", plain)
	}
}

func TestLoopStateChangedEventRefreshesStatusBarDuringStreaming(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	m := NewModelWithSize(backend, 120, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "main", Text: "partial"}})

	backend.loopState = agent.LoopStateExecuting
	backend.loopTarget = "finish current task"
	backend.loopIteration = 1
	backend.loopMaxIterations = 10
	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopStateChangedEvent{}})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want immediate loop pill while streaming", plain)
	}
}

func TestHandleAgentEventLoopContinueStatusCardBodyIsIndented(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP CONTINUE",
		Text:  "Unresolved work:\n- pending verification\n- remaining subagent",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockStatus {
		t.Fatalf("block type = %v, want BlockStatus", blocks[0].Type)
	}
	if blocks[0].StatusTitle != "LOOP CONTINUE" {
		t.Fatalf("StatusTitle = %q, want %q", blocks[0].StatusTitle, "LOOP CONTINUE")
	}
	plain := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "  Unresolved work:") || !strings.Contains(plain, "  • pending verification") || !strings.Contains(plain, "  • remaining subagent") {
		t.Fatalf("rendered card = %q, want indented LOOP CONTINUE body", plain)
	}
}

func TestHandleInsertKeyRoutesLoopCommandsViaControlAPI(t *testing.T) {
	backend := &sessionControlAgent{loopState: agent.LoopStateAssessing}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	m.input.SetValue("/loop on finish current task")
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.input.SetValue("/loop off")
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMessages); got != 2 || backend.sentMessages[0] != "/loop on finish current task" || backend.sentMessages[1] != "/loop off" {
		t.Fatalf("sentMessages = %#v, want forwarded loop commands", backend.sentMessages)
	}
}

func TestHandleInsertKeyStartsLoopTargetImmediatelyWhenIdle(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/loop on finish current task")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMessages); got != 1 || backend.sentMessages[0] != "/loop on finish current task" {
		t.Fatalf("sentMessages = %#v, want forwarded loop-on command", backend.sentMessages)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 0 {
		t.Fatalf("visible blocks = %#v, want no user loop block; loop start should be shown via control card event", blocks)
	}
}

func TestHandleInsertKeyShowsLoopUsageForBareLoopCommand(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/loop")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMessages); got != 1 || backend.sentMessages[0] != "/loop" {
		t.Fatalf("sentMessages = %#v, want bare /loop forwarded to agent", backend.sentMessages)
	}
	if m.activeToast != nil {
		t.Fatalf("active toast = %#v, want none; usage should come from agent/runtime", m.activeToast)
	}
}

func TestHandleInsertKeyShowsLoopUsageForBareLoopOnWhenIdle(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/loop on")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/loop on" {
		t.Fatalf("sentMessages = %#v, want bare /loop on forwarded to agent", backend.sentMessages)
	}
	if backend.loopTarget != "" {
		t.Fatalf("loopTarget = %q, want empty because TUI no longer handles /loop locally", backend.loopTarget)
	}
}

func TestHandleInsertHistoryUpLoadsLastUserMessageIntoComposer(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk", DisplayText: "[Pasted text #1 +11 lines]"},
			{Type: "image", MimeType: "image/png", Data: []byte{1, 2, 3}, FileName: "shot.png"},
		},
	}}}
	m := NewModel(backend)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	if got := m.input.Value(); got != "[Pasted text #1 +11 lines]" {
		t.Fatalf("input value = %q, want inline placeholder", got)
	}
	if !m.input.HasInlinePastes() {
		t.Fatal("expected inline paste metadata to be restored")
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("len(attachments) = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "shot.png" {
		t.Fatalf("attachment filename = %q, want shot.png", got)
	}
}

func TestRenderStatusBarShowsSessionSwitchProgress(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.beginSessionSwitch("resume", "123")

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar = %q, want resume progress", plain)
	}
}

func TestViewShowsSessionSwitchOverlayAfterDelay(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.beginSessionSwitch("resume", "123")
	m.sessionSwitch.startedAt = time.Now().Add(-sessionSwitchOverlayDelay - 10*time.Millisecond)
	m.layout = m.generateLayout(m.width, m.height)

	plain := stripANSI(m.View().Content)
	if !strings.Contains(plain, "Resuming session") {
		t.Fatalf("View() = %q, want session switch overlay title", plain)
	}
	if !strings.Contains(plain, "123") {
		t.Fatalf("View() = %q, want session switch overlay session badge", plain)
	}
}

func TestViewDelaysSessionSwitchOverlayToAvoidFlash(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.beginSessionSwitch("resume", "123")
	m.layout = m.generateLayout(m.width, m.height)

	plain := stripANSI(m.View().Content)
	if strings.Contains(plain, "Resuming session") {
		t.Fatalf("View() = %q, should not show delayed session switch overlay yet", plain)
	}
}

func TestRenderDisabledInputAreaPreservesTrailingNewline(t *testing.T) {
	rendered := renderDisabledInputArea("line 1\nline 2\n")
	if !strings.HasSuffix(rendered, "\n") {
		t.Fatalf("rendered = %q, want trailing newline preserved", rendered)
	}
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("rendered = %q, want ANSI styling for dimmed disabled input", rendered)
	}
}

func TestViewHidesInputCursorWhileSessionSwitchInteractionSuppressed(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.input.Focus()
	m.beginSessionSwitch("resume", "123")
	m.layout = m.generateLayout(m.width, m.height)

	view := m.View()
	if view.Cursor != nil {
		t.Fatalf("View().Cursor = %#v, want nil while interaction is suppressed", view.Cursor)
	}
}

func TestSessionSwitchStartedEventUpdatesStatusBarAndRestoredClearsIt(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "assistant", Content: "restored"}}, providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar after start = %q, want resume progress", plain)
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if m.sessionSwitch.active() {
		t.Fatal("session switch indicator should clear after SessionRestoredEvent")
	}
	plain = stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar after restore = %q, should not show stale resume progress", plain)
	}
}

func TestSessionRestoredEventRebuildsTranscriptAndTodoOrder(t *testing.T) {
	backend := &sessionControlAgent{
		messages: []message.Message{{Role: "assistant", Content: "old message"}},
		todos:    []tools.TodoItem{{ID: "old", Status: "pending", Content: "Old todo"}},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "stale block"})
	_ = m.renderInfoPanel(40, 20)
	m.beginSessionSwitch("resume", "123")

	backend.messages = []message.Message{{Role: "assistant", Content: "restored message"}}
	backend.todos = []tools.TodoItem{
		{ID: "2", Status: "in_progress", Content: "Phase 2"},
		{ID: "3", Status: "pending", Content: "Phase 3"},
		{ID: "run", Status: "pending", Content: "Run verification checks"},
		{ID: "1", Status: "completed", Content: "Phase 1"},
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)

	if m.sessionSwitch.active() {
		t.Fatal("session switch indicator should clear after SessionRestoredEvent")
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Content != "restored message" {
		t.Fatalf("visibleBlocks() = %#v, want rebuilt restored transcript", blocks)
	}
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "TODOS")
	want := []string{
		"▶ Phase 2",
		"○ Phase 3",
		"○ Run verification checks",
		"✓ Phase 1",
	}
	if len(section) != len(want) {
		t.Fatalf("todo section lines = %#v, want %#v", section, want)
	}
	for i, line := range want {
		if section[i] != line {
			t.Fatalf("todo line %d = %q, want %q (section=%#v)", i, section[i], line, section)
		}
	}
}

func TestModelUsesCacheDirRuntimeCacheForCurrentSession(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)

	backend := &sessionControlAgent{
		sessionSummary: &agent.SessionSummary{ID: "session-1"},
	}
	m := NewModelWithSize(backend, 80, 24)
	defer func() { _ = m.Close() }()

	if m.runtimeCacheHandle == nil {
		t.Fatal("expected runtime cache handle")
	}
	wantRoot := filepath.Join(cacheDir, "runtime", "session-cache")
	if got := m.runtimeCacheHandle.Dir(); !strings.HasPrefix(got, wantRoot) {
		t.Fatalf("runtime cache dir = %q, want prefix %q", got, wantRoot)
	}
}

func TestSessionRestoredEventSwitchesRuntimeCacheAndRemovesOldSessionDir(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)

	backend := &sessionControlAgent{
		sessionSummary: &agent.SessionSummary{ID: "old-session"},
	}
	m := NewModelWithSize(backend, 80, 24)
	oldDir := m.runtimeCacheHandle.Dir()

	backend.sessionSummary = &agent.SessionSummary{ID: "new-session"}
	m.beginSessionSwitch("resume", "new-session")
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	defer func() { _ = m.Close() }()

	if m.runtimeCacheHandle == nil || m.runtimeCacheHandle.SessionID() != "new-session" {
		t.Fatalf("runtime cache session = %#v, want new-session", m.runtimeCacheHandle)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old runtime cache dir err = %v, want not exist", err)
	}
	if _, err := os.Stat(m.runtimeCacheHandle.Dir()); err != nil {
		t.Fatalf("new runtime cache dir stat: %v", err)
	}
}

func TestStartupRestoreResetsTargetRuntimeCacheBeforeOpen(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)
	mgr, err := runtimecache.NewManager(cacheDir)
	if err != nil {
		t.Fatalf("NewManager(): %v", err)
	}

	projectRoot := t.TempDir()
	handle, err := mgr.OpenSession(projectRoot, "restored-session")
	if err != nil {
		t.Fatalf("OpenSession(): %v", err)
	}
	staleMarker := filepath.Join(handle.ViewportDir(), "stale.txt")
	if err := os.WriteFile(staleMarker, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile(stale): %v", err)
	}
	staleDir := handle.Dir()
	if err := handle.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir(projectRoot): %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()

	backend := &sessionControlAgent{
		resumePending:   true,
		startupResumeID: "restored-session",
		sessionSummary:  &agent.SessionSummary{ID: "restored-session"},
	}
	m := NewModelWithSize(backend, 80, 24)
	m.workingDir = projectRoot
	if m.runtimeCacheHandle != nil {
		t.Fatal("startup restore should defer runtime cache open until SessionRestoredEvent")
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	defer func() { _ = m.Close() }()

	if m.runtimeCacheHandle == nil || m.runtimeCacheHandle.SessionID() != "restored-session" {
		t.Fatalf("runtime cache session = %#v, want restored-session", m.runtimeCacheHandle)
	}
	if got := m.runtimeCacheHandle.Dir(); got != staleDir {
		t.Fatalf("runtime cache dir = %q, want %q", got, staleDir)
	}
	if _, err := os.Stat(staleMarker); !os.IsNotExist(err) {
		t.Fatalf("stale marker err = %v, want not exist", err)
	}
	if _, err := os.Stat(staleDir); err != nil {
		t.Fatalf("stale dir stat after reopen: %v", err)
	}
}

func TestModelCloseRemovesCurrentSessionRuntimeCache(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)

	backend := &sessionControlAgent{
		sessionSummary: &agent.SessionSummary{ID: "session-close"},
	}
	m := NewModelWithSize(backend, 80, 24)
	if m.runtimeCacheHandle == nil {
		t.Fatal("expected runtime cache handle")
	}
	dir := m.runtimeCacheHandle.Dir()

	if err := m.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("runtime cache dir err = %v, want not exist", err)
	}
}

func TestSessionSwitchIndicatorClearsOnError(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.beginSessionSwitch("resume", "123")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ErrorEvent{Err: errors.New("resume failed")}})

	if m.sessionSwitch.active() {
		t.Fatal("session switch indicator should clear on ErrorEvent")
	}
}

func TestSelectSessionAtCursorStartsStatusIndicator(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionSelect
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456"}},
		list:    NewOverlayList(nil, 5),
	}
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.list.SetCursor(1)

	_ = m.selectSessionAtCursor()

	if len(backend.resumeIDs) != 1 || backend.resumeIDs[0] != "456" {
		t.Fatalf("ResumeSessionID() calls = %+v, want [456]", backend.resumeIDs)
	}
	if !m.sessionSwitch.active() || m.sessionSwitch.kind != "resume" || m.sessionSwitch.sessionID != "456" {
		t.Fatalf("sessionSwitch = %+v, want active resume/456", m.sessionSwitch)
	}
}

func applyTestCmd(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	updated, next := m.Update(msg)
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model != m {
		*m = *model
	}
	return next
}

func TestHandleSessionSelectKeyDeleteOpensConfirmOverlay(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionSelect
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456", ForkedFrom: "100"}},
		list:    NewOverlayList(nil, 5),
	}
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.list.SetCursor(1)

	_ = m.handleSessionSelectKey(tea.KeyPressMsg(tea.Key{Code: 'd'}))

	if m.mode != ModeSessionDeleteConfirm {
		t.Fatalf("mode = %v, want ModeSessionDeleteConfirm", m.mode)
	}
	if m.sessionDeleteConfirm.session == nil || m.sessionDeleteConfirm.session.ID != "456" {
		t.Fatalf("sessionDeleteConfirm = %+v, want selected session 456", m.sessionDeleteConfirm)
	}
}

func TestHandleSessionDeleteConfirmKeyDeletesSelectedSessionAndUpdatesList(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionDeleteConfirm
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456"}},
		list:    NewOverlayList(nil, 5),
	}
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.list.SetCursor(1)
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session:  &agent.SessionSummary{ID: "456"},
		prevMode: ModeSessionSelect,
	}

	cmd := m.handleSessionDeleteConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if cmd == nil {
		t.Fatal("expected delete confirm command")
	}
	if got := backend.deleteSessionIDs; len(got) != 1 || got[0] != "456" {
		t.Fatalf("DeleteSession() calls = %+v, want [456]", got)
	}
	if m.mode != ModeSessionSelect {
		t.Fatalf("mode = %v, want ModeSessionSelect", m.mode)
	}
	if len(m.sessionSelect.options) != 1 || m.sessionSelect.options[0].ID != "123" {
		t.Fatalf("sessionSelect.options = %+v, want only session 123 remaining", m.sessionSelect.options)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "Deleted session 456") {
		t.Fatalf("activeToast = %+v, want delete success toast", m.activeToast)
	}
}

func TestHandleSessionDeleteConfirmKeyShowsErrorAndKeepsListOnFailure(t *testing.T) {
	backend := &sessionControlAgent{deleteSessionErr: errors.New("locked")}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionDeleteConfirm
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456"}},
		list:    NewOverlayList(nil, 5),
	}
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.list.SetCursor(1)
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session:  &agent.SessionSummary{ID: "456"},
		prevMode: ModeSessionSelect,
	}

	cmd := m.handleSessionDeleteConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if cmd == nil {
		t.Fatal("expected delete error toast command")
	}
	if len(m.sessionSelect.options) != 2 {
		t.Fatalf("sessionSelect.options = %+v, want unchanged list", m.sessionSelect.options)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "locked") {
		t.Fatalf("activeToast = %+v, want delete error toast", m.activeToast)
	}
}

func TestRenderStatusBarHidesForkOriginPill(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "2000", ForkedFrom: "1000"}}
	m := NewModelWithSize(backend, 100, 24)
	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "↳ 1000") {
		t.Fatalf("status bar = %q, want no fork origin pill", plain)
	}
}

func TestHandleNormalKeyForkRequiresDoubleE(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "edit me", MsgIndex: 2})
	m.focusedBlockID = 1

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'e'}))
	if cmd == nil {
		t.Fatal("expected pending chord command on first e")
	}
	if got := len(backend.forkMsgIndices); got != 0 {
		t.Fatalf("ForkSession() calls after first e = %d, want 0", got)
	}
	if m.chord.op != chordE {
		t.Fatalf("chord op = %v, want chordE", m.chord.op)
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'e'}))
	if got := backend.forkMsgIndices; len(got) != 1 || got[0] != 2 {
		t.Fatalf("ForkSession() calls after ee = %+v, want [2]", got)
	}
}

func TestHandleNormalKeyForkIgnoresNonForkableBlocks(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockCompactionSummary, Content: "summary", MsgIndex: -1})
	m.focusedBlockID = 1

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'e'}))

	if got := len(backend.forkMsgIndices); got != 0 {
		t.Fatalf("ForkSession() calls = %d, want 0", got)
	}
}

func TestHandleNormalKeyForkBusyShowsToastAndDoesNotFork(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "edit me", MsgIndex: 2})
	m.focusedBlockID = 1

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'e'}))

	if cmd == nil {
		t.Fatal("expected toast command while busy")
	}
	if got := len(backend.forkMsgIndices); got != 0 {
		t.Fatalf("ForkSession() calls = %d, want 0", got)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "Wait until the agent is idle before forking") {
		t.Fatalf("activeToast = %+v, want busy fork warning", m.activeToast)
	}
}

func TestSendDraftAppendsForkableMainUserBlockWithCurrentMsgIndex(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "assistant", Content: "prior"}}}
	m := NewModel(backend)
	draft := queuedDraft{Content: "hello"}

	_ = m.sendDraft(draft)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		t.Fatal("expected visible blocks after sendDraft")
	}
	last := blocks[len(blocks)-1]
	if last.Type != BlockUser || last.Content != "hello" {
		t.Fatalf("last block = %#v, want live user block 'hello'", last)
	}
	if got := last.MsgIndex; got != 1 {
		t.Fatalf("MsgIndex = %d, want 1 for next main transcript slot", got)
	}
}

func TestSyncVisibleMainUserBlockMsgIndexesRepairsOnlyMismatchedBlocks(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "mid"},
		{Role: "user", Content: "target"},
	}}
	m := NewModel(backend)
	m.viewport.ReplaceBlocks(nil)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "first", MsgIndex: 0})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "target", MsgIndex: 0})

	m.syncVisibleMainUserBlockMsgIndexes()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("len(visibleBlocks()) = %d, want 2", len(blocks))
	}
	if got := blocks[0].MsgIndex; got != 0 {
		t.Fatalf("blocks[0].MsgIndex = %d, want 0 for already-matched block", got)
	}
	if got := blocks[1].MsgIndex; got != 2 {
		t.Fatalf("blocks[1].MsgIndex = %d, want 2 for repaired block", got)
	}
}

func TestPendingDraftConsumedEventAssignsMsgIndexForMainUserBlock(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "assistant", Content: "prior"},
		{Role: "user", Content: "queued"},
	}}
	m := NewModel(backend)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{
		DraftID: "draft-1",
		Parts:   []message.ContentPart{{Type: "text", Text: "queued"}},
	}})

	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		t.Fatal("expected visible blocks after PendingDraftConsumedEvent")
	}
	last := blocks[len(blocks)-1]
	if last.Type != BlockUser || last.Content != "queued" {
		t.Fatalf("last block = %#v, want consumed user block 'queued'", last)
	}
	if got := last.MsgIndex; got != 1 {
		t.Fatalf("MsgIndex = %d, want committed user message index 1", got)
	}
}

func TestSendDraftAppendsUserBlockWithInvalidMsgIndex(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	draft := queuedDraft{Content: "hello"}

	_ = m.sendDraft(draft)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	if got := blocks[0].MsgIndex; got != 0 {
		t.Fatalf("MsgIndex = %d, want 0 for the first main transcript slot", got)
	}
}

func TestPendingDraftConsumedEventAppendsUserBlockWithInvalidMsgIndex(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{
		DraftID: "draft-1",
		Parts:   []message.ContentPart{{Type: "text", Text: "queued"}},
	}})

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	if got := blocks[0].MsgIndex; got != -1 {
		t.Fatalf("MsgIndex = %d, want -1 when committed message cannot be matched", got)
	}
}

func TestHandleInsertDiagnosticsCommandStaysLocal(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/diagnostics")

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if cmd == nil {
		t.Fatal("expected diagnostics command")
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if got := len(backend.resumeIDs); got != 0 {
		t.Fatalf("ResumeSessionID() calls = %d, want 0", got)
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("viewport should remain unchanged, got %d blocks", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty", got)
	}
}

func TestHandleInsertDiagnosticsShortcutStaysLocal(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("draft")

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))

	if cmd == nil {
		t.Fatal("expected diagnostics shortcut command")
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if got := len(backend.resumeIDs); got != 0 {
		t.Fatalf("ResumeSessionID() calls = %d, want 0", got)
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("viewport should remain unchanged, got %d blocks", got)
	}
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("input value = %q, want draft preserved", got)
	}
}

func TestHandleNormalDiagnosticsShortcutReturnsCmd(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected diagnostics shortcut command")
	}
}

func TestNewModelShowsStartupResumeStatusAndKeepsInteractionSuppressedUntilRestored(t *testing.T) {
	backend := &sessionControlAgent{
		resumePending:   true,
		startupResumeID: "123",
		messages: []message.Message{
			{Role: "assistant", Content: "restored assistant"},
		},
	}

	m := NewModelWithSize(backend, 120, 24)

	if !m.startupRestorePending {
		t.Fatal("startupRestorePending should remain true until SessionRestoredEvent arrives")
	}
	if !m.sessionSwitch.active() || m.sessionSwitch.kind != "resume" || m.sessionSwitch.sessionID != "123" {
		t.Fatalf("sessionSwitch = %+v, want active resume/123", m.sessionSwitch)
	}
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar = %q, want startup resume progress", plain)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 0 {
		t.Fatalf("visibleBlocks() = %#v, want startup loading placeholder before SessionRestoredEvent", blocks)
	}

	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "i", Code: 'i'}))
	if cmd != nil {
		t.Fatal("suppressed interaction should not return a command")
	}
	got, ok := updated.(*Model)
	if !ok {
		t.Fatalf("updated model type = %T, want *Model", updated)
	}
	if got.mode != ModeInsert {
		t.Fatalf("mode = %v, want insert unchanged while startup restore pending", got.mode)
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if m.startupRestorePending {
		t.Fatal("startupRestorePending should clear after SessionRestoredEvent")
	}
	blocks = m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Content != "restored assistant" {
		t.Fatalf("visibleBlocks() = %#v, want rebuilt restored transcript after SessionRestoredEvent", blocks)
	}
}

func TestStartupRestoredLargeTranscriptUsesWindowedTailUntilHistoryNeeded(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := 0; i < startupTranscriptWindowMinBlocks+24; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("large startup transcript should defer full hydrate after initial restore")
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != startupTranscriptTailBlocks {
		t.Fatalf("len(visibleBlocks()) = %d, want %d (tail window only)", len(blocks), startupTranscriptTailBlocks)
	}
	if blocks[0].Content != fmt.Sprintf("message-%03d", len(messages)-startupTranscriptTailBlocks) {
		t.Fatalf("tail-window first block = %q, want first visible tail block", blocks[0].Content)
	}

	m.jumpToVisibleBlockOrdinal(1)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("jump to top should keep deferred startup transcript in windowed mode")
	}
	blocks = m.viewport.visibleBlocks()
	if len(blocks) != startupTranscriptTailBlocks {
		t.Fatalf("len(visibleBlocks()) after jump-top window switch = %d, want %d", len(blocks), startupTranscriptTailBlocks)
	}
	if blocks[0].Content != "message-000" {
		t.Fatalf("top-window first block = %q, want first transcript block", blocks[0].Content)
	}
	if blocks[len(blocks)-1].Content != fmt.Sprintf("message-%03d", startupTranscriptTailBlocks-1) {
		t.Fatalf("top-window last block = %q, want last visible top-window block", blocks[len(blocks)-1].Content)
	}

	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'}))
	blocks = m.viewport.visibleBlocks()
	if blocks[0].Content != fmt.Sprintf("message-%03d", len(messages)-startupTranscriptTailBlocks) {
		t.Fatalf("tail-window first block after G = %q, want first visible tail block", blocks[0].Content)
	}
	if blocks[len(blocks)-1].Content != fmt.Sprintf("message-%03d", len(messages)-1) {
		t.Fatalf("tail-window last block after G = %q, want newest transcript block", blocks[len(blocks)-1].Content)
	}
}

func TestDeferredStartupTranscriptWindowSwitchKeepsLiveToolResult(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := 0; i < startupTranscriptWindowMinBlocks+24; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for live tool-result window switch test")
	}

	argsJSON := `{"path":"internal/tui/app.go","limit":4,"offset":0}`
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "live-read-1",
		Name:     "Read",
		ArgsJSON: argsJSON,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "live-read-1",
		Name:     "Read",
		ArgsJSON: argsJSON,
		Result:   "1\talpha line\n2\tbeta line\n3\tgamma line\n4\tomega line",
		Status:   agent.ToolResultStatusSuccess,
	}})

	liveToolID := -1
	for _, block := range m.viewport.visibleBlocks() {
		if block != nil && block.ToolID == "live-read-1" {
			liveToolID = block.ID
			break
		}
	}
	if liveToolID < 0 {
		t.Fatal("expected live tool block in visible tail window")
	}
	if block := m.viewport.GetFocusedBlock(liveToolID); block == nil || !block.ResultDone || strings.TrimSpace(block.ResultContent) == "" {
		t.Fatalf("live tool block before window switch = %#v, want completed result", block)
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("gg should keep deferred startup transcript active")
	}
	m.handleNormalKey(modelSelectKey("G"))

	block := m.viewport.GetFocusedBlock(liveToolID)
	if block == nil {
		t.Fatal("expected live tool block after switching back to tail window")
	}
	if !block.ResultDone {
		t.Fatalf("tool block ResultDone after gg/G = %v, want true", block.ResultDone)
	}
	if got := strings.TrimSpace(block.ResultContent); !strings.Contains(got, "gamma line") {
		t.Fatalf("tool block ResultContent after gg/G = %q, want preserved result", got)
	}
	if block.ToolExecutionState != "" {
		t.Fatalf("tool execution state after gg/G = %q, want terminal empty state", block.ToolExecutionState)
	}
}

func TestDeferredStartupTranscriptWindowSwitchDoesNotResurrectRolledBackAssistant(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := 0; i < startupTranscriptWindowMinBlocks+24; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for rollback window switch test")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "temporary reply"}})
	if m.currentAssistantBlock == nil {
		t.Fatal("expected streaming assistant block before rollback")
	}
	rolledBackID := m.currentAssistantBlock.ID
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{Reason: "retry"}})

	if block := m.viewport.GetFocusedBlock(rolledBackID); block != nil {
		t.Fatalf("rolled-back block still visible immediately after rollback: %#v", block)
	}
	if block := m.startupDeferredBlockByID(rolledBackID); block != nil {
		t.Fatalf("rolled-back block still present in deferred state: %#v", block)
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("G"))

	if block := m.viewport.GetFocusedBlock(rolledBackID); block != nil {
		t.Fatalf("rolled-back block resurrected after gg/G: %#v", block)
	}
}

func TestFocusedAgentSwitchRebuildsFullTranscriptAfterDeferredStartup(t *testing.T) {
	mainMessages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := 0; i < startupTranscriptWindowMinBlocks+24; i++ {
		mainMessages = append(mainMessages, message.Message{Role: "assistant", Content: fmt.Sprintf("main-%03d", i)})
	}
	subMessages := make([]message.Message, 0, 70)
	for i := 0; i < 70; i++ {
		subMessages = append(subMessages, message.Message{Role: "assistant", Content: fmt.Sprintf("sub-%03d", i)})
	}
	backend := &sessionControlAgent{
		resumePending:   true,
		startupResumeID: "123",
		messages:        mainMessages,
		messagesByFocus: map[string][]message.Message{
			"":        mainMessages,
			"agent-1": subMessages,
		},
	}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("large startup transcript should remain deferred before focus switch")
	}
	for i := 0; i < 20; i++ {
		m.viewport.AppendBlock(&Block{
			ID:      1000 + i,
			Type:    BlockAssistant,
			AgentID: "agent-1",
			Content: fmt.Sprintf("partial-sub-%02d", i),
		})
	}

	m.setFocusedAgent("agent-1")
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != len(subMessages) {
		t.Fatalf("len(visibleBlocks()) in subagent view = %d, want %d", len(blocks), len(subMessages))
	}
	if blocks[0].Content != "sub-000" {
		t.Fatalf("subagent first block = %q, want rebuilt transcript start", blocks[0].Content)
	}
	if m.hasDeferredStartupTranscript() {
		t.Fatal("focus switch should exit deferred startup transcript mode")
	}

	m.setFocusedAgent("")
	blocks = m.viewport.visibleBlocks()
	if len(blocks) != len(mainMessages) {
		t.Fatalf("len(visibleBlocks()) after returning to main = %d, want %d", len(blocks), len(mainMessages))
	}
	if blocks[0].Content != "main-000" {
		t.Fatalf("main first block = %q, want rebuilt main transcript start", blocks[0].Content)
	}
}

func TestDeferredStartupTranscriptCountedJumpBuildsTargetWindow(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for counted jump test")
	}

	m.jumpToVisibleBlockOrdinal(120)
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != startupTranscriptTailBlocks {
		t.Fatalf("len(visibleBlocks()) = %d, want %d (middle window)", len(blocks), startupTranscriptTailBlocks)
	}
	focused := m.viewport.GetBlockAtOffset()
	if focused == nil || focused.Content != "message-119" {
		t.Fatalf("jump target at current offset = %#v, want message-119", focused)
	}
}

func TestDeferredStartupTranscriptScrollAndPageMoveBetweenWindows(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	m.jumpToVisibleBlockOrdinal(120)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for window movement test")
	}
	start := state.windowStart
	m.viewport.offset = 0
	m.repeatNormalVertical(-1, 1)
	if m.startupDeferredTranscript.windowStart >= start {
		t.Fatalf("scrolling above top of deferred window should move earlier, got start=%d want < %d", m.startupDeferredTranscript.windowStart, start)
	}

	midStart := m.startupDeferredTranscript.windowStart
	m.viewport.ScrollToBottom()
	m.maybePageStartupDeferredTranscriptWindow(1, "page_down_test")
	if m.startupDeferredTranscript.windowStart <= midStart {
		t.Fatalf("page down at bottom of deferred window should move later, got start=%d want > %d", m.startupDeferredTranscript.windowStart, midStart)
	}
}

func TestDeferredStartupTranscriptSearchNavigatesWithoutHydrate(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for deferred search test")
	}

	cmd = m.handleNormalKey(modelSelectKey("/"))
	applyTestCmd(t, &m, cmd)
	if m.mode != ModeSearch {
		t.Fatalf("mode = %v, want ModeSearch", m.mode)
	}
	m.search.Input.SetValue("message-020")
	cmd = m.handleSearchKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	applyTestCmd(t, &m, cmd)

	if !m.hasDeferredStartupTranscript() {
		t.Fatal("search enter should not fully hydrate deferred startup transcript")
	}
	focused := m.viewport.GetBlockAtOffset()
	if focused == nil || focused.Content != "message-020" {
		t.Fatalf("search focused block = %#v, want message-020", focused)
	}

	cmd = m.handleNormalKey(modelSelectKey("n"))
	applyTestCmd(t, &m, cmd)
	focused = m.viewport.GetBlockAtOffset()
	if focused == nil || focused.Content != "message-020" {
		t.Fatalf("search next focused block = %#v, want message-020", focused)
	}
}

func TestDeferredStartupTranscriptDirectoryNavigatesWithoutHydrate(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for deferred directory test")
	}

	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}))
	applyTestCmd(t, &m, cmd)
	if m.mode != ModeDirectory {
		t.Fatalf("mode = %v, want ModeDirectory", m.mode)
	}
	if len(m.dirEntries) != len(messages) {
		t.Fatalf("len(dirEntries) = %d, want %d", len(m.dirEntries), len(messages))
	}
	if m.dirList == nil {
		t.Fatal("directory list should be initialized")
	}
	m.dirList.SetCursor(20)
	cmd = m.handleDirectoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	applyTestCmd(t, &m, cmd)

	if !m.hasDeferredStartupTranscript() {
		t.Fatal("directory enter should not fully hydrate deferred startup transcript")
	}
	focused := m.viewport.GetBlockAtOffset()
	if focused == nil || focused.Content != "message-020" {
		t.Fatalf("directory focused block = %#v, want message-020", focused)
	}
}

func TestDeferredStartupTranscriptRetentionShrinksViewportHotBudgetUntilHydrate(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 64))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	origBudget := m.viewport.maxHotBytes

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for retention test")
	}
	if got := m.viewport.maxHotBytes; got != startupDeferredTranscriptAggressiveHotBytes {
		t.Fatalf("viewport maxHotBytes = %d, want %d during deferred startup retention", got, startupDeferredTranscriptAggressiveHotBytes)
	}
	if state := m.startupDeferredTranscript; state == nil || state.originalViewportBudget != origBudget {
		t.Fatalf("original viewport budget = %v, want %d", state, origBudget)
	}

	if !m.maybeHydrateStartupDeferredTranscript("retention_test") {
		t.Fatal("maybeHydrateStartupDeferredTranscript should hydrate deferred transcript")
	}
	if got := m.viewport.maxHotBytes; got != origBudget {
		t.Fatalf("viewport maxHotBytes after hydrate = %d, want %d", got, origBudget)
	}
}

func TestDeferredStartupTranscriptMetadataSupportsSearchAndDirectoryAfterSpill(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 64))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for metadata retention test")
	}
	if len(state.blockMeta) != len(messages) {
		t.Fatalf("len(blockMeta) = %d, want %d", len(state.blockMeta), len(messages))
	}

	for i, block := range state.allBlocks {
		if block == nil {
			continue
		}
		if startupDeferredBlockLineCount(state.blockMeta[i], m.viewport.width) <= 0 {
			t.Fatalf("blockMeta[%d] missing usable line count for viewport width %d", i, m.viewport.width)
		}
		if i < state.windowStart || i >= state.windowEnd {
			block.searchTextLower = ""
			block.searchTextReady = false
			block.spillSummary = ""
			block.spillLineCounts = nil
			if !block.spillCold {
				m.viewport.spillBlock(block)
			}
		}
	}

	matches := m.deferredStartupTranscriptSearch("message-020")
	if len(matches) != 1 || matches[0].BlockID != state.blockMeta[20].BlockID {
		t.Fatalf("deferred search matches = %#v, want single hit for message-020", matches)
	}
	entries := m.deferredStartupTranscriptDirectoryEntries()
	if len(entries) != len(messages) {
		t.Fatalf("len(entries) = %d, want %d", len(entries), len(messages))
	}
	if entries[20].Summary == "" || !strings.Contains(entries[20].Summary, "message-020") {
		t.Fatalf("entries[20].Summary = %q, want summary for message-020", entries[20].Summary)
	}
}

func TestDeferredStartupTranscriptSearchAndDirectorySurviveViewportResize(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := 0; i < startupTranscriptWindowMinBlocks+200; i++ {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 48))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for resize test")
	}

	m.applyTerminalSize(87, 24, false)
	if m.viewport.width != 87 {
		t.Fatalf("viewport width = %d, want 87", m.viewport.width)
	}

	matches := m.deferredStartupTranscriptSearch("message-020")
	if len(matches) != 1 {
		t.Fatalf("len(matches) = %d, want 1 after resize", len(matches))
	}
	if !m.maybeScrollToSearchMatch(matches[0], "resize_search") {
		t.Fatal("maybeScrollToSearchMatch should succeed after resize")
	}
	focused := m.viewport.GetBlockAtOffset()
	if focused == nil || !strings.Contains(focused.Content, "message-020") {
		t.Fatalf("focused block after resize search = %#v, want message-020", focused)
	}

	entries := m.deferredStartupTranscriptDirectoryEntries()
	if len(entries) != len(messages) {
		t.Fatalf("len(entries) = %d, want %d after resize", len(entries), len(messages))
	}
	if !m.maybeScrollToDirectoryEntry(entries[150], "resize_directory") {
		t.Fatal("maybeScrollToDirectoryEntry should succeed after resize")
	}
	focused = m.viewport.GetBlockAtOffset()
	if focused == nil || !strings.Contains(focused.Content, "message-150") {
		t.Fatalf("focused block after resize directory jump = %#v, want message-150", focused)
	}
}

func TestNewModelRestoresStartupMessagesIntoViewport(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "user", Content: "restored user"},
		{Role: "assistant", Content: "restored assistant"},
	}}

	m := NewModelWithSize(backend, 100, 30)

	if m.startupRestorePending {
		t.Fatal("startupRestorePending should clear after eagerly rebuilding restored messages")
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("len(visibleBlocks()) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != BlockUser || blocks[0].Content != "restored user" {
		t.Fatalf("first block = %#v, want restored user block", blocks[0])
	}
	if blocks[1].Type != BlockAssistant || blocks[1].Content != "restored assistant" {
		t.Fatalf("second block = %#v, want restored assistant block", blocks[1])
	}
}

func TestNewModelDoesNotEagerlyRebuildWhenBackendFocusedOnSubAgent(t *testing.T) {
	backend := &focusedMessagesAgent{
		focused: "agent-1",
		messagesByFocus: map[string][]message.Message{
			"agent-1": {
				{Role: "assistant", Content: "subagent history"},
			},
		},
	}

	m := NewModelWithSize(backend, 100, 30)

	if m.startupRestorePending {
		t.Fatal("startupRestorePending should stay false when backend focus is a subagent")
	}
	if backend.focused != "agent-1" {
		t.Fatalf("backend focus = %q, want subagent focus preserved", backend.focused)
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("len(visibleBlocks()) = %d, want 0 before explicit focus switch/rebuild", got)
	}
}

func TestRebuildViewportFromMessagesClearsBlocksForEmptySession(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "stale"})

	m.rebuildViewportFromMessages()

	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("len(visibleBlocks()) = %d, want 0", got)
	}
}

func TestRebuildViewportFromMessagesRestoresReadBlankLineResult(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "tool-read", Name: "Read", Args: []byte(`{"path":"internal/tui/input.go","limit":1,"offset":358}`)},
			},
		},
		{Role: "tool", ToolCallID: "tool-read", Content: "   359\t\n"},
	}}
	m := NewModel(backend)

	m.rebuildViewportFromMessages()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.ToolName != "Read" || !block.ResultDone {
		t.Fatalf("restored block = %#v, want completed Read block", block)
	}
	if !block.SettledAt.IsZero() {
		t.Fatalf("restored block SettledAt = %v, want zero", block.SettledAt)
	}
	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "Read internal/tui/input.go") {
		t.Fatalf("expected restored Read header, got:\n%s", plain)
	}
	if !strings.Contains(plain, "359") {
		t.Fatalf("expected restored blank numbered line, got:\n%s", plain)
	}
}

func TestRebuildViewportFromMessagesRestoresUserPartsWithExpandedPasteText(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "header\n"},
			{Type: "text", Text: "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk", DisplayText: "[Pasted text #1 +11 lines]"},
			{Type: "text", Text: "\nfooter"},
		},
	}}}
	m := NewModelWithSize(backend, 100, 30)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockUser {
		t.Fatalf("block type = %v, want BlockUser", blocks[0].Type)
	}
	want := "header\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nfooter"
	if got := blocks[0].Content; got != want {
		t.Fatalf("restored user block content = %q, want expanded raw text", got)
	}
	if strings.Contains(blocks[0].Content, "[Pasted text #1 +11 lines]") {
		t.Fatalf("restored user block should not contain placeholder: %q", blocks[0].Content)
	}
}

func TestRebuildViewportFromMessagesDoesNotShowLastForRestoredBlocks(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "user", Content: "restored user"},
		{Role: "assistant", Content: "restored assistant"},
	}}
	m := NewModelWithSize(backend, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}

	m.rebuildViewportFromMessages()

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, statusBarIdleLabel(false)) || strings.Contains(plain, statusBarIdleLabel(true)) {
		t.Fatalf("restored history should not fabricate Last time; got %q", plain)
	}
}

func TestRebuildViewportFromMessagesClearsTimingState(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "assistant", Content: "restored assistant"}}}
	m := NewModelWithSize(backend, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-10 * time.Second)
	m.activityLastChanged["main"] = time.Now().Add(-9 * time.Second)
	m.turnBusyStartedAt["main"] = time.Now().Add(-90 * time.Second)
	m.localShellStartedAt = time.Now().Add(-5 * time.Second)
	m.backgroundIdleSince = time.Now().Add(-6 * time.Minute)
	m.lastSweepAt = time.Now().Add(-1 * time.Minute)
	m.idleSweepScheduled = true
	m.animRunning = true

	m.rebuildViewportFromMessages()

	if got := m.activities["main"]; got.Type != agent.ActivityIdle {
		t.Fatalf("main activity = %q, want idle", got.Type)
	}
	if _, ok := m.activityStartTime["main"]; ok {
		t.Fatal("activityStartTime should be cleared on restore")
	}
	if _, ok := m.activityLastChanged["main"]; ok {
		t.Fatal("activityLastChanged should be cleared on restore")
	}
	if _, ok := m.turnBusyStartedAt["main"]; ok {
		t.Fatal("turnBusyStartedAt should be cleared on restore")
	}
	if !m.localShellStartedAt.IsZero() {
		t.Fatalf("localShellStartedAt = %v, want zero", m.localShellStartedAt)
	}
	if !m.backgroundIdleSince.IsZero() {
		t.Fatalf("backgroundIdleSince = %v, want zero", m.backgroundIdleSince)
	}
	if !m.lastSweepAt.IsZero() {
		t.Fatalf("lastSweepAt = %v, want zero", m.lastSweepAt)
	}
	if m.idleSweepScheduled {
		t.Fatal("idleSweepScheduled should be false after restore timing reset")
	}
	if m.idleSweepGeneration != 2 {
		t.Fatalf("idleSweepGeneration = %d, want 2 after invalidation", m.idleSweepGeneration)
	}
	if m.animRunning {
		t.Fatal("animRunning should be false after restore timing reset")
	}
}

func TestSessionRestoredEventSchedulesImageProtocolRedrawForRestoredImages(t *testing.T) {
	pngData := makeTestPNG(t)
	backend := &sessionControlAgent{messages: []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{{
			Type:     "image",
			MimeType: "image/png",
			Data:     pngData,
			FileName: "sample.png",
		}},
	}}}
	m := NewModel(backend)
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsInline: true}
	setCurrentTerminalImageCapabilities(m.imageCaps)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	cmd = applyTestCmd(t, &m, cmd)
	if cmd == nil {
		t.Fatal("SessionRestoredEvent should schedule an image protocol redraw")
	}
	msg := cmd()
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		t.Fatalf("command returned %T, want tea.RawMsg", msg)
	}
	seq, ok := raw.Msg.(string)
	if !ok {
		t.Fatalf("tea.RawMsg payload = %T, want string", raw.Msg)
	}
	if seq == "" {
		t.Fatal("expected non-empty image protocol sequence")
	}
	if !strings.Contains(seq, "\x1b_G") {
		t.Fatalf("raw sequence %q does not contain kitty graphics escape", seq)
	}
}

func TestRebuildViewportFromMessagesMarksRestoredToolErrorsAndCancellationsDone(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "tool-error", Name: "WebFetch", Args: []byte(`{"url":"https://missing.example","timeout":40}`)},
				{ID: "tool-cancel", Name: "WebFetch", Args: []byte(`{"url":"https://slow.example"}`)},
				{ID: "tool-pending", Name: "WebFetch", Args: []byte(`{"timeout":40}`)},
			},
		},
		{Role: "tool", ToolCallID: "tool-error", Content: "Model stopped before completing this tool call: context canceled"},
		{Role: "tool", ToolCallID: "tool-cancel", Content: "Cancelled"},
	}}
	m := NewModel(backend)

	m.rebuildViewportFromMessages()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 3 {
		t.Fatalf("len(visibleBlocks()) = %d, want 3", len(blocks))
	}

	var errBlock, cancelBlock, pendingBlock *Block
	for _, b := range blocks {
		if b.Type != BlockToolCall {
			continue
		}
		switch b.ToolID {
		case "tool-error":
			errBlock = b
		case "tool-cancel":
			cancelBlock = b
		case "tool-pending":
			pendingBlock = b
		}
	}
	if errBlock == nil || cancelBlock == nil || pendingBlock == nil {
		t.Fatalf("expected all tool blocks to be rebuilt, got %#v", blocks)
	}
	if !errBlock.ResultDone || errBlock.ResultStatus != agent.ToolResultStatusError {
		t.Fatalf("error block = done:%v status:%q, want done:true status:error", errBlock.ResultDone, errBlock.ResultStatus)
	}
	if !cancelBlock.ResultDone || cancelBlock.ResultStatus != agent.ToolResultStatusCancelled {
		t.Fatalf("cancel block = done:%v status:%q, want done:true status:cancelled", cancelBlock.ResultDone, cancelBlock.ResultStatus)
	}
	if pendingBlock.ResultDone {
		t.Fatalf("pending block ResultDone = true, want false")
	}
}

func TestOpenModelSelectGroupsProvidersAndSearchesProviderOrModel(t *testing.T) {
	backend := &sessionControlAgent{
		availableModels: []agent.ModelOption{
			{ProviderModel: "openai/gpt-5.5", ProviderName: "openai", ModelID: "gpt-5.5", ContextLimit: 1_000_000, OutputLimit: 128_000},
			{ProviderModel: "azure/gpt-5.5", ProviderName: "azure", ModelID: "gpt-5.5", ContextLimit: 1_000_000, OutputLimit: 128_000},
			{ProviderModel: "anthropic/claude-opus-4.7", ProviderName: "anthropic", ModelID: "claude-opus-4.7", ContextLimit: 200_000, OutputLimit: 64_000},
		},
		providerModelRef: "openai/gpt-5.5",
	}
	m := NewModel(backend)

	m.openModelSelect()

	if got := len(m.modelSelect.options); got != 6 {
		t.Fatalf("len(options) = %d, want 6 (3 headers + 3 models)", got)
	}
	if !m.modelSelect.options[0].Header || m.modelSelect.options[0].Provider != "openai" {
		t.Fatalf("first option = %#v, want openai header to preserve configured order", m.modelSelect.options[0])
	}
	if !m.modelSelect.options[2].Header || m.modelSelect.options[2].Provider != "azure" {
		t.Fatalf("third option = %#v, want azure header", m.modelSelect.options[2])
	}
	if !m.modelSelect.options[4].Header || m.modelSelect.options[4].Provider != "anthropic" {
		t.Fatalf("fifth option = %#v, want anthropic header", m.modelSelect.options[4])
	}
	if got := m.modelSelect.table.CursorAt(); got != 1 {
		t.Fatalf("cursor = %d, want current model index 1", got)
	}

	if !m.handleModelSelectSearchKey(modelSelectKey("azure").Key()) {
		t.Fatalf("expected provider search key to be handled")
	}
	if m.modelSelect.searchInput != "azure" {
		t.Fatalf("searchInput = %q, want azure", m.modelSelect.searchInput)
	}
	if got := len(m.modelSelect.options); got != 2 {
		t.Fatalf("len(filtered options) = %d, want 2 (header + model)", got)
	}
	if !m.modelSelect.options[0].Header || m.modelSelect.options[0].Provider != "azure" {
		t.Fatalf("filtered header = %#v, want azure header", m.modelSelect.options[0])
	}
	if got := m.modelSelect.table.CursorAt(); got != 1 {
		t.Fatalf("filtered cursor = %d, want 1", got)
	}

	for range len("azure") {
		if !m.handleModelSelectSearchKey(tea.Key{Code: tea.KeyBackspace}) {
			t.Fatalf("expected backspace to be handled")
		}
	}
	if m.modelSelect.searchInput != "" {
		t.Fatalf("searchInput after clear = %q, want empty", m.modelSelect.searchInput)
	}
	if !m.handleModelSelectSearchKey(modelSelectKey("claude").Key()) {
		t.Fatalf("expected model search key to be handled")
	}
	if got := len(m.modelSelect.options); got != 2 {
		t.Fatalf("len(model filtered options) = %d, want 2", got)
	}
	if !m.modelSelect.options[0].Header || m.modelSelect.options[0].Provider != "anthropic" {
		t.Fatalf("model filtered header = %#v, want anthropic header", m.modelSelect.options[0])
	}
}

func TestBuildModelSelectOptionsKeepsCurrentProviderVisibleWhenFilteredOut(t *testing.T) {
	models := []agent.ModelOption{
		{ProviderModel: "openai/gpt-5.5", ProviderName: "openai", ModelID: "gpt-5.5", ContextLimit: 1_000_000, OutputLimit: 128_000},
		{ProviderModel: "azure/gpt-5.5", ProviderName: "azure", ModelID: "gpt-5.5", ContextLimit: 1_000_000, OutputLimit: 128_000},
		{ProviderModel: "anthropic/claude-opus-4.7", ProviderName: "anthropic", ModelID: "claude-opus-4.7", ContextLimit: 200_000, OutputLimit: 64_000},
	}

	options, cursorRef := buildModelSelectOptions(models, "openai/gpt-5.5", "gpt")
	if got := len(options); got != 4 {
		t.Fatalf("len(options) = %d, want 4", got)
	}
	if !options[0].Header || options[0].Provider != "openai" {
		t.Fatalf("first option = %#v, want openai header", options[0])
	}
	if cursorRef != "openai/gpt-5.5" {
		t.Fatalf("cursorRef = %q, want openai/gpt-5.5", cursorRef)
	}
}

func TestSelectModelAtCursorReturnsSwitchResultMsg(t *testing.T) {
	backend := &sessionControlAgent{
		availableModels: []agent.ModelOption{
			{ProviderModel: "openai/gpt-5.5", ProviderName: "openai", ModelID: "gpt-5.5", ContextLimit: 1_000_000, OutputLimit: 128_000},
			{ProviderModel: "anthropic/claude-opus-4.7", ProviderName: "anthropic", ModelID: "claude-opus-4.7", ContextLimit: 200_000, OutputLimit: 64_000},
		},
		providerModelRef: "openai/gpt-5.5",
		switchModelErr:   nil,
	}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.openModelSelect()
	if m.modelSelect.table == nil {
		t.Fatal("modelSelect table = nil")
	}
	m.modelSelect.table.list.SetCursor(3)

	cmd := m.selectModelAtCursor()
	if cmd == nil {
		t.Fatal("selectModelAtCursor() = nil, want batch command")
	}
	msg := cmd()
	gotResult := false
	switch v := msg.(type) {
	case tea.BatchMsg:
		for _, child := range v {
			if child == nil {
				continue
			}
			if _, ok := child().(modelSwitchResultMsg); ok {
				gotResult = true
				break
			}
		}
	case modelSwitchResultMsg:
		gotResult = true
	default:
		t.Fatalf("cmd() = %T, want tea.BatchMsg or modelSwitchResultMsg", msg)
	}
	if !gotResult {
		t.Fatal("batch missing modelSwitchResultMsg")
	}
	if len(backend.switchModelCalls) != 1 || backend.switchModelCalls[0] != "anthropic/claude-opus-4.7" {
		t.Fatalf("SwitchModel() calls = %+v, want [anthropic/claude-opus-4.7]", backend.switchModelCalls)
	}
}

func TestRebuildBlocksFromFocusedSubAgentMessagesMarksRestoredToolStates(t *testing.T) {
	backend := &focusedMessagesAgent{
		messagesByFocus: map[string][]message.Message{
			"agent-1": {
				{
					Role: "assistant",
					ToolCalls: []message.ToolCall{
						{ID: "sub-error", Name: "WebFetch", Args: []byte(`{"url":"https://missing.example"}`)},
						{ID: "sub-cancel", Name: "WebFetch", Args: []byte(`{"url":"https://slow.example"}`)},
					},
				},
				{Role: "tool", ToolCallID: "sub-error", Content: "Model stopped before completing this tool call: context deadline exceeded"},
				{Role: "tool", ToolCallID: "sub-cancel", Content: "Cancelled"},
			},
		},
		focused: "agent-1",
	}
	m := NewModel(backend)
	m.focusedAgentID = "agent-1"

	blocks := m.rebuildBlocksFromAgentMessages()
	if len(blocks) != 2 {
		t.Fatalf("len(rebuildBlocksFromAgentMessages()) = %d, want 2", len(blocks))
	}
	if !blocks[0].ResultDone || blocks[0].ResultStatus != agent.ToolResultStatusError {
		t.Fatalf("first sub-agent block = done:%v status:%q, want done:true status:error", blocks[0].ResultDone, blocks[0].ResultStatus)
	}
	if !blocks[1].ResultDone || blocks[1].ResultStatus != agent.ToolResultStatusCancelled {
		t.Fatalf("second sub-agent block = done:%v status:%q, want done:true status:cancelled", blocks[1].ResultDone, blocks[1].ResultStatus)
	}
	for i, b := range blocks {
		if !b.SettledAt.IsZero() {
			t.Fatalf("restored sub-agent block %d SettledAt = %v, want zero", i, b.SettledAt)
		}
	}
}

type focusedMessagesAgent struct {
	sessionControlAgent
	focused         string
	messagesByFocus map[string][]message.Message
}

func (f *focusedMessagesAgent) GetMessages() []message.Message {
	msgs := f.messagesByFocus[f.focused]
	return append([]message.Message(nil), msgs...)
}

func (f *focusedMessagesAgent) SwitchFocus(agentID string) { f.focused = agentID }

func (f *focusedMessagesAgent) FocusedAgentID() string { return f.focused }
func (f *focusedMessagesAgent) StartupResumeStatus() (bool, string) {
	return false, ""
}

type sessionControlAgent struct {
	events                  chan agent.AgentEvent
	messages                []message.Message
	messagesByFocus         map[string][]message.Message
	subAgents               []agent.SubAgentInfo
	availableModels         []agent.ModelOption
	availableAgents         []string
	availableRoles          []string
	currentRole             string
	projectRoot             string
	focused                 string
	providerModelRef        string
	providerModelRefByFocus map[string]string
	runningModelRef         string
	runningModelRefByFocus  map[string]string
	runningVariant          string
	runningVariantByFocus   map[string]string
	tokenUsage              message.TokenUsage
	sidebarUsage            analytics.SessionStats
	contextCurrent          int
	contextLimit            int
	todos                   []tools.TodoItem
	sentMessages            []string
	sentMultipart           [][]message.ContentPart
	queuedDraftIDs          []string
	queuedDrafts            [][]message.ContentPart
	removedDraftIDs         []string
	newSessionCalls         int
	resumeCalls             int
	resumeIDs               []string
	resumePending           bool
	startupResumeID         string
	forkMsgIndices          []int
	deleteSessionIDs        []string
	deleteSessionErr        error
	sessionSummary          *agent.SessionSummary
	cancelResult            bool
	cancelCalls             int
	continueCalls           int
	switchModelErr          error
	switchModelCalls        []string
	loopState               agent.LoopState
	loopTarget              string
	loopEnableCalls         int
	loopDisableCalls        int
	loopIteration           int
	loopMaxIterations       int
	loopMaxSet              bool
}

func (s *sessionControlAgent) Events() <-chan agent.AgentEvent { return s.events }
func (s *sessionControlAgent) SendUserMessage(content string) {
	s.sentMessages = append(s.sentMessages, content)
}
func (s *sessionControlAgent) SendUserMessageWithParts(parts []message.ContentPart) {
	cp := append([]message.ContentPart(nil), parts...)
	s.sentMultipart = append(s.sentMultipart, cp)
}
func (s *sessionControlAgent) AppendContextMessage(msg message.Message) {}
func (s *sessionControlAgent) CancelCurrentTurn() bool {
	s.cancelCalls++
	return s.cancelResult
}
func (s *sessionControlAgent) QueuePendingUserDraft(draftID string, parts []message.ContentPart) bool {
	s.queuedDraftIDs = append(s.queuedDraftIDs, draftID)
	cp := append([]message.ContentPart(nil), parts...)
	s.queuedDrafts = append(s.queuedDrafts, cp)
	return true
}
func (s *sessionControlAgent) UpdatePendingUserDraft(draftID string, parts []message.ContentPart) bool {
	return s.QueuePendingUserDraft(draftID, parts)
}
func (s *sessionControlAgent) RemovePendingUserDraft(draftID string) bool {
	s.removedDraftIDs = append(s.removedDraftIDs, draftID)
	return true
}
func (s *sessionControlAgent) ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string) {
}
func (s *sessionControlAgent) ResolveQuestion(answers []string, cancelled bool, requestID string) {
}
func (s *sessionControlAgent) SwitchModel(providerModel string) error {
	s.switchModelCalls = append(s.switchModelCalls, providerModel)
	return s.switchModelErr
}
func (s *sessionControlAgent) AvailableModels() []agent.ModelOption {
	return append([]agent.ModelOption(nil), s.availableModels...)
}
func (s *sessionControlAgent) ProviderModelRef() string {
	if s.providerModelRefByFocus != nil {
		if ref, ok := s.providerModelRefByFocus[s.focused]; ok {
			return ref
		}
	}
	return s.providerModelRef
}
func (s *sessionControlAgent) RunningModelRef() string {
	if s.runningModelRefByFocus != nil {
		if ref, ok := s.runningModelRefByFocus[s.focused]; ok {
			return ref
		}
	}
	if s.runningModelRef != "" {
		return s.runningModelRef
	}
	return s.ProviderModelRef()
}
func (s *sessionControlAgent) RunningVariant() string {
	if s.runningVariantByFocus != nil {
		if variant, ok := s.runningVariantByFocus[s.focused]; ok {
			return variant
		}
	}
	return s.runningVariant
}
func (s *sessionControlAgent) GetSubAgents() []agent.SubAgentInfo {
	return append([]agent.SubAgentInfo(nil), s.subAgents...)
}
func (s *sessionControlAgent) GetMessages() []message.Message {
	if s.messagesByFocus != nil {
		if msgs, ok := s.messagesByFocus[s.focused]; ok {
			return append([]message.Message(nil), msgs...)
		}
	}
	return append([]message.Message(nil), s.messages...)
}
func (s *sessionControlAgent) SwitchFocus(agentID string) { s.focused = agentID }
func (s *sessionControlAgent) FocusedAgentID() string     { return s.focused }
func (s *sessionControlAgent) StartupResumeStatus() (bool, string) {
	return s.resumePending, s.startupResumeID
}
func (s *sessionControlAgent) ContinueFromContext()                  { s.continueCalls++ }
func (s *sessionControlAgent) RemoveLastMessage()                    {}
func (s *sessionControlAgent) GetTokenUsage() message.TokenUsage     { return s.tokenUsage }
func (s *sessionControlAgent) GetUsageStats() analytics.SessionStats { return analytics.SessionStats{} }
func (s *sessionControlAgent) GetSidebarUsageStats() analytics.SessionStats {
	return s.sidebarUsage
}
func (s *sessionControlAgent) GetContextStats() (current, limit int) {
	return s.contextCurrent, s.contextLimit
}
func (s *sessionControlAgent) GetContextMessageCount() int                               { return 0 }
func (s *sessionControlAgent) KeyStats() (available, total int)                          { return 0, 0 }
func (s *sessionControlAgent) CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot { return nil }
func (s *sessionControlAgent) ProxyInUseForRef(ref string) bool                          { return false }
func (s *sessionControlAgent) ProjectRoot() string                                       { return s.projectRoot }
func (s *sessionControlAgent) CurrentRole() string                                       { return s.currentRole }
func (s *sessionControlAgent) LoopKeepsMainBusy() bool                                   { return false }
func (s *sessionControlAgent) CurrentLoopState() agent.LoopState                         { return s.loopState }
func (s *sessionControlAgent) CurrentLoopTarget() string                                 { return s.loopTarget }
func (s *sessionControlAgent) CurrentLoopIteration() int {
	if s.loopIteration == 0 {
		return 1
	}
	return s.loopIteration
}
func (s *sessionControlAgent) CurrentLoopMaxIterations() int {
	if s.loopMaxSet {
		return 0
	}
	if s.loopMaxIterations == 0 {
		return 10
	}
	return s.loopMaxIterations
}
func (s *sessionControlAgent) EnableLoopMode(target string) {
	s.loopEnableCalls++
	s.loopTarget = target
}
func (s *sessionControlAgent) DisableLoopMode() { s.loopDisableCalls++ }
func (s *sessionControlAgent) ListSessionSummaries() ([]agent.SessionSummary, error) {
	return nil, nil
}
func (s *sessionControlAgent) GetSessionSummary() *agent.SessionSummary { return s.sessionSummary }
func (s *sessionControlAgent) DeleteSession(sessionID string) error {
	s.deleteSessionIDs = append(s.deleteSessionIDs, sessionID)
	return s.deleteSessionErr
}
func (s *sessionControlAgent) ExportSession(format, path string) {}
func (s *sessionControlAgent) ResumeSession()                    { s.resumeCalls++ }
func (s *sessionControlAgent) ResumeSessionID(sessionID string) {
	s.resumeIDs = append(s.resumeIDs, sessionID)
}
func (s *sessionControlAgent) NewSession() { s.newSessionCalls++ }
func (s *sessionControlAgent) ForkSession(msgIndex int) {
	s.forkMsgIndices = append(s.forkMsgIndices, msgIndex)
}
func (s *sessionControlAgent) ExecutePlan(planPath, agentName string) {
}
func (s *sessionControlAgent) AvailableAgents() []string {
	return append([]string(nil), s.availableAgents...)
}
func (s *sessionControlAgent) SwitchRole(role string) { s.currentRole = role }
func (s *sessionControlAgent) AvailableRoles() []string {
	return append([]string(nil), s.availableRoles...)
}
func (s *sessionControlAgent) InvokedSkills() []*skill.Meta { return nil }
func (s *sessionControlAgent) GetTodos() []tools.TodoItem {
	return append([]tools.TodoItem(nil), s.todos...)
}

func (s *sessionControlAgent) IsCompactionRunning() bool { return false }
func (s *sessionControlAgent) CancelCompaction() bool    { return false }

func TestForkSessionEventBackfillsTextInlinePastesAndImages(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	parts := []message.ContentPart{
		{Type: "text", Text: "[Pasted text #1 +59 lines]", DisplayText: "[Pasted text #1 +59 lines]"},
		{Type: "image", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}, FileName: "screenshot.png"},
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, cmd)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert after ForkSessionEvent", m.mode)
	}
	if got := m.input.Value(); got != "[Pasted text #1 +59 lines]" {
		t.Fatalf("input value = %q, want inline paste placeholder", got)
	}
	if !m.input.HasInlinePastes() {
		t.Fatal("expected inline paste metadata to be restored for large paste part")
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("len(attachments) = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "screenshot.png" {
		t.Fatalf("attachment filename = %q, want screenshot.png", got)
	}
	if got := m.attachments[0].MimeType; got != "image/png" {
		t.Fatalf("attachment mime = %q, want image/png", got)
	}
}

// TestForkSessionEventBackfillsPlainText verifies that a ForkSessionEvent
// with a plain text part (no inline paste, no images) loads the text into
// the composer and switches to insert mode.
func TestForkSessionEventBackfillsPlainText(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	parts := []message.ContentPart{{Type: "text", Text: "hello world"}}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, cmd)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if got := m.input.Value(); got != "hello world" {
		t.Fatalf("input value = %q, want 'hello world'", got)
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("len(attachments) = %d, want 0 for text-only fork", got)
	}
}

// TestForkSessionEventBackfillsImagesOnly verifies that a ForkSessionEvent
// containing only image parts (no text) loads the images into attachments
// and switches to insert mode.
func TestForkSessionEventBackfillsImagesOnly(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	parts := []message.ContentPart{
		{Type: "image", MimeType: "image/jpeg", Data: []byte{0xff, 0xd8}, FileName: "photo.jpg"},
		{Type: "image", MimeType: "image/png", Data: []byte{0x89, 'P'}, FileName: "diagram.png"},
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, cmd)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if got := len(m.attachments); got != 2 {
		t.Fatalf("len(attachments) = %d, want 2", got)
	}
	if got := m.attachments[0].FileName; got != "photo.jpg" {
		t.Fatalf("attachments[0].FileName = %q, want photo.jpg", got)
	}
	if got := m.attachments[1].FileName; got != "diagram.png" {
		t.Fatalf("attachments[1].FileName = %q, want diagram.png", got)
	}
}

// TestForkSessionEventRestoresAtLiteralButNotInjectedFilePayload verifies
// the documented backfill limitation: plain text containing @path literals
// is restored verbatim, but injected <file ...> payloads (file-ref content)
// are not reversed back to @path tokens. The display logic strips file-ref
// text, so the composer input is left empty for such payloads.
func TestForkSessionEventRestoresAtLiteralButNotInjectedFilePayload(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	// Case 1: plain text with @path literal should be restored as-is
	parts := []message.ContentPart{{Type: "text", Text: "look at @main.go for context"}}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, cmd)

	if got := m.input.Value(); got != "look at @main.go for context" {
		t.Fatalf("input value = %q, want plain text with @main.go literal restored", got)
	}

	// Case 2: injected file payload (IsFileRefContent) is NOT restored to
	// the composer — the display logic cannot reverse <file ...> back to the
	// original @path editing state. This is the documented limitation.
	m2 := NewModel(backend)
	m2.mode = ModeNormal
	payloadText := "<file path=\"main.go\">\npackage main\n</file>"
	parts2 := []message.ContentPart{{Type: "text", Text: payloadText}}

	cmd2 := m2.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts2}})
	applyTestCmd(t, &m2, cmd2)

	if got := m2.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty (file-ref payload cannot be reconstructed into @path editing state)", got)
	}
	if m2.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert (should still switch even with empty input)", m2.mode)
	}
}
