package tui

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func sendNormalCountPrefix(m *Model, count int) {
	if count < 0 {
		count = -count
	}
	for _, ch := range fmt.Sprintf("%d", count) {
		_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: string(ch), Code: ch}))
	}
}

func assertDeferredWindowInvariant(t *testing.T, m *Model) {
	t.Helper()
	state := m.startupDeferredTranscript
	if state == nil {
		return
	}
	if state.windowStart < 0 || state.windowEnd < state.windowStart || state.windowEnd > len(state.allBlocks) {
		t.Fatalf("invalid deferred window bounds [%d,%d) for total %d", state.windowStart, state.windowEnd, len(state.allBlocks))
	}
	visible := m.viewport.visibleBlocks()
	want := state.windowEnd - state.windowStart
	if len(visible) != want {
		t.Fatalf("len(visibleBlocks()) = %d, want %d from window [%d,%d)", len(visible), want, state.windowStart, state.windowEnd)
	}
	seen := make(map[int]struct{}, len(visible))
	for i, block := range visible {
		if block == nil {
			t.Fatalf("visible block %d is nil in deferred window [%d,%d)", i, state.windowStart, state.windowEnd)
		}
		if _, ok := seen[block.ID]; ok {
			t.Fatalf("duplicate visible block id %d in deferred window [%d,%d)", block.ID, state.windowStart, state.windowEnd)
		}
		seen[block.ID] = struct{}{}
		expected := state.allBlocks[state.windowStart+i]
		if expected == nil {
			t.Fatalf("source deferred block %d is nil", state.windowStart+i)
		}
		if block.ID != expected.ID {
			t.Fatalf("visible block[%d] id = %d, want source id %d at absolute index %d", i, block.ID, expected.ID, state.windowStart+i)
		}
	}
	if m.focusedBlockID >= 0 {
		if _, ok := seen[m.focusedBlockID]; !ok {
			t.Fatalf("focusedBlockID %d not present in visible deferred window [%d,%d)", m.focusedBlockID, state.windowStart, state.windowEnd)
		}
	}
}

func TestDeferredStartupTranscriptPageUpSwitchesWindowBeforeExactTopOffset(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+140)
	for i := range startupTranscriptWindowMinBlocks + 140 {
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
	for i := range startupTranscriptWindowMinBlocks + 140 {
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
	for i := range startupTranscriptWindowMinBlocks + 140 {
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

func TestDeferredStartupTranscriptHiddenToolResultDoesNotDuplicateAfterMouseWheelWindowSwitch(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+140)
	for i := range startupTranscriptWindowMinBlocks + 140 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for hidden tool result test")
	}

	argsJSON := `{"path":"internal/tui/app_agent_events.go","limit":1}`
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "live-read-hidden-1",
		Name:     "read",
		ArgsJSON: argsJSON,
	}})
	if block, ok := m.viewport.FindBlockByToolID("live-read-hidden-1"); !ok || block == nil {
		t.Fatal("expected pending tool block in tail window before mouse-wheel switch")
	}

	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing")
	}
	startBefore, endBefore := state.windowStart, state.windowEnd
	m.viewport.offset = startupDeferredPageUpSwitchThreshold(m.viewport.height)

	updated, wheelCmd := m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if wheelCmd == nil {
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
	visibleBefore := len(model.viewport.visibleBlocks())
	if _, ok := model.viewport.FindBlockByToolID("live-read-hidden-1"); ok {
		t.Fatal("tail tool block should be hidden after switching to the previous deferred window")
	}

	_ = model.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "live-read-hidden-1",
		Name:     "read",
		ArgsJSON: argsJSON,
		Result:   "ok",
		Status:   message.ToolStatusSuccess,
	}})

	if got := len(model.viewport.visibleBlocks()); got != visibleBefore {
		t.Fatalf("len(visibleBlocks()) after hidden tool result = %d, want unchanged %d", got, visibleBefore)
	}
	if _, ok := model.viewport.FindBlockByToolID("live-read-hidden-1"); ok {
		t.Fatal("hidden tool result should update deferred source instead of appending a duplicate visible card")
	}

	model.handleNormalKey(modelSelectKey("G"))
	blocks := model.viewport.visibleBlocks()
	count := 0
	for _, block := range blocks {
		if block != nil && block.ToolID == "live-read-hidden-1" {
			count++
			if !block.ResultDone {
				t.Fatal("expected hidden tool block to be completed after returning to tail window")
			}
			if got := strings.TrimSpace(block.ResultContent); got != "ok" {
				t.Fatalf("hidden tool block result = %q, want %q", got, "ok")
			}
		}
	}
	if count != 1 {
		t.Fatalf("tool blocks for live-read-hidden-1 after returning to tail = %d, want 1", count)
	}
}

func TestDeferredStartupTranscriptCountedJumpBuildsTargetWindow(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
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
	for i := range startupTranscriptWindowMinBlocks + 200 {
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
	if got := m.startupDeferredTranscript.windowStart; got != max(0, start-startupTranscriptTailBlocks) {
		t.Fatalf("scrolling above top of deferred window should page to previous window, got start=%d want %d", got, max(0, start-startupTranscriptTailBlocks))
	}
	if got := m.startupDeferredTranscript.windowEnd; got != start {
		t.Fatalf("previous deferred window end = %d, want %d", got, start)
	}

	midStart := m.startupDeferredTranscript.windowStart
	midEnd := m.startupDeferredTranscript.windowEnd
	m.viewport.ScrollToBottom()
	m.maybePageStartupDeferredTranscriptWindow(1, "page_down_test")
	if got := m.startupDeferredTranscript.windowStart; got != midEnd {
		t.Fatalf("page down at bottom of deferred window should page to next window, got start=%d want %d", got, midEnd)
	}
	if got := m.startupDeferredTranscript.windowEnd; got != min(len(messages), midStart+2*startupTranscriptTailBlocks) {
		t.Fatalf("next deferred window end = %d, want %d", got, min(len(messages), midStart+2*startupTranscriptTailBlocks))
	}
}

func TestDeferredStartupTranscriptScrollWindowSwitchUsesContiguousPaging(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for contiguous paging test")
	}
	if got, want := state.windowStart, len(messages)-startupTranscriptTailBlocks; got != want {
		t.Fatalf("initial deferred window start = %d, want %d", got, want)
	}

	m.viewport.offset = startupDeferredPageUpSwitchThreshold(m.viewport.height)
	cmd = m.handleNormalKey(modelSelectKey("k"))
	applyTestCmd(t, &m, cmd)
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred after scroll-up switch")
	}
	wantStart := len(messages) - 2*startupTranscriptTailBlocks
	wantEnd := len(messages) - startupTranscriptTailBlocks
	if state.windowStart != wantStart || state.windowEnd != wantEnd {
		t.Fatalf("deferred window after scroll-up = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantStart, wantEnd)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != startupTranscriptTailBlocks {
		t.Fatalf("len(visibleBlocks()) after scroll-up switch = %d, want %d", len(blocks), startupTranscriptTailBlocks)
	}
	if blocks[0].Content != fmt.Sprintf("message-%03d", wantStart) {
		t.Fatalf("first block after contiguous page-up = %q, want message-%03d", blocks[0].Content, wantStart)
	}
	if blocks[len(blocks)-1].Content != fmt.Sprintf("message-%03d", wantEnd-1) {
		t.Fatalf("last block after contiguous page-up = %q, want message-%03d", blocks[len(blocks)-1].Content, wantEnd-1)
	}
	focused := m.viewport.GetBlockAtOffset()
	if focused == nil {
		t.Fatal("expected block at current viewport offset after contiguous page-up")
	}
	if focused.Content == fmt.Sprintf("message-%03d", wantStart-1) || focused.Content == fmt.Sprintf("message-%03d", wantEnd) {
		t.Fatalf("focused block after contiguous page-up = %#v, want block from new window range [%03d,%03d)", focused, wantStart, wantEnd)
	}
}

func TestDeferredStartupTranscriptPrevBlockAtTopDoesNotJumpToStaleAnchor(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for top hydration test")
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	focused := m.viewport.GetBlockAtOffset()
	if focused == nil || focused.Content != "message-000" {
		t.Fatalf("focused block after gg = %#v, want message-000", focused)
	}

	cmd = m.handleNormalKey(modelSelectKey("k"))
	applyTestCmd(t, &m, cmd)
	focused = m.viewport.GetBlockAtOffset()
	if focused == nil || focused.Content != "message-000" {
		t.Fatalf("focused block after k at top = %#v, want message-000", focused)
	}
	if m.hasDeferredStartupTranscript() {
		t.Fatal("k at the top deferred window should hydrate full transcript")
	}
}

func TestDeferredStartupTranscriptNextBlockFocusIsMonotonicAcrossWindows(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for monotonic next-block test")
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	if m.focusedBlockID < 0 {
		t.Fatal("gg should focus the first deferred block")
	}
	focused := m.viewport.GetFocusedBlock(m.focusedBlockID)
	if focused == nil || focused.Content != "message-000" {
		t.Fatalf("focused block after gg = %#v, want message-000", focused)
	}

	prev := -1
	for step := range 140 {
		cmd = m.handleNormalKey(modelSelectKey("j"))
		applyTestCmd(t, &m, cmd)
		if m.focusedBlockID < 0 {
			t.Fatalf("focusedBlockID after step %d < 0", step+1)
		}
		focused = m.viewport.GetFocusedBlock(m.focusedBlockID)
		if focused == nil {
			t.Fatalf("focused block after step %d = nil", step+1)
		}
		var cur int
		if _, err := fmt.Sscanf(focused.Content, "message-%03d", &cur); err != nil {
			t.Fatalf("parse focused content %q: %v", focused.Content, err)
		}
		if cur <= prev {
			t.Fatalf("focused block sequence regressed at step %d: got %d after %d", step+1, cur, prev)
		}
		prev = cur
	}
}

func TestDeferredStartupTranscriptFocusRestoreTailPreventsMouseWheelPagingPastBottom(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+120)
	for i := range startupTranscriptWindowMinBlocks + 120 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for focus-restore tail test")
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	if state := m.startupDeferredTranscript; state == nil || state.windowStart != 0 {
		t.Fatalf("deferred state after gg = %#v, want top window", state)
	}
	m.handleNormalKey(modelSelectKey("G"))
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after G")
	}
	wantTailStart := len(messages) - startupTranscriptTailBlocks
	if state.windowStart != wantTailStart || state.windowEnd != len(messages) {
		t.Fatalf("tail window after G = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantTailStart, len(messages))
	}
	if !m.viewport.atBottom() {
		t.Fatal("viewport should be at bottom before blur")
	}

	_ = m.handleBlurMsg()
	if !m.deferredResumeTailOnFocus {
		t.Fatal("blur should remember deferred tail-follow state")
	}

	if !m.applyStartupDeferredTranscriptWindow(0, startupTranscriptTailBlocks, "test_stale_window") {
		t.Fatal("expected manual switch to stale historical window for focus restore test")
	}
	m.viewport.ScrollToBottom()
	if state := m.startupDeferredTranscript; state == nil || state.windowEnd == len(state.allBlocks) {
		t.Fatalf("test setup should leave deferred state away from tail, got %#v", state)
	}

	cmd = m.handleFocusMsg()
	applyTestCmd(t, &m, cmd)
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after focus restore")
	}
	if state.windowStart != wantTailStart || state.windowEnd != len(messages) {
		t.Fatalf("tail window after focus restore = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantTailStart, len(messages))
	}
	if !m.viewport.atBottom() {
		t.Fatal("viewport should remain at bottom after focus restore")
	}

	beforeStart, beforeEnd := state.windowStart, state.windowEnd
	m.applyWheelScrollDelta(1)
	state = m.startupDeferredTranscript
	if state.windowStart != beforeStart || state.windowEnd != beforeEnd {
		t.Fatalf("mouse wheel down at restored tail changed window to [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, beforeStart, beforeEnd)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		t.Fatal("visibleBlocks after wheel down at tail should remain non-empty")
	}
	if blocks[len(blocks)-1].Content != fmt.Sprintf("message-%03d", len(messages)-1) {
		t.Fatalf("last visible block after wheel down at tail = %q, want newest message", blocks[len(blocks)-1].Content)
	}
}

func TestDeferredStartupTranscriptMouseWheelDownAt197CardTailDoesNotJumpToWindowStart(t *testing.T) {
	messages := make([]message.Message, 0, 197)
	for i := range 197 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 8))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for 197-card mouse-wheel regression test")
	}
	m.handleNormalKey(modelSelectKey("G"))

	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after G")
	}
	wantStart := len(messages) - startupTranscriptTailBlocks
	if state.windowStart != wantStart || state.windowEnd != len(messages) {
		t.Fatalf("tail window after G = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantStart, len(messages))
	}
	if !m.viewport.atBottom() {
		t.Fatal("viewport should be at bottom before wheel down")
	}

	beforeOffset := m.viewport.offset
	beforeTop := m.viewport.GetBlockAtOffset()
	if beforeTop == nil {
		t.Fatal("top block before wheel down is nil")
	}
	m.applyWheelScrollDelta(mouseWheelScrollStep)

	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after wheel down at tail")
	}
	if state.windowStart != wantStart || state.windowEnd != len(messages) {
		t.Fatalf("tail window after wheel down = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantStart, len(messages))
	}
	if m.viewport.offset != beforeOffset {
		t.Fatalf("offset after wheel down at tail = %d, want unchanged %d", m.viewport.offset, beforeOffset)
	}
	afterTop := m.viewport.GetBlockAtOffset()
	if afterTop == nil {
		t.Fatal("top block after wheel down is nil")
	}
	if afterTop.ID != beforeTop.ID {
		t.Fatalf("top block after wheel down at tail = %q, want unchanged %q", afterTop.Content, beforeTop.Content)
	}
}

func TestDeferredStartupTranscriptCountedNextBlockSaturatesAtLastCard(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+40)
	for i := range startupTranscriptWindowMinBlocks + 40 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for counted next-block saturation test")
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	if m.focusedBlockID < 0 {
		t.Fatal("gg should focus first block")
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "0", Code: '0'}))
	cmd = m.handleNormalKey(modelSelectKey("j"))
	applyTestCmd(t, &m, cmd)

	if m.focusedBlockID < 0 {
		t.Fatal("focusedBlockID after 20j should remain valid")
	}
	focused := m.viewport.GetFocusedBlock(m.focusedBlockID)
	if focused == nil {
		t.Fatal("focused block after 20j should remain visible")
	}
	if focused.Content != "message-020" {
		t.Fatalf("focused block after 20j = %q, want message-020", focused.Content)
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	cmd = m.handleNormalKey(modelSelectKey("j"))
	applyTestCmd(t, &m, cmd)

	focused = m.viewport.GetFocusedBlock(m.focusedBlockID)
	if focused == nil {
		t.Fatal("focused block after saturating 99j should remain visible")
	}
	if focused.Content != "message-119" {
		t.Fatalf("focused block after 20j + 99j = %q, want message-119", focused.Content)
	}
	state := m.startupDeferredTranscript
	if state == nil || state.windowStart > 119 || state.windowEnd <= 119 {
		t.Fatalf("deferred state after 20j + 99j = %#v, want window containing index 119", state)
	}
}

func TestDeferredStartupTranscriptCountedPrevNextRemainContiguousAcrossWindows(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for counted cross-window navigation test")
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	if m.focusedBlockID < 0 {
		t.Fatal("gg should focus first block")
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "1", Code: '1'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "0", Code: '0'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "0", Code: '0'}))
	cmd = m.handleNormalKey(modelSelectKey("j"))
	applyTestCmd(t, &m, cmd)

	focused := m.viewport.GetFocusedBlock(m.focusedBlockID)
	if focused == nil || focused.Content != "message-100" {
		t.Fatalf("focused block after 100j = %#v, want message-100", focused)
	}
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("deferred state missing after 100j")
	}
	if state.windowStart > 100 || state.windowEnd <= 100 {
		t.Fatalf("window after 100j = [%d,%d), want to contain index 100", state.windowStart, state.windowEnd)
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "1", Code: '1'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "0", Code: '0'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "0", Code: '0'}))
	cmd = m.handleNormalKey(modelSelectKey("k"))
	applyTestCmd(t, &m, cmd)

	focused = m.viewport.GetFocusedBlock(m.focusedBlockID)
	if focused == nil || focused.Content != "message-000" {
		t.Fatalf("focused block after 100k = %#v, want message-000", focused)
	}
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("reaching the first block exactly should keep deferred mode active until the next upward move")
	}
	cmd = m.handleNormalKey(modelSelectKey("k"))
	applyTestCmd(t, &m, cmd)
	focused = m.viewport.GetBlockAtOffset()
	if focused == nil || focused.Content != "message-000" {
		t.Fatalf("focused block after extra k at top = %#v, want message-000", focused)
	}
	if m.hasDeferredStartupTranscript() {
		t.Fatal("extra k beyond the first block should hydrate and exit deferred mode")
	}
}

func TestDeferredStartupTranscriptCountedLineScrollSaturatesAtTail(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+120)
	for i := range startupTranscriptWindowMinBlocks + 120 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 8))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for counted line scroll test")
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	applyTestCmd(t, &m, cmd)

	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("deferred state missing after counted down scroll")
	}
	if state.windowEnd != len(messages) {
		t.Fatalf("window end after counted down scroll = %d, want %d tail end", state.windowEnd, len(messages))
	}
	if !m.viewport.atBottom() {
		t.Fatal("viewport should saturate at bottom after counted down scroll")
	}
}

func TestDeferredStartupTranscriptLiveAppendAndFocusRestoreKeepMouseWheelTailBounded(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+220)
	for i := range startupTranscriptWindowMinBlocks + 220 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 8))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for live-append focus-restore test")
	}

	m.handleNormalKey(modelSelectKey("G"))
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after G")
	}
	if !m.viewport.atBottom() {
		t.Fatal("viewport should be at bottom after G")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "live tail reply"}})
	applyTestCmd(t, &m, cmd)
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after live append")
	}
	if state.windowEnd != len(state.allBlocks) {
		t.Fatalf("tail window after live append should remain synced to allBlocks, got [%d,%d) total=%d", state.windowStart, state.windowEnd, len(state.allBlocks))
	}
	wantStart, wantEnd := state.windowStart, state.windowEnd

	_ = m.handleBlurMsg()
	cmd = m.handleFocusMsg()
	applyTestCmd(t, &m, cmd)
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after focus restore")
	}
	if state.windowStart != wantStart || state.windowEnd != wantEnd {
		t.Fatalf("tail window after focus restore = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantStart, wantEnd)
	}

	m.applyWheelScrollDelta(-2 * m.viewport.height)
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after wheel-up from restored tail")
	}
	if state.windowStart != wantStart || state.windowEnd != wantEnd {
		t.Fatalf("tail window changed after wheel-up from restored tail: got [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantStart, wantEnd)
	}

	steps := 0
	for !m.viewport.atBottom() {
		prevOffset := m.viewport.offset
		m.applyWheelScrollDelta(1)
		steps++
		state = m.startupDeferredTranscript
		if state == nil {
			t.Fatal("startup deferred transcript missing during wheel-down after live append/focus restore")
		}
		if state.windowStart != wantStart || state.windowEnd != wantEnd {
			t.Fatalf("tail window changed during wheel-down after live append/focus restore at step %d: got [%d,%d), want [%d,%d)", steps, state.windowStart, state.windowEnd, wantStart, wantEnd)
		}
		if m.viewport.offset < prevOffset {
			t.Fatalf("wheel-down regressed offset from %d to %d", prevOffset, m.viewport.offset)
		}
		if m.viewport.offset == prevOffset && !m.viewport.atBottom() {
			t.Fatalf("wheel-down made no progress at step %d before reaching bottom; offset=%d", steps, m.viewport.offset)
		}
		if steps > 200 {
			t.Fatalf("unexpectedly needed more than 200 wheel-down steps to return to bottom; offset=%d", m.viewport.offset)
		}
	}
}

func TestDeferredStartupTranscriptGScrollsToTailLineWhenLastBlockIsTall(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+80)
	for i := range startupTranscriptWindowMinBlocks + 79 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	messages = append(messages, message.Message{Role: "assistant", Content: strings.Repeat("tail line\n", 80)})
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 18)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should be deferred")
	}

	m.handleNormalKey(modelSelectKey("G"))
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after G")
	}
	if state.windowEnd != len(state.allBlocks) {
		t.Fatalf("window end after G = %d, want tail end %d", state.windowEnd, len(state.allBlocks))
	}
	if !m.viewport.atBottom() {
		t.Fatalf("viewport should be at bottom after G, offset=%d total=%d height=%d", m.viewport.offset, m.viewport.TotalLines(), m.viewport.height)
	}
	lastBlock := state.allBlocks[len(state.allBlocks)-1]
	if m.focusedBlockID != lastBlock.ID {
		t.Fatalf("focusedBlockID after G = %d, want last block %d", m.focusedBlockID, lastBlock.ID)
	}
	lastStart, ok := m.viewport.LineOffsetForBlockID(lastBlock.ID)
	if !ok {
		t.Fatal("last block should be present in tail window")
	}
	lastBlockHeight := m.viewport.TotalLines() - lastStart
	if lastBlockHeight > m.viewport.height && m.viewport.offset <= lastStart {
		t.Fatalf("G stopped at last card top offset=%d, want below last card start %d for tall card height=%d viewport=%d", m.viewport.offset, lastStart, lastBlockHeight, m.viewport.height)
	}
}

func TestDeferredStartupTranscriptMouseWheelRoundTripFromTailStaysInTailWindow(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+220)
	for i := range startupTranscriptWindowMinBlocks + 220 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 8))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for mouse-wheel tail round-trip test")
	}

	m.handleNormalKey(modelSelectKey("G"))
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after G")
	}
	beforeStart, beforeEnd := state.windowStart, state.windowEnd
	if !m.viewport.atBottom() {
		t.Fatal("viewport should be at bottom after G")
	}

	m.applyWheelScrollDelta(-2 * m.viewport.height)
	if m.viewport.offset == 0 {
		t.Fatal("setup failed: mouse wheel up should move above bottom within tail window")
	}
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after wheel up")
	}
	if state.windowStart != beforeStart || state.windowEnd != beforeEnd {
		t.Fatalf("tail window changed after wheel-up within tail window: got [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, beforeStart, beforeEnd)
	}

	steps := 0
	for !m.viewport.atBottom() {
		prevOffset := m.viewport.offset
		m.applyWheelScrollDelta(1)
		steps++
		if m.viewport.offset < prevOffset {
			t.Fatalf("wheel-down regressed offset from %d to %d", prevOffset, m.viewport.offset)
		}
		if state = m.startupDeferredTranscript; state == nil {
			t.Fatal("startup deferred transcript missing during wheel-down round-trip")
		}
		if state.windowStart != beforeStart || state.windowEnd != beforeEnd {
			t.Fatalf("tail window changed during wheel-down round-trip at step %d: got [%d,%d), want [%d,%d)", steps, state.windowStart, state.windowEnd, beforeStart, beforeEnd)
		}
		if m.viewport.offset == prevOffset && !m.viewport.atBottom() {
			t.Fatalf("wheel-down made no progress at step %d before reaching bottom; offset=%d", steps, m.viewport.offset)
		}
		if steps > 200 {
			t.Fatalf("unexpectedly needed more than 200 wheel-down steps to return to bottom; offset=%d", m.viewport.offset)
		}
	}
}

func TestDeferredStartupTranscriptJumpBottomThenPartialScrollUpAndDownStaysInTailWindow(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+220)
	for i := range startupTranscriptWindowMinBlocks + 220 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 8))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for jump-bottom partial scroll test")
	}

	m.handleNormalKey(modelSelectKey("G"))
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after G")
	}
	wantStart := len(messages) - startupTranscriptTailBlocks
	if state.windowStart != wantStart || state.windowEnd != len(messages) {
		t.Fatalf("tail window after G = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantStart, len(messages))
	}

	for range 2 {
		m.viewport.ScrollUp(m.viewport.height)
	}
	beforeStart, beforeEnd := state.windowStart, state.windowEnd
	beforeOffset := m.viewport.offset
	if beforeOffset == 0 {
		t.Fatal("setup failed: offset after two page-up moves should be above top within tail window")
	}

	m.viewport.ScrollDown(m.viewport.height)
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript missing after partial scroll down")
	}
	if state.windowStart != beforeStart || state.windowEnd != beforeEnd {
		t.Fatalf("tail window changed after scrolling down within same tail window: got [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, beforeStart, beforeEnd)
	}
	if m.viewport.offset <= beforeOffset {
		t.Fatalf("viewport offset after page-down = %d, want > %d", m.viewport.offset, beforeOffset)
	}

	pageDowns := 0
	for !m.viewport.atBottom() {
		prevOffset := m.viewport.offset
		m.viewport.ScrollDown(m.viewport.height)
		pageDowns++
		if m.viewport.offset <= prevOffset {
			break
		}
	}
	if state = m.startupDeferredTranscript; state == nil {
		t.Fatal("startup deferred transcript missing after paging back to bottom")
	}
	if state.windowStart != beforeStart || state.windowEnd != beforeEnd {
		t.Fatalf("tail window changed while paging back to bottom: got [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, beforeStart, beforeEnd)
	}
	if !m.viewport.atBottom() {
		t.Fatalf("viewport should reach bottom again within same tail window after %d page-downs; offset=%d", pageDowns, m.viewport.offset)
	}
}

func TestDeferredStartupTranscriptRandomCountedNavigationMatchesTheory(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+220)
	for i := range startupTranscriptWindowMinBlocks + 220 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for random counted navigation test")
	}
	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	if m.focusedBlockID < 0 {
		t.Fatal("gg should focus first block")
	}
	assertDeferredWindowInvariant(t, &m)

	theoretical := 0
	rng := rand.New(rand.NewSource(42))
	for step := range 60 {
		count := rng.Intn(180) + 1
		dir := 1
		key := "j"
		if rng.Intn(2) == 0 {
			dir = -1
			key = "k"
		}
		sendNormalCountPrefix(&m, count)
		cmd = m.handleNormalKey(modelSelectKey(key))
		applyTestCmd(t, &m, cmd)
		theoretical += dir * count
		if theoretical < 0 {
			theoretical = 0
		}
		if theoretical >= len(messages) {
			theoretical = len(messages) - 1
		}
		focused := m.viewport.GetFocusedBlock(m.focusedBlockID)
		if focused == nil {
			t.Fatalf("step %d: focused block missing after %d%s", step, count, key)
		}
		want := fmt.Sprintf("message-%03d", theoretical)
		if focused.Content != want {
			t.Fatalf("step %d after %d%s: focused block = %q, want %q", step, count, key, focused.Content, want)
		}
		assertDeferredWindowInvariant(t, &m)
	}
}

func TestDeferredStartupTranscriptCountedNavigationMaintainsWindowInvariant(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+260)
	for i := range startupTranscriptWindowMinBlocks + 260 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for window invariant test")
	}
	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	assertDeferredWindowInvariant(t, &m)

	ops := []struct {
		count int
		key   string
	}{
		{1, "j"}, {47, "j"}, {48, "j"}, {49, "j"}, {120, "j"},
		{1, "k"}, {47, "k"}, {48, "k"}, {49, "k"}, {120, "k"},
	}
	for _, op := range ops {
		sendNormalCountPrefix(&m, op.count)
		cmd = m.handleNormalKey(modelSelectKey(op.key))
		applyTestCmd(t, &m, cmd)
		assertDeferredWindowInvariant(t, &m)
	}
}

func TestNormalModeCountedBoundaryNavigationSaturatesAtEdges(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	for i := range 10 {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: fmt.Sprintf("message-%03d", i)})
	}
	m.viewport.offset = 0
	m.setFocusedBlockFromViewport()
	if m.focusedBlockID < 0 {
		t.Fatal("expected first block to be focused in normal mode")
	}

	sendNormalCountPrefix(&m, 20)
	cmd := m.handleNormalKey(modelSelectKey("j"))
	applyTestCmd(t, &m, cmd)
	focused := m.viewport.GetFocusedBlock(m.focusedBlockID)
	if focused == nil || focused.Content != "message-009" {
		t.Fatalf("focused block after 20j in normal mode = %#v, want message-009", focused)
	}

	sendNormalCountPrefix(&m, 20)
	cmd = m.handleNormalKey(modelSelectKey("k"))
	applyTestCmd(t, &m, cmd)
	focused = m.viewport.GetFocusedBlock(m.focusedBlockID)
	if focused == nil || focused.Content != "message-000" {
		t.Fatalf("focused block after 20k in normal mode = %#v, want message-000", focused)
	}
}
