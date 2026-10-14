package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestDeferredStartupTranscriptSearchRevealExpandsToolCallContent(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := range startupTranscriptWindowMinBlocks + 110 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "read",
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
	if toolBlock.ToolName != "read" {
		t.Fatalf("matched tool = %q, want read", toolBlock.ToolName)
	}
	if toolBlock.Collapsed {
		t.Fatal("search should reveal collapsed Read tool content")
	}
	plain := terminaltext.StripANSI(strings.Join(toolBlock.Render(m.viewport.width, ""), "\n"))
	if !strings.Contains(plain, "needle line") {
		t.Fatalf("rendered search match = %q, want visible needle line", plain)
	}
	viewportPlain := terminaltext.StripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex(), m.searchCurrentInnerOffset(), m.search.State.Query))
	if !strings.Contains(viewportPlain, "needle line") {
		t.Fatalf("viewport render should include revealed search content, got %q", viewportPlain)
	}
}

func TestDeferredStartupTranscriptSearchRevealMaterializesColdCompactToolOutput(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := range startupTranscriptWindowMinBlocks + 110 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-shell-cold-1",
			Name: "shell",
			Args: []byte(`{"command":"go test ./internal/tui -count=1","timeout":120}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-shell-cold-1", Content: "ok chord/internal/tui 0.711s\nneedle output\nPASS"},
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
		if block != nil && block.ToolID == "call-shell-cold-1" {
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
	viewportPlain := terminaltext.StripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex(), m.searchCurrentInnerOffset(), m.search.State.Query))
	if !strings.Contains(viewportPlain, "needle output") {
		t.Fatalf("viewport render should include materialized cold compact tool output, got %q", viewportPlain)
	}
}

func TestDeferredStartupTranscriptSearchSkipsInvisibleThinkingBlocks(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110)
	for i := range startupTranscriptWindowMinBlocks + 110 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages, message.Message{
		Role:           "assistant",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: ""}},
		ToolCalls: []message.ToolCall{{
			ID:   "call-patch-1",
			Name: tools.NameEdit,
			Args: []byte(`{"path":"internal/tui/needle-output.go","patch":"@@\n-before\n+after\n"}`),
		}},
	})
	messages = append(messages, message.Message{Role: "tool", ToolCallID: "call-patch-1", Content: "success"})
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for invisible thinking search test")
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("needle-output.go")
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
	for i := range startupTranscriptWindowMinBlocks + 110 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-dump-artifact-read-1",
			Name: "read",
			Args: []byte(`{"path":"~/.local/state/chord/logs/tui-dumps/tui-dump-20260403.log","limit":160,"offset":0}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-dump-artifact-read-1", Content: "1\tfind artifact line\n2\tdebug dump line"},
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-real-search-1",
			Name: "shell",
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
	viewportPlain := terminaltext.StripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex(), m.searchCurrentInnerOffset(), m.search.State.Query))
	if !strings.Contains(viewportPlain, "real find result") {
		t.Fatalf("viewport render should include non-diagnostic search content, got %q", viewportPlain)
	}
	if strings.Contains(viewportPlain, "find artifact line") && !strings.Contains(viewportPlain, "real find result") {
		t.Fatalf("viewport render should focus the non-diagnostic match, got %q", viewportPlain)
	}
}

func TestDeferredStartupTranscriptSearchRevealExpandsCompactToolOutput(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+110+2)
	for i := range startupTranscriptWindowMinBlocks + 110 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("filler-%03d", i)})
	}
	messages = append(messages,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{
			ID:   "call-shell-1",
			Name: "shell",
			Args: []byte(`{"command":"python3 - <<'PY'\nprint('alpha')\nPY","description":"Run scripted search","timeout":120}`),
		}}},
		message.Message{Role: "tool", ToolCallID: "call-shell-1", Content: "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\niota\nkappa\nneedle output\nomega"},
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
	viewportPlain := terminaltext.StripANSI(m.viewport.Render("", nil, m.searchCurrentBlockIndex(), m.searchCurrentInnerOffset(), m.search.State.Query))
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
	if toolBlock.ToolName != "shell" {
		t.Fatalf("matched tool = %q, want shell", toolBlock.ToolName)
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

	plain := terminaltext.StripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "/grep [1/1]") {
		t.Fatalf("status bar should show search session before ESC, got %q", plain)
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after ESC = %+v, want cleared", m.search.State)
	}
}

func TestSearchPillShowsAcrossModesWhileSearchSessionActive(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}, {BlockIndex: 1}}
	m.search.State.Current = 0

	m.mode = ModeNormal
	if plain := terminaltext.StripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [1/2]") {
		t.Fatalf("normal-mode status bar should show search pill, got %q", plain)
	}
	m.mode = ModeInsert
	if plain := terminaltext.StripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [1/2]") {
		t.Fatalf("insert-mode status bar should show search pill, got %q", plain)
	}
	m.mode = ModeSearch
	if plain := terminaltext.StripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [1/2]") {
		t.Fatalf("search-mode status bar should show search pill, got %q", plain)
	}
}

func TestDeferredStartupTranscriptSearchNavigatesWithoutHydrate(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
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

func TestDeferredStartupTranscriptSearchNavigatesToBlockIDZero(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: message.RoleAssistant, Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for first-block search")
	}
	if got := m.startupDeferredTranscript.allBlocks[0].ID; got != 0 {
		t.Fatalf("first deferred block ID = %d, want 0", got)
	}

	cmd = m.handleNormalKey(modelSelectKey("/"))
	applyTestCmd(t, &m, cmd)
	m.search.Input.SetValue("message-000")
	cmd = m.handleSearchKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	applyTestCmd(t, &m, cmd)

	if !m.hasDeferredStartupTranscript() {
		t.Fatal("first-block search should not hydrate the deferred transcript")
	}
	if state := m.startupDeferredTranscript; state.windowStart != 0 {
		t.Fatalf("deferred window after first-block search = [%d,%d), want start 0", state.windowStart, state.windowEnd)
	}
	if got := m.viewport.GetBlockAtOffset(); got == nil || got.ID != 0 || got.Content != "message-000" {
		t.Fatalf("block at offset after first-block search = %#v, want block 0", got)
	}
	if m.focusedBlockID != 0 {
		t.Fatalf("focusedBlockID after first-block search = %d, want 0", m.focusedBlockID)
	}

	cmd = m.handleNormalKey(modelSelectKey("n"))
	applyTestCmd(t, &m, cmd)
	if got := m.viewport.GetBlockAtOffset(); got == nil || got.ID != 0 {
		t.Fatalf("block at offset after wrapped next search = %#v, want block 0", got)
	}
}

func TestDeferredStartupTranscriptDirectoryNavigatesWithoutHydrate(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
		messages = append(messages, message.Message{Role: "assistant", Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for deferred directory test")
	}

	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
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

func TestDeferredStartupTranscriptMetadataSupportsSearchAndDirectoryAfterSpill(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
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

func TestDeferredStartupTranscriptSearchesVisibleAssistantMarkdown(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{ID: 1, Type: BlockAssistant, Content: "visible **needle** phrase"}
	m := NewModelWithSize(nil, 80, 24)
	m.startupDeferredTranscript = &startupDeferredTranscriptState{
		allBlocks: []*Block{block},
		blockMeta: buildStartupDeferredBlockMeta([]*Block{block}, 80),
	}

	matches := m.deferredStartupTranscriptSearch("visible needle")
	if len(matches) != 1 || matches[0].BlockID != block.ID {
		t.Fatalf("deferred search matches = %#v, want one visible Assistant Markdown match", matches)
	}
}

func TestDeferredStartupTranscriptSearchAndDirectorySurviveViewportResize(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+200)
	for i := range startupTranscriptWindowMinBlocks + 200 {
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
