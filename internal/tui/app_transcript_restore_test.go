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
)

func TestMessagesToBlocksRendersLoopNoticeAsStatusCard(t *testing.T) {
	msgs := []message.Message{{Role: "user", Content: "LOOP\n\nTarget:\n- finish current task", Kind: "loop_notice"}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockStatus {
		t.Fatalf("block type = %v, want BlockStatus", blocks[0].Type)
	}
	if blocks[0].StatusTitle != "LOOP" {
		t.Fatalf("StatusTitle = %q, want %q", blocks[0].StatusTitle, "LOOP")
	}
	if !blocks[0].Collapsed {
		t.Fatal("restored loop notices must start collapsed")
	}
	collapsed := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(collapsed, "LOOP") || !strings.Contains(collapsed, "▸") || strings.Contains(collapsed, "Target:") {
		t.Fatalf("rendered status card = %q, want a collapsed LOOP badge with the body hidden", collapsed)
	}
	if !blocks[0].ToggleAtWidth(80) || blocks[0].Collapsed {
		t.Fatal("toggling must expand the restored notice")
	}
	plain := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "  Target:") || !strings.Contains(plain, "  • finish current task") {
		t.Fatalf("rendered status card = %q, want indented loop body", plain)
	}
}

func TestMessagesToBlocksRestoresMainFollowUpNotifyWithoutMessageID(t *testing.T) {
	content := "[follow_up] continue with option B"
	msgs := []message.Message{{
		Role:    "user",
		Content: content,
		Kind:    message.KindSubAgentMailbox,
		Mailbox: &message.MailboxMetadata{
			AgentID: "main",
			Kind:    "follow_up",
		},
	}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.Type != BlockStatus || block.StatusTitle != "AGENT MESSAGE" {
		t.Fatalf("block = %#v, want AGENT MESSAGE status card", block)
	}
	if block.StatusFrom != "main" || block.StatusKind != "follow_up" {
		t.Fatalf("notify block fields = from %q kind %q, want main/follow_up", block.StatusFrom, block.StatusKind)
	}
	if block.Content != content {
		t.Fatalf("block content = %q, want the persisted notify text", block.Content)
	}
	if block.LinkedAgentID != "main" || block.LinkedTaskID != "" {
		t.Fatalf("block links = (%q, %q), want main with no task", block.LinkedAgentID, block.LinkedTaskID)
	}
}

func TestAppendLocalStatusCardDefersWhileAssistantStreamIsActive(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	block := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: "streaming", Streaming: true}
	m.nextBlockID++
	m.currentAssistantBlock = block
	m.assistantBlockAppended = true
	m.appendViewportBlock(block)

	m.appendLocalStatusCard("EXPORT", "written")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1 while streaming", len(blocks))
	}
	if got := len(m.pendingLocalStatusCards); got != 1 {
		t.Fatalf("pendingLocalStatusCards = %d, want 1", got)
	}
	if m.currentAssistantBlock != block {
		t.Fatal("currentAssistantBlock should remain active")
	}
}

func TestFinalizeTurnFlushesDeferredLocalStatusCards(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	block := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: "streaming", Streaming: true}
	m.nextBlockID++
	m.currentAssistantBlock = block
	m.assistantBlockAppended = true
	m.appendViewportBlock(block)
	m.pendingLocalStatusCards = []localStatusCard{{title: "DIAGNOSTICS", content: "bundle ready"}}

	m.finalizeTurn()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible block count = %d, want 2 after finalize", len(blocks))
	}
	last := blocks[len(blocks)-1]
	if last.StatusTitle != "DIAGNOSTICS" || last.Content != "bundle ready" {
		t.Fatalf("last block = %#v, want diagnostics status card", last)
	}
	if got := len(m.pendingLocalStatusCards); got != 0 {
		t.Fatalf("pendingLocalStatusCards = %d, want 0", got)
	}
	if m.currentAssistantBlock != nil {
		t.Fatal("currentAssistantBlock should be finalized")
	}
}

func TestFinalizeAssistantBlockFlushesDeferredLocalStatusCards(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	block := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: "streaming", Streaming: true}
	m.nextBlockID++
	m.currentAssistantBlock = block
	m.assistantBlockAppended = true
	m.appendViewportBlock(block)
	m.pendingLocalStatusCards = []localStatusCard{{title: "DIAGNOSTICS", content: "bundle ready"}}

	m.finalizeAssistantBlock()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible block count = %d, want 2 after assistant finalize", len(blocks))
	}
	last := blocks[len(blocks)-1]
	if last.StatusTitle != "DIAGNOSTICS" || last.Content != "bundle ready" {
		t.Fatalf("last block = %#v, want diagnostics status card", last)
	}
	if got := len(m.pendingLocalStatusCards); got != 0 {
		t.Fatalf("pendingLocalStatusCards = %d, want 0", got)
	}
	if m.currentAssistantBlock != nil {
		t.Fatal("currentAssistantBlock should be finalized")
	}
}

func TestMessagesToBlocksSkillToolRestoresDisplaySummary(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{{
				ID:   "skill-1",
				Name: "skill",
				Args: json.RawMessage(`{"name":"skill-creator"}`),
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "skill-1",
			Content:    "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n\n# Skill Creator\n\n- Step one\n</skill>",
		},
	}

	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.ToolName != "skill" {
		t.Fatalf("ToolName = %q, want Skill", block.ToolName)
	}
	if got, want := block.Content, `{"name":"skill-creator","result":"\u003cpath\u003e/tmp/skills/skill-creator/SKILL.md\u003c/path\u003e"}`; got != want {
		t.Fatalf("Content = %q, want %q", got, want)
	}
	if !strings.Contains(block.ResultContent, "# Skill Creator") {
		t.Fatalf("ResultContent should preserve full skill body, got %q", block.ResultContent)
	}
}

func TestMessagesToBlocksSkipsInvisibleAssistantBodyButKeepsToolCall(t *testing.T) {
	msgs := []message.Message{{
		Role:    message.RoleAssistant,
		Content: "\u200b\u200b",
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: tools.NameGrep,
			Args: json.RawMessage(`{"pattern":"sample"}`),
		}},
	}}

	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want only the tool call", len(blocks))
	}
	if blocks[0].Type != BlockToolCall || blocks[0].ToolID != "call-1" {
		t.Fatalf("block = %#v, want grep tool call", blocks[0])
	}
}

func TestMessagesToBlocksKeepsVisibleEmojiZWJAssistantBody(t *testing.T) {
	const content = "Status 👩‍💻"
	nextID := 1
	blocks := messagesToBlocks([]message.Message{{Role: message.RoleAssistant, Content: content}}, &nextID)
	if len(blocks) != 1 || blocks[0].Type != BlockAssistant || blocks[0].Content != content {
		t.Fatalf("blocks = %#v, want visible assistant body unchanged", blocks)
	}
}

func TestHandleAgentEventSkillToolCollapsedSummaryButExpandedBody(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "skill-1",
		Name:     "skill",
		ArgsJSON: `{"name":"skill-creator","args":"Create 4 new skills"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "skill-1",
		Name:     "skill",
		ArgsJSON: `{"name":"skill-creator","args":"Create 4 new skills"}`,
		Status:   agent.ToolResultStatusSuccess,
		Result:   "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n<notes>ignored</notes>\n\n# Skill Creator\n\n- Step one\n- Step two\n</skill>",
	}})

	block, ok := m.viewport.FindBlockByToolID("skill-1")
	if !ok {
		t.Fatal("expected Skill tool block")
	}
	if got, want := block.Content, `{"name":"skill-creator","result":"\u003cpath\u003e/tmp/skills/skill-creator/SKILL.md\u003c/path\u003e"}`; got != want {
		t.Fatalf("Content = %q, want %q", got, want)
	}
	if !strings.Contains(block.ResultContent, "# Skill Creator") {
		t.Fatalf("ResultContent should preserve full skill body, got %q", block.ResultContent)
	}
	collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "skill skill-creator") {
		t.Fatalf("expected collapsed skill card summary to keep full skill name, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "name:") {
		t.Fatalf("collapsed skill card should not repeat a name param line, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "Create 4 new skills") || strings.Contains(collapsed, "Step one") || strings.Contains(collapsed, "<skill>") {
		t.Fatalf("collapsed skill card should hide args/body/tags, got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"skill skill-creator", "Skill Creator", "Step one", "Step two"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded skill card missing %q; got:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "<skill>") || strings.Contains(expanded, "<root>") || strings.Contains(expanded, "Create 4 new skills") {
		t.Fatalf("expanded skill card should show body but not wrapper tags/args, got:\n%s", expanded)
	}
}

func TestMessagesToBlocksDoneToolRestoresMarkdownReport(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{{
				ID:   "done-1",
				Name: "done",
				Args: json.RawMessage(`{"report":"## Completion status\nAll requested work is finished\n\n- shipped\n- verified"}`),
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "done-1",
			Content:    "Done approved",
		},
	}

	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.ToolName != "done" {
		t.Fatalf("ToolName = %q, want done", block.ToolName)
	}
	wantReport := "## Completion status\nAll requested work is finished\n\n- shipped\n- verified"
	if got := block.DoneReport; got != wantReport {
		t.Fatalf("DoneReport = %q, want %q", got, wantReport)
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"Completion status", "All requested work is finished", "• shipped", "• verified"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected restored Done card to contain %q, got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"Status:", "Done approved"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected restored Done card to omit %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestLiveAndRestoredDoneToolShareStableResultFields(t *testing.T) {
	args := `{"report":"## Completion status\nDone\n\n- verified"}`
	result := "Done approved"

	m := NewModelWithSize(nil, 120, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "done-1",
		Name:     tools.NameDone,
		ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:     "done-1",
		Name:       tools.NameDone,
		ArgsJSON:   args,
		Result:     result,
		Status:     agent.ToolResultStatusSuccess,
		DoneReport: "## Completion status\nDone\n\n- verified",
	}})
	live, ok := m.viewport.FindBlockByToolID("done-1")
	if !ok {
		t.Fatal("expected live done block")
	}

	nextID := 1
	restored := messagesToBlocks([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "done-1", Name: tools.NameDone, Args: json.RawMessage(args)}}},
		{Role: "tool", ToolCallID: "done-1", Content: result, ToolStatus: string(agent.ToolResultStatusSuccess)},
	}, &nextID)
	if len(restored) != 1 {
		t.Fatalf("restored len = %d, want 1", len(restored))
	}

	for name, block := range map[string]*Block{"live": live, "restored": restored[0]} {
		if block.DoneReport != "## Completion status\nDone\n\n- verified" {
			t.Fatalf("%s DoneReport = %q", name, block.DoneReport)
		}
		if block.ResultContent != result || block.ResultStatus != agent.ToolResultStatusSuccess || !block.ResultDone {
			t.Fatalf("%s result fields = content %q status %q done %t", name, block.ResultContent, block.ResultStatus, block.ResultDone)
		}
		if block.ToolExecutionState != "" || block.ToolProgress != nil {
			t.Fatalf("%s should not keep runtime execution state: state=%q progress=%#v", name, block.ToolExecutionState, block.ToolProgress)
		}
	}
}

func TestMessagesToBlocksRestoresLegacyLocalShellAsTerminal(t *testing.T) {
	msg := message.Message{
		Role:    "user",
		Content: "User:\n\n!ls pd1-10.csv\n\ncommand:\nls pd1-10.csv\n\noutput:\npd1-10.csv",
	}
	nextID := 1
	blocks := messagesToBlocks([]message.Message{msg}, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if !block.IsUserLocalShell() || block.UserLocalShellCmd != "ls pd1-10.csv" || block.UserLocalShellResult != "pd1-10.csv" {
		t.Fatalf("restored block = %#v, want local shell block", block)
	}
	if got := stripANSI(strings.Join(block.Render(80, ""), "\n")); !strings.Contains(got, "TERMINAL") || strings.Contains(got, "USER") {
		t.Fatalf("restored render = %q, want TERMINAL and no USER label", got)
	}
}

func TestMessagesToBlocksDelegateToolRestoresLinkedAgentMetadata(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{{
				ID:   "delegate-1",
				Name: "delegate",
				Args: json.RawMessage(`{"description":"review tests","agent_type":"reviewer"}`),
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "delegate-1",
			Content:    `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		},
	}

	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.ToolName != "delegate" {
		t.Fatalf("ToolName = %q, want Delegate", block.ToolName)
	}
	if got := block.LinkedAgentID; got != "reviewer-2" {
		t.Fatalf("LinkedAgentID = %q, want reviewer-2", got)
	}
	if got := block.LinkedTaskID; got != "adhoc-7" {
		t.Fatalf("LinkedTaskID = %q, want adhoc-7", got)
	}
}

func TestMessagesToBlocksUserPartsUseRawTextNotDisplayText(t *testing.T) {
	msgs := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "before\n"},
			{Type: "text", Text: "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11", DisplayText: "[Pasted text #1 +11 lines]"},
			{Type: "text", Text: "\nafter"},
			{Type: "text", Text: `<file path="docs/ARCHITECTURE.md">\nignored\n</file>`},
		},
	}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Type != BlockUser {
		t.Fatalf("block type = %v, want BlockUser", b.Type)
	}
	want := "before\nline1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11\nafter"
	if b.Content != want {
		t.Fatalf("user block content = %q, want full raw pasted text", b.Content)
	}
	if strings.Contains(b.Content, "[Pasted text #1 +11 lines]") {
		t.Fatalf("user block content should not contain placeholder: %q", b.Content)
	}
	if len(b.FileRefs) != 1 || b.FileRefs[0] != "docs/ARCHITECTURE.md" {
		t.Fatalf("FileRefs = %#v, want docs/ARCHITECTURE.md", b.FileRefs)
	}
}

func TestMessagesToBlocksCompactionSummaryFullyExpandedAndNotCollapsible(t *testing.T) {
	msgs := []message.Message{{
		Role:                "user",
		IsCompactionSummary: true,
		Content:             "[Context Summary]\n## Goal\n- Continue improving extraction quality.\n- Keep markdown headings visible.\n\n## Progress\n- Added preview state.\n\n## Key Decisions\n- Use compacting activity state.\n\n## Next Step\n- Ship.\n\n[Context compressed]\nEarlier conversation was compacted into the summary above.\nArchived history files:\n- history-1.md",
	}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Type != BlockCompactionSummary {
		t.Fatalf("block type = %v, want BlockCompactionSummary", b.Type)
	}
	// Compaction cards are always fully expanded: the archived context (and
	// the compaction facts) stay visible.
	if b.Collapsed {
		t.Fatal("compaction summary block should be fully expanded by default")
	}
	if !strings.Contains(b.Content, "history-1.md") {
		t.Fatalf("expanded content should show archived history path, got %q", b.Content)
	}
	b.ToggleAtWidth(120)
	if b.Collapsed {
		t.Fatal("compaction summary block should not collapse on toggle")
	}
	if !strings.Contains(b.Content, "history-1.md") {
		t.Fatalf("content should remain fully expanded after toggle, got %q", b.Content)
	}
}

func TestRebuildAfterCompactionResetsVisibleCardNumbers(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 98, Type: BlockUser, Content: "old prompt"})
	m.viewport.AppendBlock(&Block{ID: 99, Type: BlockAssistant, Content: "old response"})
	m.nextBlockID = 100
	backend.messages = []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md"},
		{Role: "assistant", Content: "continue after compaction"},
	}

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible blocks after rebuild = %d, want 2", len(blocks))
	}
	if blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("blocks[0].Type = %v, want BlockCompactionSummary", blocks[0].Type)
	}
	// The rebuilt rows share no identity with the dropped transcript, so they
	// get fresh IDs past the previous allocator high-water mark instead of
	// reusing the archived cards' IDs.
	if blocks[0].ID == blocks[1].ID || blocks[0].ID < 100 || blocks[1].ID < 100 {
		t.Fatalf("rebuilt block IDs = [%d, %d], want fresh unique IDs >= 100", blocks[0].ID, blocks[1].ID)
	}
	if m.nextBlockID <= max(blocks[0].ID, blocks[1].ID) {
		t.Fatalf("nextBlockID = %d, want past rebuilt ids [%d, %d]", m.nextBlockID, blocks[0].ID, blocks[1].ID)
	}
	if blocks[0].DisplaySequence != 1 {
		t.Fatalf("compaction summary display sequence = %d, want 1", blocks[0].DisplaySequence)
	}
	joined := stripANSI(strings.Join(blocks[0].Render(120, ""), "\n"))
	if !strings.Contains(joined, "CONTEXT SUMMARY #1") {
		t.Fatalf("compaction summary header should restart at #1, got:\n%s", joined)
	}
}

func TestSessionRestorePinsLongInterruptedReplyAtTail(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleUser, Content: "keep this restored prompt visible"},
		{Role: message.RoleAssistant, Content: strings.Repeat("partial reply line\n", 80), StopReason: "interrupted"},
	}}
	m := NewModelWithSize(backend, 80, 20)

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 || blocks[0].Type != BlockUser || blocks[1].Type != BlockAssistant {
		t.Fatalf("restored blocks = %#v, want user then interrupted assistant", blocks)
	}
	if !m.viewport.sticky || !m.viewport.atBottom() {
		t.Fatalf("restored interrupted turn should stay at tail: sticky=%v offset=%d total=%d height=%d", m.viewport.sticky, m.viewport.offset, m.viewport.totalLines, m.viewport.height)
	}
}

func TestSessionRestoreKeepsCompletedReplyAtTail(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleUser, Content: "completed prompt"},
		{Role: message.RoleAssistant, Content: strings.Repeat("completed reply line\n", 80), StopReason: "stop"},
	}}
	m := NewModelWithSize(backend, 80, 20)

	m.rebuildViewportFromMessagesWithReason("session_restored")

	if !m.viewport.sticky || !m.viewport.atBottom() {
		t.Fatalf("completed reply restore should stay at tail: sticky=%v offset=%d total=%d height=%d", m.viewport.sticky, m.viewport.offset, m.viewport.totalLines, m.viewport.height)
	}
}

func TestMessagesToBlocksRestoredEditWithoutToolDiffHidesSuccessResult(t *testing.T) {
	nextID := 1
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "patch-1", Name: tools.NameEdit, Args: []byte(`{"path":"foo.txt","patch":"@@\n-old\n+new\n"}`)}}},
		{Role: "tool", ToolCallID: "patch-1", Content: "Applied patch to foo.txt (+1 -1)", ToolStatus: string(agent.ToolResultStatusSuccess)},
	}

	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.ToolName != tools.NameEdit || !block.ResultDone || block.Diff != "" {
		t.Fatalf("restored block = %#v, want completed Edit without diff", block)
	}
	if block.Collapsed {
		t.Fatal("restored Edit should use the same expanded terminal state as live Edit results")
	}
	var displayArgs map[string]string
	if err := json.Unmarshal([]byte(block.Content), &displayArgs); err != nil {
		t.Fatalf("parse restored Edit Content %q: %v", block.Content, err)
	}
	if displayArgs["path"] != "foo.txt" {
		t.Fatalf("restored Edit display path = %q, want foo.txt", displayArgs["path"])
	}
	if _, ok := displayArgs["patch"]; ok {
		t.Fatalf("restored Edit Content = %q, want stable path-only display args", block.Content)
	}
	if block.RawArgs != `{"path":"foo.txt","patch":"@@\n-old\n+new\n"}` {
		t.Fatalf("restored Edit RawArgs = %q, want complete tool args", block.RawArgs)
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Contains(plain, "↳ Result:") || strings.Contains(plain, "Applied patch") {
		t.Fatalf("restored Edit without ToolDiff should hide routine success result, got:\n%s", plain)
	}
}

func TestMessagesToBlocksRestoredFileMutationResultsUseLiveExpandedState(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		args      json.RawMessage
		content   string
		status    agent.ToolResultStatus
		want      []string
		wantOrder []string
	}{
		{
			name:     "write error",
			toolName: tools.NameWrite,
			args:     json.RawMessage(`{"path":"foo.txt","content":"hello"}`),
			content:  "Error: writing file: permission denied",
			status:   agent.ToolResultStatusError,
			want:     []string{"↳ Error:", "permission denied"},
		},
		{
			name:      "apply patch error",
			toolName:  tools.NameEdit,
			args:      json.RawMessage(`{"path":"foo.txt","patch":"@@\n-old\n+new\n"}`),
			content:   "hunk not found; re-read the file before applying the patch",
			status:    agent.ToolResultStatusError,
			want:      []string{"↳ Patch:", "-old", "+new", "↳ Error:", "hunk not found"},
			wantOrder: []string{"↳ Patch:", "-old", "+new", "↳ Error:", "hunk not found"},
		},
		{
			name:     "delete success",
			toolName: tools.NameDelete,
			args:     json.RawMessage(`{"paths":["/tmp/obsolete.go"],"reason":"remove obsolete file"}`),
			content:  "Deleted (1):\n- /tmp/obsolete.go",
			status:   agent.ToolResultStatusSuccess,
			want:     []string{"delete /tmp/obsolete.go (remove obsolete file)", "Deleted /tmp/obsolete.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextID := 1
			msgs := []message.Message{
				{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: tt.toolName, Args: tt.args}}},
				{Role: "tool", ToolCallID: "call-1", Content: tt.content, ToolStatus: string(tt.status)},
			}

			blocks := messagesToBlocks(msgs, &nextID)
			if len(blocks) != 1 {
				t.Fatalf("len(blocks) = %d, want 1", len(blocks))
			}
			block := blocks[0]
			wantCollapsed := false
			if block.Collapsed != wantCollapsed {
				t.Fatalf("restored %s should use live expanded terminal state", tt.toolName)
			}
			plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
			for _, want := range tt.want {
				if !strings.Contains(plain, want) {
					t.Fatalf("restored %s render missing %q; got:\n%s", tt.toolName, want, plain)
				}
			}
			last := -1
			for _, want := range tt.wantOrder {
				idx := strings.Index(plain, want)
				if idx < 0 {
					t.Fatalf("restored %s render missing ordered item %q; got:\n%s", tt.toolName, want, plain)
				}
				if idx <= last {
					t.Fatalf("restored %s render order mismatch at %q; got:\n%s", tt.toolName, want, plain)
				}
				last = idx
			}
		})
	}
}

func TestLiveEditErrorShowsAttemptedPatchFromRawArgs(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	args := `{"path":"foo.txt","patch":"*** Begin Patch\n*** Update File: foo.txt\n@@\n-old\n+new\n*** End Patch\n"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "patch-1",
		Name:     tools.NameEdit,
		ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "patch-1",
		Name:     tools.NameEdit,
		ArgsJSON: args,
		Result:   "Error: hunk not found; re-read the file before applying the patch",
		Status:   agent.ToolResultStatusError,
	}})

	var block *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockToolCall && b.ToolID == "patch-1" {
			block = b
			break
		}
	}
	if block == nil {
		t.Fatal("expected live Edit tool block")
	}
	if block.Collapsed {
		t.Fatal("failed live Edit should be expanded")
	}
	if strings.Contains(block.Content, "patch") {
		t.Fatalf("display args should stay compact, got %s", block.Content)
	}
	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"edit foo.txt", "↳ Patch:", "*** Begin Patch", "*** End Patch", "@@", "-old", "+new", "↳ Error:", "hunk not found"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("live Edit error render missing %q; got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Error: hunk not found") {
		t.Fatalf("live Edit error render should not repeat Error prefix in the error body; got:\n%s", plain)
	}
	last := -1
	for _, want := range []string{"↳ Patch:", "*** Begin Patch", "@@", "-old", "+new", "*** End Patch", "↳ Error:", "hunk not found"} {
		idx := strings.Index(plain, want)
		if idx < 0 {
			t.Fatalf("live Edit error render missing ordered item %q; got:\n%s", want, plain)
		}
		if idx <= last {
			t.Fatalf("live Edit error render order mismatch at %q; got:\n%s", want, plain)
		}
		last = idx
	}
}

func TestSessionRestoredDeleteToolShowsReasonAndPersistedDuration(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "del-1", Name: tools.NameDelete, Args: json.RawMessage(`{"paths":["/tmp/obsolete.go"],"reason":"remove obsolete file"}`)}}},
		{Role: "tool", ToolCallID: "del-1", Content: "Deleted (1):\n- /tmp/obsolete.go", ToolStatus: string(agent.ToolResultStatusSuccess), ToolDurationMs: 1234},
	}}
	m := NewModelWithSize(backend, 120, 24)
	m.rebuildViewportFromMessagesWithReason("session_restored")

	var block *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockToolCall && b.ToolName == tools.NameDelete {
			block = b
			break
		}
	}
	if block == nil {
		t.Fatal("expected restored Delete tool block")
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "delete /tmp/obsolete.go (remove obsolete file)") {
		t.Fatalf("expected restored Delete header to lead with the path; got:\n%s", joined)
	}
	if !strings.Contains(joined, "remove obsolete file") {
		t.Fatalf("expected restored Delete header to show reason; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Deleted /tmp/obsolete.go") {
		t.Fatalf("expected restored Delete block to show compact success summary; got:\n%s", joined)
	}
	if block.PersistedDuration != 1234*time.Millisecond {
		t.Fatalf("PersistedDuration = %v, want 1234ms", block.PersistedDuration)
	}
}

func TestSessionRestoredToolStatusUsesPersistedStatus(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "sh-1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"false","description":"verify failure"}`)}}},
		{Role: "tool", ToolCallID: "sh-1", Content: "plain hook-modified failure", ToolStatus: string(agent.ToolResultStatusError)},
	}}
	m := NewModelWithSize(backend, 120, 24)
	m.rebuildViewportFromMessagesWithReason("session_restored")

	var block *Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockToolCall && b.ToolName == tools.NameShell {
			block = b
			break
		}
	}
	if block == nil {
		t.Fatal("expected restored Shell tool block")
	}
	if block.ResultStatus != agent.ToolResultStatusError {
		t.Fatalf("ResultStatus = %q, want %q", block.ResultStatus, agent.ToolResultStatusError)
	}
}

func TestSessionRestoredRebuildClearsStaleFocusedBlock(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md"}}}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 42, Type: BlockUser, Content: "stale"})
	m.focusedBlockID = 42
	m.refreshBlockFocus()

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("visible blocks after rebuild = %#v, want single compaction summary", blocks)
	}
	if m.focusedBlockID != blocks[0].ID {
		t.Fatalf("focusedBlockID after rebuild = %d, want compaction summary block %d", m.focusedBlockID, blocks[0].ID)
	}
	if !blocks[0].Focused {
		t.Fatal("rebuild should auto-focus visible compaction summary")
	}
}

func TestToggleCollapseFallsBackToBlockAtOffsetWhenFocusedBlockIsStale(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.mode = ModeNormal
	block := &Block{
		ID:                   1,
		Type:                 BlockCompactionSummary,
		CompactionSummaryRaw: "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md",
		Content:              "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md",
	}
	m.viewport.AppendBlock(block)
	m.recalcViewportSize()
	m.focusedBlockID = 99

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))

	if m.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID after stale toggle fallback = %d, want -1", m.focusedBlockID)
	}
	got := m.viewport.GetFocusedBlock(block.ID)
	if got == nil {
		t.Fatal("expected compaction summary block to remain present")
	}
	// Compaction cards are always fully expanded; toggle is a no-op.
	if got.Collapsed {
		t.Fatal("compaction summary should never collapse")
	}
	if !strings.Contains(got.Content, "history-1.md") {
		t.Fatalf("compaction summary content = %q, want archived history path", got.Content)
	}
}

func TestToggleCollapseFallsBackToBlockAtOffsetWhenFocusedBlockScrolledOutOfView(t *testing.T) {
	m := NewModelWithSize(nil, 100, 8)
	m.mode = ModeNormal

	focused := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "shell",
		Content:       `{"command":"echo first"}`,
		ResultContent: "first",
		ResultDone:    true,
		Collapsed:     true,
	}
	visible := &Block{
		ID:            2,
		Type:          BlockToolCall,
		ToolName:      "shell",
		Content:       `{"command":"echo second"}`,
		ResultContent: "second",
		ResultDone:    true,
		Collapsed:     true,
	}
	for i := range 4 {
		m.viewport.AppendBlock(&Block{ID: 10 + i, Type: BlockAssistant, Content: strings.Repeat("filler ", 20)})
	}
	m.viewport.AppendBlock(focused)
	m.viewport.AppendBlock(visible)
	m.recalcViewportSize()
	m.viewport.recalcTotalLines()

	// Simulate mouse-wheel scroll: offset moves past the focused block while
	// the focused block stays focused (mouse wheel does not clear focus).
	m.focusedBlockID = focused.ID
	focused.Focused = true
	if start, ok := m.viewport.LineOffsetForBlockID(focused.ID); !ok {
		t.Fatal("expected line offset for focused block")
	} else {
		// Scroll past the focused block so it leaves the window entirely.
		m.viewport.offset = start + m.viewport.blockSpanLines(m.viewport.GetFocusedBlock(focused.ID))
		m.viewport.clampOffset()
	}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))

	// The focused block scrolled out of view: Space must fall back to the
	// visible block at the current offset instead of folding an off-screen one.
	if m.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID after out-of-view toggle fallback = %d, want -1", m.focusedBlockID)
	}
	if focused.ToolCallDetailExpanded {
		t.Fatal("off-screen focused block must not be toggled")
	}
	if !visible.ToolCallDetailExpanded {
		t.Fatal("visible block at offset should be toggled by the Space fallback")
	}
}

func TestToggleCollapseActsOnFocusedBlockWhenStillVisible(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)
	m.mode = ModeNormal

	focused := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"echo first"}`,
		ResultContent:          "first",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}
	m.viewport.AppendBlock(focused)
	m.recalcViewportSize()
	m.viewport.recalcTotalLines()
	m.focusedBlockID = focused.ID
	focused.Focused = true

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))

	if m.focusedBlockID != focused.ID {
		t.Fatalf("focusedBlockID = %d, want %d", m.focusedBlockID, focused.ID)
	}
	if !focused.ToolCallDetailExpanded {
		t.Fatal("focused visible block should be expanded by Space")
	}
}

func TestHandleNormalKeySpaceOpensLinkedTaskWorkerView(t *testing.T) {
	backend := &sessionControlAgent{
		messagesByFocus: map[string][]message.Message{
			"": {
				{Role: "assistant", Content: "main history"},
			},
			"agent-1": {
				{Role: "assistant", Content: "worker history"},
			},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	task := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "delegate",
		Collapsed:     false,
		LinkedAgentID: "agent-1",
		Content:       `{"description":"review tests\ncheck coverage\nupdate docs","agent_type":"reviewer"}`,
		ResultContent: `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:    true,
		Focused:       true,
	}
	m.viewport.AppendBlock(task)
	m.recalcViewportSize()
	m.focusedBlockID = task.ID

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))

	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want agent-1", m.focusedAgentID)
	}
	if backend.focused != "agent-1" {
		t.Fatalf("backend focused = %q, want agent-1", backend.focused)
	}
	// Delegation cards are always expanded: space opens the worker view, it
	// never collapses the card (the expand/collapse toggle does not apply).
	if task.Collapsed {
		t.Fatal("space should not collapse the always-expanded Delegate card")
	}
}

func TestHandleNormalKeyEnterOnLinkedTaskSwitchesToWorkerView(t *testing.T) {
	backend := &sessionControlAgent{
		messagesByFocus: map[string][]message.Message{
			"": {
				{Role: "assistant", Content: "main history"},
			},
			"agent-1": {
				{Role: "assistant", Content: "worker history"},
			},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	task := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "delegate",
		Collapsed:     true,
		LinkedAgentID: "agent-1",
		Content:       `{"description":"review tests","agent_type":"reviewer"}`,
		ResultContent: `{"status":"started","task_id":"adhoc-7","agent_id":"agent-1"}`,
		ResultDone:    true,
		Focused:       true,
	}
	m.viewport.AppendBlock(task)
	m.recalcViewportSize()
	m.focusedBlockID = task.ID

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want agent-1", m.focusedAgentID)
	}
	if backend.focused != "agent-1" {
		t.Fatalf("backend focused = %q, want agent-1", backend.focused)
	}
}

func TestAgentViewRebuildsCompleteHistoryWhenLiveTailAlreadyExists(t *testing.T) {
	backend := &sessionControlAgent{
		messagesByFocus: map[string][]message.Message{
			"": {
				{Role: "user", Content: "main first"},
				{Role: "assistant", Content: "main middle"},
				{Role: "assistant", Content: "main latest"},
			},
			"agent-1": {
				{Role: "user", Content: "worker original task"},
				{Role: "assistant", Content: "worker earlier result"},
				{Role: "user", Content: "worker correction"},
			},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.appendViewportBlock(&Block{ID: m.nextBlockID, Type: BlockUser, AgentID: "agent-1", Content: "worker correction"})
	m.nextBlockID++

	m.setFocusedAgent("agent-1")

	workerBlocks := m.viewport.visibleBlocks()
	if len(workerBlocks) != 3 {
		t.Fatalf("len(workerBlocks) = %d, want complete three-card worker history", len(workerBlocks))
	}
	if workerBlocks[0].Content != "worker original task" || workerBlocks[2].Content != "worker correction" {
		t.Fatalf("worker history = %#v, want original task through correction", workerBlocks)
	}
	workerNext := &Block{ID: m.nextBlockID, Type: BlockAssistant, AgentID: "agent-1", Content: "worker next"}
	m.nextBlockID++
	m.appendViewportBlock(workerNext)
	if workerNext.DisplaySequence != 4 {
		t.Fatalf("worker next sequence = %d, want 4 after rebuilt history", workerNext.DisplaySequence)
	}

	// Reproduce a partial main viewport accumulated while the worker was focused.
	m.viewport.ReplaceBlocks([]*Block{{ID: m.nextBlockID, Type: BlockAssistant, Content: "main latest"}})
	m.nextBlockID++
	m.setFocusedAgent("")

	mainBlocks := m.viewport.visibleBlocks()
	if len(mainBlocks) != 3 {
		t.Fatalf("len(mainBlocks) = %d, want complete three-card main history", len(mainBlocks))
	}
	if mainBlocks[0].Content != "main first" || mainBlocks[2].Content != "main latest" {
		t.Fatalf("main history = %#v, want first through latest", mainBlocks)
	}
	mainNext := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: "main next"}
	m.nextBlockID++
	m.appendViewportBlock(mainNext)
	if mainNext.DisplaySequence != 4 {
		t.Fatalf("main next sequence = %d, want 4 after rebuilt history", mainNext.DisplaySequence)
	}
}

func TestCardDisplaySequencesAreIndependentPerAgent(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	mainFirst := &Block{ID: 189, Type: BlockUser, Content: "main first"}
	workerFirst := &Block{ID: 190, Type: BlockUser, AgentID: "agent-1", Content: "worker first"}
	mainSecond := &Block{ID: 191, Type: BlockAssistant, Content: "main second"}
	workerSecond := &Block{ID: 192, Type: BlockAssistant, AgentID: "agent-1", Content: "worker second"}
	for _, block := range []*Block{mainFirst, workerFirst, mainSecond, workerSecond} {
		m.appendViewportBlock(block)
	}

	if mainFirst.DisplaySequence != 1 || mainSecond.DisplaySequence != 2 {
		t.Fatalf("main sequences = [%d %d], want [1 2]", mainFirst.DisplaySequence, mainSecond.DisplaySequence)
	}
	if workerFirst.DisplaySequence != 1 || workerSecond.DisplaySequence != 2 {
		t.Fatalf("worker sequences = [%d %d], want [1 2]", workerFirst.DisplaySequence, workerSecond.DisplaySequence)
	}
	if got := stripANSI(strings.Join(mainFirst.Render(100, ""), "\n")); !strings.Contains(got, "USER #1") {
		t.Fatalf("main first label = %q, want USER #1", got)
	}
	if got := stripANSI(strings.Join(workerFirst.Render(100, ""), "\n")); !strings.Contains(got, "USER #1") {
		t.Fatalf("worker first label = %q, want USER #1", got)
	}
	if got := stripANSI(strings.Join(workerSecond.Render(100, ""), "\n")); !strings.Contains(got, "ASSISTANT #2") {
		t.Fatalf("worker second label = %q, want ASSISTANT #2", got)
	}
}

func TestSessionRestoredRebuildPrefersVisibleCompactionSummaryOverValidOldFocus(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md"},
		{Role: "user", Content: "recent tail"},
	}}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 11, Type: BlockUser, Content: "old first"})
	m.viewport.AppendBlock(&Block{ID: 12, Type: BlockUser, Content: "old second"})
	m.focusedBlockID = 12
	m.refreshBlockFocus()

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("blocks[0].Type = %v, want BlockCompactionSummary", blocks[0].Type)
	}
	if m.focusedBlockID != blocks[0].ID {
		t.Fatalf("focusedBlockID = %d, want compaction summary block %d", m.focusedBlockID, blocks[0].ID)
	}
	if !blocks[0].Focused {
		t.Fatal("expected visible compaction summary to take focus after session restore rebuild")
	}
}

func TestSessionRestoredRebuildDoesNotReuseOldCompactionRawForNewSummary(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{
		Role:                "user",
		IsCompactionSummary: true,
		Content:             "[Context Summary]\nsummary 2\n\n[Context compressed]\nArchived history files:\n- history-2.md",
	}}}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	old := &Block{
		ID:                   7,
		Type:                 BlockCompactionSummary,
		CompactionSummaryRaw: "[Context Summary]\nsummary 1\n\n[Context compressed]\nArchived history files:\n- history-1.md",
		Content:              "[Context Summary]\nsummary 1\n\n[Context compressed]\nArchived history files:\n- history-1.md",
	}
	m.viewport.AppendBlock(old)
	m.focusedBlockID = old.ID
	m.refreshBlockFocus()

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("visible blocks after rebuild = %#v, want single compaction summary", blocks)
	}
	got := blocks[0]
	// Compaction cards are always fully expanded regardless of prior state.
	if got.Collapsed {
		t.Fatal("new compaction summary should be fully expanded")
	}
	if !strings.Contains(got.CompactionSummaryRaw, "history-2.md") {
		t.Fatalf("CompactionSummaryRaw = %q, want history-2.md", got.CompactionSummaryRaw)
	}
	if strings.Contains(got.CompactionSummaryRaw, "history-1.md") {
		t.Fatalf("CompactionSummaryRaw = %q, should not retain old history-1.md", got.CompactionSummaryRaw)
	}
	if !strings.Contains(got.Content, "history-2.md") {
		t.Fatalf("content = %q, want history-2.md", got.Content)
	}
}

func TestSessionRestoredEventClearsStartupRestorePlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.startupRestorePending = true
	m.pendingScrollDelta = 9
	m.scrollFlushScheduled = true

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	if cmd == nil {
		t.Fatal("SessionRestoredEvent should schedule a rebuild message")
	}
	updated, next := m.Update(cmd())
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	m = *model
	if next != nil {
		_ = next
	}
	if m.startupRestorePending {
		t.Fatal("SessionRestoredEvent should clear startupRestorePending")
	}
	if m.pendingScrollDelta != 0 || m.scrollFlushScheduled {
		t.Fatal("SessionRestoredEvent should clear pending scroll flush state")
	}
}

func TestCompactionRebuildPreservesActiveMainRequest(t *testing.T) {
	stubTUITicks(t)

	backend := &sessionControlAgent{messages: []message.Message{{
		Role:                message.RoleUser,
		IsCompactionSummary: true,
		Content:             "[Context Summary]\nsummary\n\n[Context compressed]",
	}}}
	m := NewModelWithSize(backend, 100, 30)
	started := time.Now().Add(-2 * time.Second)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}
	m.activityStartTime["main"] = started
	m.workStartedAt["main"] = started
	m.turnBusyStartedAt["main"] = started
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "agent-1"}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{PreserveRequestActivity: true}})
	if cmd == nil {
		t.Fatal("compaction SessionRestoredEvent should schedule a rebuild message")
	}
	updated, next := m.Update(cmd())
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	m = *model
	if next == nil {
		t.Fatal("compaction rebuild did not schedule replacement animation tick")
	}
	commandMsg := next()
	batch, ok := commandMsg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("compaction rebuild command = %T, want tea.BatchMsg", commandMsg)
	}
	animationScheduled := false
	for _, child := range batch {
		if child == nil {
			continue
		}
		if _, ok := child().(animTickMsg); ok {
			animationScheduled = true
			break
		}
	}
	if !animationScheduled {
		t.Fatal("compaction rebuild did not include an animation tick")
	}

	if got := m.activityForAgent("main").Type; got != agent.ActivityConnecting {
		t.Fatalf("main activity after compaction rebuild = %v, want connecting", got)
	}
	if got := m.activityStartTime["main"]; !got.Equal(started) {
		t.Fatalf("main activity start after compaction rebuild = %v, want %v", got, started)
	}
	if _, ok := m.activities["agent-1"]; ok {
		t.Fatal("compaction rebuild should not preserve unrelated sub-agent activity")
	}
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "↓ 0 B") {
		t.Fatalf("status bar after compaction rebuild should show request activity, got %q", plain)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("rebuilt blocks = %#v, want one compaction summary", blocks)
	}
}

func TestOrdinarySessionRestoreStillClearsActiveMainRequest(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: message.RoleUser, Content: "restored"}}}
	m := NewModelWithSize(backend, 100, 30)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}
	m.activityStartTime["main"] = time.Now()

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	if cmd == nil {
		t.Fatal("SessionRestoredEvent should schedule a rebuild message")
	}
	updated, _ := m.Update(cmd())
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	m = *model

	if got := m.activityForAgent("main").Type; got != agent.ActivityIdle {
		t.Fatalf("main activity after ordinary restore = %v, want idle", got)
	}
	if _, ok := m.activityStartTime["main"]; ok {
		t.Fatal("ordinary restore should clear the previous request timing")
	}
}
