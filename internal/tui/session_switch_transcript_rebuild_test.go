package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestIdleEventFinalizesStreamingAssistantWithoutDuplicateCommittedBlock(t *testing.T) {
	stubTUITicks(t)
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
	if plain := strings.TrimSpace(terminaltext.StripANSI(blocks[0].Content)); !strings.Contains(plain, "hello world") {
		t.Fatalf("assistant content = %q, want committed text", plain)
	}
}

func TestTranscriptRebuildReplacesStreamingAssistantInsteadOfDuplicatingIt(t *testing.T) {
	stubTUITicks(t)
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
	if plain := strings.TrimSpace(terminaltext.StripANSI(blocks[0].Content)); !strings.Contains(plain, "partial reply") {
		t.Fatalf("assistant content = %q, want committed text", plain)
	}
}

func TestStreamRollbackRemovesStreamingAssistantBeforeCommittedRebuild(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "temporary reply"}})
	applyTestCmd(t, &m, cmd)
	if got := len(m.viewport.visibleBlocks()); got != 1 {
		t.Fatalf("streaming block count = %d, want 1", got)
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{Reason: "retry", AgentID: ""}})
	if cmd == nil {
		t.Fatal("rollback with reason should return follow-up command")
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("block count after rollback = %d, want 0", got)
	}

	backend.messages = []message.Message{{Role: "assistant", Content: "final reply"}}
	m.rebuildViewportFromMessagesWithReason("rollback_then_rebuild")
	blocks := m.viewport.visibleBlocks()
	if got := len(blocks); got != 1 {
		t.Fatalf("block count after rollback+rebuild = %d, want 1", got)
	}
	if plain := strings.TrimSpace(terminaltext.StripANSI(blocks[0].Content)); !strings.Contains(plain, "final reply") {
		t.Fatalf("assistant content = %q, want final committed text", plain)
	}
}

func TestSessionRestoreRebuildDoesNotDuplicateExistingStreamingAssistant(t *testing.T) {
	stubTUITicks(t)
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
	if plain := strings.TrimSpace(terminaltext.StripANSI(blocks[0].Content)); !strings.Contains(plain, "restored reply") {
		t.Fatalf("assistant content = %q, want restored committed text", plain)
	}
}

func TestVeryShortAssistantPrefixBeforeToolCallIsDroppedAcrossFinalizeAndRebuild(t *testing.T) {
	stubTUITicks(t)
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	// Build a completed tool card first; this is the context where a very-short
	// assistant prefix ("Okay"/"Sure"/"Let") should be treated as an orphan.
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-1",
		Name:     "read",
		ArgsJSON: `{"file_path":"a.go"}`,
	}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-1",
		Name:     "read",
		ArgsJSON: `{"file_path":"a.go"}`,
		Result:   "ok",
		Status:   message.ToolStatusSuccess,
	}})
	applyTestCmd(t, &m, cmd)

	// Stream a very-short assistant prefix, then start the next tool call.
	// ToolCallStartEvent finalizes the assistant block; the orphan prefix must
	// be dropped instead of persisting as a standalone assistant card.
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "Okay"}})
	applyTestCmd(t, &m, cmd)
	cmd = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-2",
		Name:     "read",
		ArgsJSON: `{"file_path":"b.go"}`,
	}})
	applyTestCmd(t, &m, cmd)

	for _, b := range m.viewport.visibleBlocks() {
		if b.Type != BlockAssistant {
			continue
		}
		if strings.Contains(terminaltext.StripANSI(b.Content), "Okay") {
			t.Fatalf("unexpected orphan short assistant prefix kept before rebuild: %q", terminaltext.StripANSI(b.Content))
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
	plain := strings.TrimSpace(terminaltext.StripANSI(blocks[0].Content))
	if !strings.Contains(plain, "final response") {
		t.Fatalf("assistant content after rebuild = %q, want committed final response", plain)
	}
	if strings.Contains(plain, "Okay") {
		t.Fatalf("assistant content after rebuild should not retain orphan short prefix, got %q", plain)
	}
}

func TestSessionRestoredEventRebuildsDoneMarkdownAndDelegateLink(t *testing.T) {
	backend := &sessionControlAgent{
		messages: []message.Message{
			{
				Role: "assistant",
				ToolCalls: []message.ToolCall{{
					ID:   "done-1",
					Name: "done",
					Args: json.RawMessage(`{"report":"## Summary\n- shipped\n- verified"}`),
				}},
			},
			{Role: "tool", ToolCallID: "done-1", Content: "Done approved"},
			{
				Role: "assistant",
				ToolCalls: []message.ToolCall{{
					ID:   "delegate-1",
					Name: "delegate",
					Args: json.RawMessage(`{"description":"review tests","agent_type":"reviewer"}`),
				}},
			},
			{Role: "tool", ToolCallID: "delegate-1", Content: `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`},
		},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.beginSessionSwitch("resume", "123")

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	var doneBlock, delegateBlock *Block
	for _, block := range blocks {
		switch block.ToolName {
		case "done":
			doneBlock = block
		case "delegate":
			delegateBlock = block
		}
	}
	if doneBlock == nil {
		t.Fatal("expected restored Done block")
	}
	if delegateBlock == nil {
		t.Fatal("expected restored Delegate block")
	}
	plain := terminaltext.StripANSI(strings.Join(doneBlock.Render(100, ""), "\n"))
	for _, want := range []string{"Summary", "• shipped", "• verified"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected restored Done block to contain %q, got:\n%s", want, plain)
		}
	}
	if got := delegateBlock.LinkedAgentID; got != "reviewer-2" {
		t.Fatalf("delegate LinkedAgentID = %q, want reviewer-2", got)
	}
	if got := delegateBlock.LinkedTaskID; got != "adhoc-7" {
		t.Fatalf("delegate LinkedTaskID = %q, want adhoc-7", got)
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

func TestRebuildViewportFromMessagesRestoresReadCollapsedSummary(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "tool-read", Name: "read", Args: []byte(`{"path":"internal/tui/input.go","limit":1,"offset":358}`)},
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
	if block.ToolName != "read" || !block.ResultDone {
		t.Fatalf("restored block = %#v, want completed Read block", block)
	}
	if !block.SettledAt.IsZero() {
		t.Fatalf("restored block SettledAt = %v, want zero", block.SettledAt)
	}
	if !block.Collapsed {
		t.Fatalf("restored Read block should stay collapsed (summary state), got %#v", block)
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "read internal/tui/input.go") {
		t.Fatalf("expected restored Read header, got:\n%s", plain)
	}
	if strings.Contains(plain, "359") {
		t.Fatalf("collapsed restored Read should not render body lines, got:\n%s", plain)
	}
}

func TestSessionRestoredEventUpdatesWorkingDirAndShowsRelativeToolPath(t *testing.T) {
	backend := &sessionControlAgent{
		messages: []message.Message{{
			Role: "assistant",
			ToolCalls: []message.ToolCall{{
				ID:   "tool-read",
				Name: "read",
				Args: []byte(`{"path":"/repo-b/internal/tui/input.go","limit":1,"offset":358}`),
			}},
		}, {Role: "tool", ToolCallID: "tool-read", Content: "   359\t\n"}},
		contentRoot: "/repo-b",
		workDir:     "/repo-b",
	}
	m := NewModelWithSize(backend, 100, 30)
	m.workingDir = "/repo-a"
	m.viewport.SetWorkingDir(m.workingDir)
	m.beginSessionSwitch("resume", "123")

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)

	if got := m.workingDir; got != "/repo-b" {
		t.Fatalf("workingDir after session restore = %q, want /repo-b", got)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	plain := terminaltext.StripANSI(strings.Join(blocks[0].Render(100, ""), "\n"))
	if !strings.Contains(plain, "read internal/tui/input.go") {
		t.Fatalf("expected restored Read header relative to new project root, got:\n%s", plain)
	}
	if strings.Contains(plain, "/repo-b/internal/tui/input.go") {
		t.Fatalf("expected restored Read header not to show absolute path from new project root, got:\n%s", plain)
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

	plain := terminaltext.StripANSI(m.renderStatusBar())
	if strings.Contains(plain, statusBarIdleLabel()) {
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
	stubTUITicks(t)
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
	if batch, ok := msg.(tea.BatchMsg); ok {
		msg = nil
		for _, child := range batch {
			if child == nil {
				continue
			}
			childMsg := child()
			if loaded, ok := childMsg.(inlineImagesLoadedMsg); ok {
				if next := m.handleInlineImagesLoaded(loaded); next != nil {
					childMsg = next()
				}
			}
			if raw, ok := childMsg.(tea.RawMsg); ok {
				msg = raw
				break
			}
		}
		if msg == nil {
			t.Fatalf("command batch %#v has no tea.RawMsg child", batch)
		}
	}
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

func TestRebuildViewportFromMessagesRestoresRejectedDoneIntoSingleToolCard(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{{
				ID:   "done-1",
				Name: tools.NameDone,
				Args: []byte(`{"report":"## Completion status\ndone\n\n**Verification**: passed"}`),
			}},
		},
		{Role: "tool", ToolCallID: "done-1", Content: "Done rejected: update docs as needed, verify loop exit behavior, and commit all changes"},
	}}
	m := NewModel(backend)

	m.rebuildViewportFromMessages()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.Type != BlockToolCall || block.ToolName != tools.NameDone || block.ToolID != "done-1" {
		t.Fatalf("restored block = %#v, want single Done tool card", block)
	}
	if !block.ResultDone {
		t.Fatal("restored Done block should be marked complete")
	}
	if !strings.Contains(block.ResultContent, "Done rejected:") {
		t.Fatalf("ResultContent = %q, want rejected Done content", block.ResultContent)
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(plain, "✗ done") {
		t.Fatalf("rendered Done card = %q, want rejected Done failure header", plain)
	}
	if !strings.Contains(plain, "update docs as needed") {
		t.Fatalf("rendered Done card = %q, want visible rejected reason text", plain)
	}
}

func TestRebuildViewportFromMessagesMarksRestoredToolErrorsAndCancellationsDone(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "tool-error", Name: "web_fetch", Args: []byte(`{"url":"https://missing.example","timeout_ms":40000}`)},
				{ID: "tool-cancel", Name: "web_fetch", Args: []byte(`{"url":"https://slow.example"}`)},
				{ID: "tool-pending", Name: "web_fetch", Args: []byte(`{"timeout_ms":40000}`)},
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
	if !errBlock.ResultDone || errBlock.ResultStatus != message.ToolStatusError {
		t.Fatalf("error block = done:%v status:%q, want done:true status:error", errBlock.ResultDone, errBlock.ResultStatus)
	}
	if !cancelBlock.ResultDone || cancelBlock.ResultStatus != message.ToolStatusCancelled {
		t.Fatalf("cancel block = done:%v status:%q, want done:true status:cancelled", cancelBlock.ResultDone, cancelBlock.ResultStatus)
	}
	if pendingBlock.ResultDone {
		t.Fatalf("pending block ResultDone = true, want false")
	}
}

func TestRebuildBlocksFromFocusedSubAgentMessagesMarksRestoredToolStates(t *testing.T) {
	backend := &focusedMessagesAgent{
		messagesByFocus: map[string][]message.Message{
			"agent-1": {
				{
					Role: "assistant",
					ToolCalls: []message.ToolCall{
						{ID: "sub-error", Name: "web_fetch", Args: []byte(`{"url":"https://missing.example"}`)},
						{ID: "sub-cancel", Name: "web_fetch", Args: []byte(`{"url":"https://slow.example"}`)},
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
	if !blocks[0].ResultDone || blocks[0].ResultStatus != message.ToolStatusError {
		t.Fatalf("first sub-agent block = done:%v status:%q, want done:true status:error", blocks[0].ResultDone, blocks[0].ResultStatus)
	}
	if !blocks[1].ResultDone || blocks[1].ResultStatus != message.ToolStatusCancelled {
		t.Fatalf("second sub-agent block = done:%v status:%q, want done:true status:cancelled", blocks[1].ResultDone, blocks[1].ResultStatus)
	}
	for i, b := range blocks {
		if !b.SettledAt.IsZero() {
			t.Fatalf("restored sub-agent block %d SettledAt = %v, want zero", i, b.SettledAt)
		}
	}
}
