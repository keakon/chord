package tui

import (
	"image"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestClipboardTextPasteShortInsertsIntoComposer(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert

	updated, cmd := m.Update(clipboardTextMsg("line1\nline2"))
	model := updated.(*Model)
	if cmd != nil {
		t.Fatalf("clipboard short paste command = %#v, want nil", cmd)
	}
	if got := model.input.Value(); got != "line1\nline2" {
		t.Fatalf("input value = %q, want short pasted text", got)
	}
}

func TestClipboardTextPasteUpdatesAtMentionQuery(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.atMentionLoaded = true
	m.atMentionFiles = []string{"docs/RATE_LIMIT_PLAN.md"}
	m.input.SetValue("@")
	m.input.SetCursorPosition(0, 1)
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1

	updated, _ := m.Update(clipboardTextMsg("docs/RATE_LIMIT_PLAN.md"))
	model := updated.(*Model)

	if got := model.atMentionQuery; got != "docs/RATE_LIMIT_PLAN.md" {
		t.Fatalf("atMentionQuery = %q, want pasted query", got)
	}
	if model.atMentionList == nil || model.atMentionList.Len() == 0 {
		t.Fatal("atMentionList should be populated after paste")
	}
}

func TestClipboardTextPasteLongUsesInlinePlaceholder(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	text := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n")

	updated, _ := m.Update(clipboardTextMsg(text))
	model := updated.(*Model)
	if got := model.input.Value(); got != "[Pasted text #1 +11 lines]" {
		t.Fatalf("input value = %q, want inline placeholder", got)
	}
	if !model.input.HasInlinePastes() {
		t.Fatal("input should track inline paste placeholder")
	}
}

func TestHandleInsertKeySubmitsLargePasteAsHiddenTextPart(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	if !m.input.InsertLargePaste(strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n")) {
		t.Fatal("InsertLargePaste() = false, want true")
	}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMultipart); got != 1 {
		t.Fatalf("SendUserMessageWithParts() calls = %d, want 1", got)
	}
	parts := backend.sentMultipart[0]
	if len(parts) != 1 {
		t.Fatalf("sent parts len = %d, want 1", len(parts))
	}
	if parts[0].DisplayText == "" {
		t.Fatal("large paste part missing DisplayText summary")
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("viewport block count = %d, want 1", len(blocks))
	}
	want := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n")
	if got := blocks[0].Content; got != want {
		t.Fatalf("user block content = %q, want full pasted text", got)
	}
}

func TestHandleInsertKeySubmitsFileRefsParsedFromLargePaste(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.workingDir = t.TempDir()
	mustWriteFile(t, filepath.Join(m.workingDir, "docs", "RATE_LIMIT_PLAN.md"), "rate limit plan")
	if !m.input.InsertLargePaste(strings.Join([]string{
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
		"line6",
		"line7",
		"line8",
		"line9",
		"line10",
		"see @docs/RATE_LIMIT_PLAN.md",
	}, "\n")) {
		t.Fatal("InsertLargePaste() = false, want true")
	}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMultipart); got != 1 {
		t.Fatalf("SendUserMessageWithParts() calls = %d, want 1", got)
	}
	parts := backend.sentMultipart[0]
	if len(parts) != 2 {
		t.Fatalf("sent parts len = %d, want 2 (hidden paste + file)", len(parts))
	}
	if !strings.Contains(parts[1].Text, `<file path="docs/RATE_LIMIT_PLAN.md">`) {
		t.Fatalf("second part = %q, want embedded file ref", parts[1].Text)
	}
	if got := m.viewport.visibleBlocks()[0].FileRefs; len(got) != 1 || got[0] != "docs/RATE_LIMIT_PLAN.md" {
		t.Fatalf("user block file refs = %#v, want docs/RATE_LIMIT_PLAN.md", got)
	}
	wantBody := strings.Join([]string{
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
		"line6",
		"line7",
		"line8",
		"line9",
		"line10",
		"see @docs/RATE_LIMIT_PLAN.md",
	}, "\n")
	if got := userBlockTextFromParts(parts, ""); got != wantBody {
		t.Fatalf("composed text from parts = %q, want full composer body with pasted lines", got)
	}
	if got := m.viewport.visibleBlocks()[0].Content; got != wantBody {
		t.Fatalf("user block content = %q, want full pasted text (not inline placeholder)", got)
	}
}

func TestBackspaceRemovesInlinePlaceholderAsWhole(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	_ = m.input.InsertLargePaste(strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n"))

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))

	if m.input.HasInlinePastes() {
		t.Fatal("inline placeholder should be removed by backspace as a whole")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty after removing placeholder", got)
	}
}

func TestLoadQueuedDraftRestoresInlinePlaceholder(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	parts := []message.ContentPart{{
		Type:        "text",
		Text:        strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n"),
		DisplayText: "[Pasted text #1 +11 lines]",
	}}
	m.queuedDrafts = []queuedDraft{{
		ID:             "draft-1",
		Parts:          parts,
		DisplayContent: "[Pasted text #1 +11 lines]",
	}}

	cmd := m.editQueuedDraftAt(0)
	if cmd == nil {
		t.Fatal("editQueuedDraftAt() = nil, want focus command")
	}
	if !m.input.HasInlinePastes() {
		t.Fatal("input should restore inline paste placeholder from queued draft")
	}
	if got := m.input.Value(); got != "[Pasted text #1 +11 lines]" {
		t.Fatalf("input value = %q, want restored inline placeholder", got)
	}
}

func TestInlinePlaceholderCannotBeEditedInside(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	if !m.input.InsertLargePaste(strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n")) {
		t.Fatal("InsertLargePaste() = false, want true")
	}
	m.input.SetCursorPosition(0, 5)

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Text: "X"}))

	if got := m.input.Value(); got != "[Pasted text #1 +11 lines]" {
		t.Fatalf("input value = %q, want unchanged placeholder", got)
	}
}

func TestHandleInsertKeyQueuesBusyMainDraftLocally(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("queued")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(m.queuedDrafts); got != 1 {
		t.Fatalf("len(queuedDrafts) = %d, want 1", got)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if got := len(backend.queuedDraftIDs); got != 1 {
		t.Fatalf("QueuePendingUserDraft() calls = %d, want 1", got)
	}
	if backend.queuedDraftIDs[0] == "" {
		t.Fatal("queued draft id = empty, want generated id")
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("viewport block count = %d, want 0", got)
	}
}

func TestHandleInsertKeyBusyMainAgentSlashBypassesLocalQueue(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("/model")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	if got := len(backend.sentMessages); got != 1 {
		t.Fatalf("SendUserMessage() calls = %d, want 1", got)
	}
	if backend.sentMessages[0] != "/model" {
		t.Fatalf("sent message = %q, want /model", backend.sentMessages[0])
	}
}

func TestHandleInsertKeyBusyMainAgentCompactBypassesLocalQueue(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("/compact")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	if got := len(backend.sentMessages); got != 1 {
		t.Fatalf("SendUserMessage() calls = %d, want 1", got)
	}
	if backend.sentMessages[0] != "/compact" {
		t.Fatalf("sent message = %q, want /compact", backend.sentMessages[0])
	}
}

func TestHandleInsertKeyStatsOpensLocalPanelWhenBusy(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("/stats")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if m.mode != ModeUsageStats {
		t.Fatalf("mode = %v, want ModeUsageStats", m.mode)
	}
}

func TestHandleInsertKeyBusySubAgentSkipsLocalQueue(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.focusedAgentID = "agent-1"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("subagent message")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	if got := len(backend.sentMessages); got != 1 {
		t.Fatalf("SendUserMessage() calls = %d, want 1", got)
	}
}

func TestHandleInsertKeyBusySubAgentWithImageSendsPartsImmediately(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.focusedAgentID = "agent-1"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("describe this")
	m.attachments = []Attachment{{FileName: "clip.png", MimeType: "image/png", Data: []byte{1, 2, 3}}}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	if got := len(backend.sentMultipart); got != 1 {
		t.Fatalf("SendUserMessageWithParts() calls = %d, want 1", got)
	}
	parts := backend.sentMultipart[0]
	if len(parts) != 2 {
		t.Fatalf("sent parts len = %d, want 2", len(parts))
	}
	if parts[1].Type != "image" || parts[1].MimeType != "image/png" {
		t.Fatalf("image part = %#v", parts[1])
	}
}

func TestHandleInsertKeyEmptyComposerContinuesFromCommittedUserContext(t *testing.T) {
	backend := &sessionControlAgent{
		messages: []message.Message{
			{Role: "assistant", Content: "partial reply", StopReason: "interrupted"},
			{Role: "user", Content: "queued after cancel"},
		},
	}
	m := NewModel(backend)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := backend.continueCalls; got != 1 {
		t.Fatalf("ContinueFromContext() calls = %d, want 1", got)
	}
}

func TestHandleInsertKeyEmptyComposerIgnoresStalePendingToolCardWhenIdle(t *testing.T) {
	backend := &sessionControlAgent{
		messages: []message.Message{
			{Role: "assistant", Content: "partial reply", StopReason: "interrupted"},
			{Role: "user", Content: "queued after cancel"},
		},
	}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "Read",
		ToolID:     "call-stale",
		Content:    `{"path":"main.go"}`,
		ResultDone: false,
	})

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := backend.continueCalls; got != 1 {
		t.Fatalf("ContinueFromContext() calls = %d, want 1 despite stale pending tool card", got)
	}
}

func TestQueuedDraftsAutoContinueIgnoresLocalOnlyReclaimedDraft(t *testing.T) {
	m := NewModel(nil)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "previous", Mirrored: true}}
	m.prependQueuedDraft(queuedDraft{ID: "draft-2", Content: "local only", Mirrored: true})

	if len(m.queuedDrafts) != 2 {
		t.Fatalf("len(queuedDrafts) = %d, want 2", len(m.queuedDrafts))
	}
	if m.queuedDrafts[0].Mirrored {
		t.Fatal("prepended reclaimed draft should be local-only (Mirrored=false)")
	}
	if !m.queuedDraftsAutoContinue() {
		t.Fatal("queuedDraftsAutoContinue = false, want true when a mirrored draft remains")
	}
	m.queuedDrafts = []queuedDraft{{ID: "draft-3", Content: "local only", Mirrored: false}}
	if m.queuedDraftsAutoContinue() {
		t.Fatal("queuedDraftsAutoContinue = true, want false for local-only queued draft")
	}
}

func TestPendingDraftConsumedEventAppendsTranscriptWhenActuallyConsumed(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("queued")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	draftID := backend.queuedDraftIDs[0]
	_ = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{
		DraftID: draftID,
		Parts:   []message.ContentPart{{Type: "text", Text: "queued"}},
	}})

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockUser || blocks[0].Content != "queued" {
		t.Fatalf("viewport blocks = %+v, want single consumed user block", blocks)
	}
	if m.inflightDraft == nil || m.inflightDraft.ID != draftID {
		t.Fatalf("inflightDraft = %+v, want consumed draft %q", m.inflightDraft, draftID)
	}
}

func TestEditQueuedDraftRemovesPendingDraftAndLoadsComposer(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "edit me"}}
	m.queueSyncEnabled = true

	cmd := m.editQueuedDraftAt(0)
	if cmd == nil {
		t.Fatal("editQueuedDraftAt() = nil, want focus command")
	}

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	if got := m.input.Value(); got != "edit me" {
		t.Fatalf("input value = %q, want edit me", got)
	}
	if got := m.editingQueuedDraftID; got != "draft-1" {
		t.Fatalf("editingQueuedDraftID = %q, want draft-1", got)
	}
	if got := len(backend.removedDraftIDs); got != 1 || backend.removedDraftIDs[0] != "draft-1" {
		t.Fatalf("RemovePendingUserDraft() calls = %+v, want [draft-1]", backend.removedDraftIDs)
	}
}

func TestDeleteQueuedDraftRemovesPendingDraft(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "delete me"}}
	m.queueSyncEnabled = true

	_ = m.deleteQueuedDraftAt(0)

	if got := len(m.queuedDrafts); got != 0 {
		t.Fatalf("len(queuedDrafts) = %d, want 0", got)
	}
	if got := len(backend.removedDraftIDs); got != 1 || backend.removedDraftIDs[0] != "draft-1" {
		t.Fatalf("RemovePendingUserDraft() calls = %+v, want [draft-1]", backend.removedDraftIDs)
	}
}

func TestRenderQueuedDraftsLeavesRightMarginForDeleteToken(t *testing.T) {
	m := NewModel(nil)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "delete me"}}

	got := stripANSI(m.renderQueuedDrafts(24, 1))
	if !strings.HasSuffix(got, queuedDraftDeleteToken+strings.Repeat(" ", queuedDraftDeleteRightMargin)) {
		t.Fatalf("renderQueuedDrafts() suffix = %q, want delete token with %d trailing spaces", got, queuedDraftDeleteRightMargin)
	}
}

func TestQueuedDraftActionAtOnlyTreatsDeleteTokenAreaAsDelete(t *testing.T) {
	m := NewModel(nil)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "delete me"}}
	m.layout.queue = image.Rect(0, 0, 24, 1)

	deleteWidth := runewidth.StringWidth(queuedDraftDeleteToken)
	deleteStart := m.layout.queue.Max.X - queuedDraftDeleteRightMargin - deleteWidth

	if idx, remove, ok := m.queuedDraftActionAt(deleteStart-1, 0); !ok || idx != 0 || remove {
		t.Fatalf("queuedDraftActionAt(text area) = (%d, %v, %v), want (0, false, true)", idx, remove, ok)
	}
	if idx, remove, ok := m.queuedDraftActionAt(deleteStart, 0); !ok || idx != 0 || !remove {
		t.Fatalf("queuedDraftActionAt(delete token) = (%d, %v, %v), want (0, true, true)", idx, remove, ok)
	}
	if idx, remove, ok := m.queuedDraftActionAt(m.layout.queue.Max.X-1, 0); !ok || idx != 0 || remove {
		t.Fatalf("queuedDraftActionAt(right margin) = (%d, %v, %v), want (0, false, true)", idx, remove, ok)
	}
}

func TestHandleNormalEscapeClearsPendingChordWithoutCancellingBusyTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}
	m.chord = chordState{op: chordG, startAt: time.Now()}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	if cmd != nil {
		t.Fatalf("esc with pending chord should not return cancel command, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("pending chord should be cleared by esc")
	}
	if backend.cancelCalls != 0 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 0", backend.cancelCalls)
	}
	if m.activeToast != nil {
		t.Fatalf("activeToast = %+v, want nil", m.activeToast)
	}
}

func TestHandleNormalEscapeCancelsBusyMainTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	if m.activeToast == nil {
		t.Fatal("expected cancel toast, got nil")
	}
}
