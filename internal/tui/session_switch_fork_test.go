package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/composer"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestRenderStatusBarHidesForkOriginPill(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "2000", ForkedFrom: "1000"}}
	m := NewModelWithSize(backend, 100, 24)
	plain := terminaltext.StripANSI(m.renderStatusBar())
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
	if m.sessionSwitch.active() {
		t.Fatalf("sessionSwitch = %+v, want inactive until backend confirms actual session switch", m.sessionSwitch)
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

func TestSyncVisibleMainUserBlockMsgIndexesSkipsSyntheticUserMessages(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "user", Content: "continue from where you stopped"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "continue from where you stopped", Kind: message.KindStreamContinue},
	}}
	m := NewModel(backend)
	m.viewport.ReplaceBlocks(nil)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "continue from where you stopped", MsgIndex: -1})

	m.syncVisibleMainUserBlockMsgIndexes()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	if got := blocks[0].MsgIndex; got != 0 {
		t.Fatalf("MsgIndex = %d, want 0 for the user-authored match instead of the newer synthetic duplicate", got)
	}
}

func TestSyncVisibleMainUserBlockMsgIndexesLeavesSyntheticOnlyContentUnmatched(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "job finished", Kind: message.KindBackgroundResult},
	}}
	m := NewModel(backend)
	m.viewport.ReplaceBlocks(nil)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "job finished", MsgIndex: -1})

	m.syncVisibleMainUserBlockMsgIndexes()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", len(blocks))
	}
	if got := blocks[0].MsgIndex; got != -1 {
		t.Fatalf("MsgIndex = %d, want -1 when only a synthetic message matches", got)
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

func TestPendingDraftConsumedEventSkipsSyntheticUserMessages(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "user", Content: "queued"},
		{Role: "user", Content: "queued", Kind: message.KindStreamContinue},
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
	var users []*Block
	for _, block := range blocks {
		if block.Type == BlockUser {
			users = append(users, block)
		}
	}
	if len(users) != 1 {
		t.Fatalf("user blocks = %d, want 1: the rebuilt card is adopted, not duplicated", len(users))
	}
	if got := users[0].MsgIndex; got != 0 {
		t.Fatalf("MsgIndex = %d, want 0 for the user-authored message", got)
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
	if got := m.input.Value(); got != "[Pasted text #1 +59 lines][screenshot.png]" {
		t.Fatalf("input value = %q, want inline paste placeholder followed by image token", got)
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

func TestForkSessionRebuildPreservesRestoredImageAttachments(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	parts := []message.ContentPart{
		{Type: message.ContentPartImage, MimeType: "image/jpeg", Data: []byte("a"), FileName: "image1.jpg"},
	}

	rebuildCmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, rebuildCmd)

	if len(m.attachments) != 1 || m.attachments[0].FileName != "image1.jpg" || string(m.attachments[0].Data) != "a" {
		t.Fatalf("attachments after deferred rebuild = %+v, want original image1.jpg attachment", m.attachments)
	}
	if got := m.nextInlineImageOrdinal(); got != 2 {
		t.Fatalf("nextInlineImageOrdinal() = %d, want 2", got)
	}

	m.input.InsertImagePlaceholderWithDisplay(2, "[image2.jpg]")
	m.handleAttachmentReadyMsg(attachmentReadyMsg{
		attachment:                Attachment{FileName: "image2.jpg", MimeType: "image/jpeg", Data: []byte("b")},
		inlineImagePlaceholderRaw: composer.ImagePlaceholder(2),
	})
	if len(m.attachments) != 2 {
		t.Fatalf("attachments after second image = %+v, want 2 attachments", m.attachments)
	}

	sentParts := interleaveAttachments(m.input.ContentParts(), m.attachments)
	var images []message.ContentPart
	for _, part := range sentParts {
		if part.Type == message.ContentPartImage {
			images = append(images, part)
		}
	}
	if len(images) != 2 {
		t.Fatalf("sent image parts = %+v, want 2 images", images)
	}
	if images[0].FileName != "image1.jpg" || string(images[0].Data) != "a" {
		t.Fatalf("first image = %+v, want original image1.jpg/a", images[0])
	}
	if images[1].FileName != "image2.jpg" || string(images[1].Data) != "b" {
		t.Fatalf("second image = %+v, want new image2.jpg/b", images[1])
	}
}

// A durable compaction rewrites the history of the session the user is still
// in. The rebuild it triggers must keep the composer the user is holding: a
// pasted image is gone for good once the attachment is dropped, even though
// its placeholder stays in the input text.
func TestCompactionRebuildKeepsComposerState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.attachments = []Attachment{{FileName: "screenshot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}, InlineImagePlaceholder: true}}
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued follow-up"}}
	m.agentComposerStates = map[string]agentComposerState{"worker-1": {attachments: cloneAttachments(m.attachments)}}
	m.editingQueuedDraftID = "draft-2"

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{PreserveRequestActivity: true, PreserveComposerState: true}})
	applyTestCmd(t, &m, cmd)

	if len(m.attachments) != 1 || m.attachments[0].FileName != "screenshot.png" {
		t.Fatalf("attachments after compaction rebuild = %+v, want the pasted image kept", m.attachments)
	}
	if len(m.queuedDrafts) != 1 || m.queuedDrafts[0].ID != "draft-1" {
		t.Fatalf("queued drafts after compaction rebuild = %+v, want the queued draft kept", m.queuedDrafts)
	}
	if state, ok := m.agentComposerStates["worker-1"]; !ok || len(state.attachments) != 1 {
		t.Fatalf("per-agent composer state after compaction rebuild = %+v, want it kept", m.agentComposerStates)
	}
	if m.editingQueuedDraftID != "draft-2" {
		t.Fatalf("editingQueuedDraftID = %q, want draft-2 kept", m.editingQueuedDraftID)
	}
}

// A restore or switch replaces the session, so the outgoing composer must not
// leak into the new one.
func TestSessionRestoreRebuildDropsComposerState(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.attachments = []Attachment{{FileName: "screenshot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}}
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued follow-up"}}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)

	if len(m.attachments) != 0 {
		t.Fatalf("attachments after session restore rebuild = %+v, want them dropped", m.attachments)
	}
	if len(m.queuedDrafts) != 0 {
		t.Fatalf("queued drafts after session restore rebuild = %+v, want them dropped", m.queuedDrafts)
	}
}

func TestForkSessionEventSubmitReloadsImagePathOnlyAttachment(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	imagePath := writeTinyPNG(t)
	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read image fixture: %v", err)
	}

	parts := []message.ContentPart{
		{Type: "text", Text: "what is this?"},
		{Type: "image", MimeType: "image/png", ImagePath: imagePath, FileName: "screenshot.png"},
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, cmd)
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.submittedInputs); got != 1 {
		t.Fatalf("SubmitUserInput() calls = %d, want 1", got)
	}
	sent := backend.submittedInputs[0].Parts
	if got := len(sent); got != 2 {
		t.Fatalf("sent parts = %d, want 2", got)
	}
	img := sent[1]
	if img.Type != "image" {
		t.Fatalf("sent[1].Type = %q, want image", img.Type)
	}
	if !bytes.Equal(img.Data, imageData) {
		t.Fatalf("sent image data = %v, want %v", img.Data, imageData)
	}
	if img.ImagePath != imagePath {
		t.Fatalf("sent image path = %q, want %q", img.ImagePath, imagePath)
	}
	if img.MimeType != "image/png" {
		t.Fatalf("sent image mime = %q, want image/png", img.MimeType)
	}
}

func TestForkSessionEventSubmitRestoresImageAfterPlainTextEdit(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	imageData := []byte{0x89, 'P', 'N', 'G'}
	parts := []message.ContentPart{
		{Type: "text", Text: "what is this?"},
		{Type: "image", MimeType: "image/png", Data: imageData, FileName: "screenshot.png"},
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, cmd)

	// Simulate an edit path that preserves the visible image placeholder text but
	// rebuilds the composer as plain text, losing inline image metadata.
	m.input.SetValue("edited " + m.input.Value())
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.submittedInputs); got != 1 {
		t.Fatalf("SubmitUserInput() calls = %d, want 1", got)
	}
	sent := backend.submittedInputs[0].Parts
	if got := len(sent); got != 2 {
		t.Fatalf("sent parts = %d, want 2", got)
	}
	if got := sent[0].Text; got != "edited what is this?" {
		t.Fatalf("sent text = %q, want edited prompt without UI-only attachment label", got)
	}
	img := sent[1]
	if img.Type != "image" {
		t.Fatalf("sent[1].Type = %q, want image", img.Type)
	}
	if !bytes.Equal(img.Data, imageData) {
		t.Fatalf("sent image data = %v, want %v", img.Data, imageData)
	}
}

func TestForkSessionEventSubmitKeepsLiteralImageTextAfterPlainTextEdit(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeNormal

	imageData := []byte{0x89, 'P', 'N', 'G'}
	parts := []message.ContentPart{
		{Type: "text", Text: "literal [image] text"},
		{Type: "image", MimeType: "image/png", Data: imageData, FileName: "screenshot.png"},
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ForkSessionEvent{Parts: parts}})
	applyTestCmd(t, &m, cmd)

	// Simulate an edit path that loses inline image metadata. The literal [image]
	// in the user's original text must not be consumed as the attachment marker.
	m.input.SetValue("edited " + m.input.Value())
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.submittedInputs); got != 1 {
		t.Fatalf("SubmitUserInput() calls = %d, want 1", got)
	}
	sent := backend.submittedInputs[0].Parts
	if got := len(sent); got != 2 {
		t.Fatalf("sent parts = %d, want 2", got)
	}
	if got := sent[0].Text; got != "edited literal [image] text" {
		t.Fatalf("sent text = %q, want literal text without UI-only attachment label", got)
	}
	img := sent[1]
	if img.Type != "image" {
		t.Fatalf("sent[1].Type = %q, want image", img.Type)
	}
	if !bytes.Equal(img.Data, imageData) {
		t.Fatalf("sent image data = %v, want %v", img.Data, imageData)
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
	if got := m.input.Value(); got != "[photo.jpg][diagram.png]" {
		t.Fatalf("input value = %q, want two inline image placeholders", got)
	}
	pastes := m.input.InlinePastes()
	if len(pastes) != 2 || pastes[0].Kind != composer.TokenImage || pastes[1].Kind != composer.TokenImage {
		t.Fatalf("inline pastes = %#v, want two image tokens", pastes)
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
