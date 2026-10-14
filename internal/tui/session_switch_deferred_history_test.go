package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestSessionRestoredRebuildSchedulesStartupDeferredTranscriptPreheat(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+32)
	for i := range startupTranscriptWindowMinBlocks + 32 {
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
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
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
	leftIdx := max(state.windowStart-startupDeferredTranscriptPreheatHalo, 0)
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
	state.blockMeta[leftIdx].LineCounts = nil
	if rightIdx >= 0 {
		state.blockMeta[rightIdx].LineCounts = nil
	}

	preheatMsg := next().(startupDeferredPreheatTickMsg)
	cmd = m.handleStartupDeferredTranscriptPreheat(preheatMsg)
	if cmd == nil {
		t.Fatal("preheat tick should reschedule while deferred transcript remains active")
	}
	if state.blockMeta[leftIdx].LineCounts == nil || startupDeferredBlockLineCount(state.blockMeta[leftIdx], m.viewport.width) <= 0 {
		t.Fatalf("left halo metadata not preheated: %#v", state.blockMeta[leftIdx])
	}
	if rightIdx >= 0 {
		if state.blockMeta[rightIdx].LineCounts == nil || startupDeferredBlockLineCount(state.blockMeta[rightIdx], m.viewport.width) <= 0 {
			t.Fatalf("right halo metadata not preheated: %#v", state.blockMeta[rightIdx])
		}
	}
}

func TestDeferredWindowSwitchRestartsPreheatForNewHalo(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
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
	leftIdx := max(state.windowStart-startupDeferredTranscriptPreheatHalo, 0)
	rightIdx := state.windowEnd + min(startupDeferredTranscriptPreheatHalo, len(state.allBlocks)-state.windowEnd) - 1
	if rightIdx < state.windowEnd {
		rightIdx = -1
	}
	if rightIdx < 0 {
		t.Fatal("top window should have a right halo to preheat")
	}
	state.blockMeta[rightIdx].LineCounts = nil
	if leftIdx < state.windowStart {
		state.blockMeta[leftIdx].LineCounts = nil
	}

	_ = next // original scheduled tick should now be stale
	cmd = m.handleStartupDeferredTranscriptPreheat(startupDeferredPreheatTickMsg{generation: m.startupDeferredPreheatGeneration})
	if cmd == nil {
		t.Fatal("restarted preheat tick should reschedule while deferred transcript remains active")
	}
	if state.blockMeta[rightIdx].LineCounts == nil || startupDeferredBlockLineCount(state.blockMeta[rightIdx], m.viewport.width) <= 0 {
		t.Fatalf("right halo metadata not preheated after window switch: %#v", state.blockMeta[rightIdx])
	}
}

func TestStartupRestoredDeferredTranscriptUsesUpdatedWorkDirForRelativeToolPath(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := range startupTranscriptWindowMinBlocks + 110 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-read-root-1",
			Name: "read",
			Args: []byte(`{"path":"/repo-b/internal/tui/app.go","limit":20,"offset":0}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-read-root-1", Content: "1\tneedle line\n2\tomega line"},
	)
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages, contentRoot: "/repo-b", workDir: "/repo-b"}
	m := NewModelWithSize(backend, 120, 24)
	m.workingDir = "/repo-a"
	m.viewport.SetWorkingDir(m.workingDir)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for project-root path test")
	}
	if got := m.workingDir; got != "/repo-b" {
		t.Fatalf("workingDir after startup restore = %q, want /repo-b", got)
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("needle line")
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("search should find deferred Read tool content")
	}
	if !m.maybeScrollToSearchMatch(match, "search_enter") {
		t.Fatal("maybeScrollToSearchMatch should succeed for deferred Read tool match")
	}
	block := m.viewport.GetFocusedBlock(match.BlockID)
	if block == nil {
		t.Fatal("expected focused Read block after search reveal")
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(m.viewport.width, ""), "\n"))
	if !strings.Contains(plain, "read internal/tui/app.go") {
		t.Fatalf("expected deferred restored Read header relative to updated project root, got:\n%s", plain)
	}
	if strings.Contains(plain, "/repo-b/internal/tui/app.go") {
		t.Fatalf("expected deferred restored Read header not to show absolute path, got:\n%s", plain)
	}
}

func TestApplyStartupDeferredTranscriptWindowPreservesColdBlocksForLaterMaterialization(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := range startupTranscriptWindowMinBlocks + 110 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-shell-window-1",
			Name: "shell",
			Args: []byte(`{"command":"go test ./internal/tui -count=1","timeout":120}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-shell-window-1", Content: "ok chord/internal/tui 0.711s\nneedle output\nPASS"},
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
		if block != nil && block.ToolID == "call-shell-window-1" {
			targetIndex = i
			break
		}
	}
	if targetIndex < 0 {
		t.Fatal("expected deferred shell block in state")
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

func TestDeferredStartupTranscriptSendDraftExtendsVisibleTailWindow(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+120)
	for i := range startupTranscriptWindowMinBlocks + 120 {
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
	if len(blocks) != before {
		t.Fatalf("len(visibleBlocks()) after draft = %d, want fixed tail window %d", len(blocks), before)
	}
	last := blocks[len(blocks)-1]
	if last.Type != BlockUser || last.Content != "new user message" {
		t.Fatalf("last visible block = %#v, want appended user block", last)
	}
	userBlockID := last.ID

	thinking := &Block{ID: m.nextBlockID, Type: BlockThinking, Content: "next turn thinking"}
	m.nextBlockID++
	m.appendViewportBlock(thinking)
	blocks = m.viewport.visibleBlocks()
	if len(blocks) != before+1 {
		t.Fatalf("len(visibleBlocks()) after thinking = %d, want %d", len(blocks), before+1)
	}
	if got := m.viewport.GetFocusedBlock(userBlockID); got == nil || got.Type != BlockUser || got.Content != "new user message" {
		t.Fatalf("user block after thinking append = %#v, want retained user block", got)
	}
	last = blocks[len(blocks)-1]
	if last.ID != thinking.ID || last.Type != BlockThinking {
		t.Fatalf("last visible block after thinking append = %#v, want thinking block %d", last, thinking.ID)
	}
	if state := m.startupDeferredTranscript; state == nil || state.windowEnd != len(state.allBlocks) {
		t.Fatalf("startupDeferredTranscript after draft = %+v, want windowEnd synced to allBlocks", state)
	}
}

func TestDeferredStartupTranscriptForkKeepsOriginalVisibleMsgIndexAfterSendDraft(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+40)
	for i := range startupTranscriptWindowMinBlocks + 40 {
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

func TestStartupRestoredLargeTranscriptUsesWindowedTailUntilHistoryNeeded(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := range startupTranscriptWindowMinBlocks + 24 {
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

func TestDeferredStartupTranscriptLiveAppendOutsideTailKeepsPagingContiguous(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+120)
	for i := range startupTranscriptWindowMinBlocks + 120 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for live append paging test")
	}

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing after gg")
	}
	startBefore, endBefore := state.windowStart, state.windowEnd
	visibleBefore := len(m.viewport.visibleBlocks())

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "live tail reply"}})
	applyTestCmd(t, &m, cmd)

	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing after live append")
	}
	if state.windowStart != startBefore || state.windowEnd != endBefore {
		t.Fatalf("deferred window changed after live append outside tail: got [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, startBefore, endBefore)
	}
	if got := len(m.viewport.visibleBlocks()); got != visibleBefore+1 {
		t.Fatalf("len(visibleBlocks()) after live append = %d, want %d", got, visibleBefore+1)
	}

	m.viewport.ScrollToBottom()
	m.maybePageStartupDeferredTranscriptWindow(1, "page_down_after_live_append")
	state = m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup deferred transcript state missing after page down")
	}
	wantStart := endBefore
	wantEnd := min(len(state.allBlocks), endBefore+startupTranscriptTailBlocks)
	if state.windowStart != wantStart || state.windowEnd != wantEnd {
		t.Fatalf("deferred window after page down = [%d,%d), want [%d,%d)", state.windowStart, state.windowEnd, wantStart, wantEnd)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != wantEnd-wantStart {
		t.Fatalf("len(visibleBlocks()) after page down = %d, want %d", len(blocks), wantEnd-wantStart)
	}
	if blocks[0].Content != fmt.Sprintf("message-%03d", wantStart) {
		t.Fatalf("first block after page down = %q, want message-%03d", blocks[0].Content, wantStart)
	}
	if blocks[len(blocks)-1].ID == m.currentAssistantBlock.ID {
		t.Fatalf("last block after page down = current live assistant block %d, want contiguous historical page", m.currentAssistantBlock.ID)
	}
}

func TestDeferredStartupTranscriptWindowSwitchKeepsStreamingAssistantContent(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := range startupTranscriptWindowMinBlocks + 24 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for streaming assistant window switch test")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "partial "}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "reply"}})
	applyTestCmd(t, &m, cmd)
	if m.currentAssistantBlock == nil {
		t.Fatal("expected streaming assistant block before window switch")
	}
	streamID := m.currentAssistantBlock.ID

	m.handleNormalKey(modelSelectKey("g"))
	m.handleNormalKey(modelSelectKey("g"))
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("gg should keep deferred startup transcript active")
	}
	m.handleNormalKey(modelSelectKey("G"))

	if m.currentAssistantBlock == nil {
		t.Fatal("expected streaming assistant block after switching back to tail")
	}
	if m.currentAssistantBlock.ID != streamID {
		t.Fatalf("streaming assistant block id after gg/G = %d, want %d", m.currentAssistantBlock.ID, streamID)
	}
	if got := m.currentAssistantBlock.Content; got != "partial reply" {
		t.Fatalf("streaming assistant content after gg/G = %q, want %q", got, "partial reply")
	}
	block := m.viewport.GetFocusedBlock(streamID)
	if block == nil {
		t.Fatal("expected streaming assistant block in viewport after gg/G")
	}
	if got := block.Content; got != "partial reply" {
		t.Fatalf("viewport streaming assistant content after gg/G = %q, want %q", got, "partial reply")
	}
}

func TestDeferredStartupTranscriptWindowSwitchKeepsLiveToolResult(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := range startupTranscriptWindowMinBlocks + 24 {
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
		Name:     "read",
		ArgsJSON: argsJSON,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "live-read-1",
		Name:     "read",
		ArgsJSON: argsJSON,
		Result:   "1\talpha line\n2\tbeta line\n3\tgamma line\n4\tomega line",
		Status:   message.ToolStatusSuccess,
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
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := range startupTranscriptWindowMinBlocks + 24 {
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

func TestFocusedAgentSwitchWindowsLargeTranscriptsAfterDeferredStartup(t *testing.T) {
	stubTUITicks(t)
	mainMessages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := range startupTranscriptWindowMinBlocks + 24 {
		mainMessages = append(mainMessages, message.Message{Role: "assistant", Content: fmt.Sprintf("main-%03d", i)})
	}
	subMessages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+24)
	for i := range startupTranscriptWindowMinBlocks + 24 {
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
	for i := range 20 {
		m.viewport.AppendBlock(&Block{
			ID:      1000 + i,
			Type:    BlockAssistant,
			AgentID: "agent-1",
			Content: fmt.Sprintf("partial-sub-%02d", i),
		})
	}

	m.setFocusedAgent("agent-1")
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != startupTranscriptTailBlocks {
		t.Fatalf("len(visibleBlocks()) in subagent view = %d, want %d", len(blocks), startupTranscriptTailBlocks)
	}
	wantSubStart := fmt.Sprintf("sub-%03d", len(subMessages)-startupTranscriptTailBlocks)
	if blocks[0].Content != wantSubStart {
		t.Fatalf("subagent first block = %q, want windowed transcript tail start %q", blocks[0].Content, wantSubStart)
	}
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("large focus switch transcript should remain windowed")
	}

	m.setFocusedAgent("")
	blocks = m.viewport.visibleBlocks()
	if len(blocks) != startupTranscriptTailBlocks {
		t.Fatalf("len(visibleBlocks()) after returning to main = %d, want %d", len(blocks), startupTranscriptTailBlocks)
	}
	wantMainStart := fmt.Sprintf("main-%03d", len(mainMessages)-startupTranscriptTailBlocks)
	if blocks[0].Content != wantMainStart {
		t.Fatalf("main first block = %q, want windowed transcript tail start %q", blocks[0].Content, wantMainStart)
	}
}

func TestDeferredStartupTranscriptRetentionShrinksViewportHotBudgetUntilHydrate(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
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

func TestDeferredStartupTranscriptMetadataReferencesArchiveClones(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 32))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for metadata ownership test")
	}
	if len(state.blockMeta) != len(state.allBlocks) {
		t.Fatalf("len(blockMeta) = %d, want %d", len(state.blockMeta), len(state.allBlocks))
	}
	for i, block := range state.allBlocks {
		if block == nil {
			continue
		}
		if state.blockMeta[i].block != block {
			t.Fatalf("blockMeta[%d].block = %p, want archive clone %p", i, state.blockMeta[i].block, block)
		}
	}
}

func TestDeferredStartupTranscriptSyncRebindsMetadataToReplacedClone(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 32))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for sync rebind test")
	}
	idx := state.windowStart
	if idx < 0 || idx >= len(state.allBlocks) || len(m.viewport.blocks) == 0 {
		t.Fatalf("deferred window start %d is out of range for %d archive blocks", idx, len(state.allBlocks))
	}
	live := m.viewport.blocks[0]
	if state.allBlocks[idx] != live {
		t.Fatalf("archive[%d] = %p, want the viewport block %p", idx, state.allBlocks[idx], live)
	}

	previous := state.allBlocks[idx]
	m.syncStartupDeferredTranscriptBlock(live)
	if state.allBlocks[idx] == previous {
		t.Fatal("sync should replace the archive block object")
	}
	if got := state.blockMeta[idx].block; got != state.allBlocks[idx] {
		t.Fatalf("blockMeta[%d].block = %p, want replaced archive clone %p", idx, got, state.allBlocks[idx])
	}
}

func TestStartupDeferredTranscriptPreheatStopsWhenHaloIsWarm(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 32))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	state := m.startupDeferredTranscript
	if state == nil {
		t.Fatal("startup transcript should remain deferred for preheat stop test")
	}
	if state.windowStart <= 0 {
		t.Fatalf("windowStart = %d, want a left halo to preheat", state.windowStart)
	}

	cold := m.handleStartupDeferredTranscriptPreheat(startupDeferredPreheatTickMsg{generation: m.startupDeferredPreheatGeneration})
	if cold == nil {
		t.Fatal("a cold halo should keep the preheat timer armed")
	}
	warm := m.handleStartupDeferredTranscriptPreheat(startupDeferredPreheatTickMsg{generation: m.startupDeferredPreheatGeneration})
	if warm != nil {
		t.Fatal("a warm halo must not re-arm the preheat timer")
	}
}

func TestDeferredTranscriptPreheatRestartsOnViewportWidthChange(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d %s", i, strings.Repeat("payload ", 32))})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if m.startupDeferredTranscript == nil {
		t.Fatal("startup transcript should remain deferred for resize preheat test")
	}
	previous := m.viewport.width
	if cmd := m.restartDeferredTranscriptPreheatAfterResize(previous); cmd != nil {
		t.Fatal("an unchanged viewport width must not restart the preheat")
	}

	generation := m.startupDeferredPreheatGeneration
	m.viewport.SetSize(previous+20, m.viewport.height)
	preheatCmd := m.restartDeferredTranscriptPreheatAfterResize(previous)
	if preheatCmd == nil {
		t.Fatal("a viewport width change should restart the preheat")
	}
	if m.startupDeferredPreheatGeneration == generation {
		t.Fatalf("preheat generation = %d, want it advanced past %d", m.startupDeferredPreheatGeneration, generation)
	}
	if _, ok := preheatCmd().(startupDeferredPreheatTickMsg); !ok {
		t.Fatal("restarted preheat should schedule a preheat tick")
	}
}

// A mid-session /resume delivers SessionRestoredEvent without a startup
// restore pending. Its transcript must get the same deferred windowing as a
// startup restore: windowing only on startup reasons would synchronously
// render every block of the resumed session before the next frame.
func TestMidSessionRestoreWindowsDeferredTranscript(t *testing.T) {
	stubTUITicks(t)
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+130)
	for i := range startupTranscriptWindowMinBlocks + 130 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: false, messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	// Hydration clears the deferred state; the next restore rebuild must
	// re-establish windowing.
	m.startupDeferredTranscript = nil
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("mid-session restore should window the deferred transcript")
	}
	state := m.startupDeferredTranscript
	if state.windowEnd != len(state.allBlocks) || state.windowStart != len(state.allBlocks)-startupTranscriptTailBlocks {
		t.Fatalf("window after mid-session restore = [%d,%d), want the tail window", state.windowStart, state.windowEnd)
	}
}
