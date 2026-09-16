package tui

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/clipboardread"
	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg=="

func writeTinyPNG(t *testing.T) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write tiny png: %v", err)
	}
	return path
}

func TestHandleNonKeyInputMsgKeepsImagePathPasteAsText(t *testing.T) {
	path := writeTinyPNG(t)
	attachmentReads := 0
	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		attachmentReads++
		return nil, "", clipboardread.ErrNoAttachment
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleNonKeyInputMsg(tea.PasteMsg{Content: path})
	if cmd != nil {
		t.Fatalf("expected no attachment command, got %T", cmd)
	}
	if got := m.input.Value(); got != path {
		t.Fatalf("input value = %q, want pasted path text %q", got, path)
	}
	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d, want 0", len(m.attachments))
	}
	if attachmentReads != 0 {
		t.Fatalf("clipboard attachment reads = %d, want 0", attachmentReads)
	}
}

func TestPasteMsgInsertsTextWithoutReadingClipboardAttachment(t *testing.T) {
	attachmentReads := 0
	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		attachmentReads++
		return []byte("image"), "image/png", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleNonKeyInputMsg(tea.PasteMsg{Content: "hello"})
	if cmd != nil {
		t.Fatalf("paste command = %T, want nil", cmd)
	}
	if got := m.input.Value(); got != "hello" {
		t.Fatalf("input value = %q, want hello", got)
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("attachments = %d, want 0", got)
	}
	if attachmentReads != 0 {
		t.Fatalf("clipboard attachment reads = %d, want 0", attachmentReads)
	}
}

func TestInsertAttachClipboardSuppressesImmediateTerminalPasteText(t *testing.T) {
	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		return []byte("image"), "image/png", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected ctrl+v to start an attachment read")
	}
	if pasteCmd := m.handleNonKeyInputMsg(tea.PasteMsg{Content: "duplicate text"}); pasteCmd != nil {
		t.Fatalf("duplicate terminal paste command = %T, want nil", pasteCmd)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want duplicate text suppressed", got)
	}
}

func TestInsertAttachClipboardReadsImageAsynchronously(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	attachmentReads := 0
	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		attachmentReads++
		return data, "image/png", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	model := NewModelWithSize(nil, 80, 24)
	model.mode = ModeInsert

	cmd := model.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected ctrl+v to start an asynchronous attachment read")
	}
	if attachmentReads != 0 {
		t.Fatalf("clipboard attachment reads before cmd execution = %d, want 0", attachmentReads)
	}
	if !model.clipboardAttachmentPending {
		t.Fatal("clipboard attachment should be pending before cmd completion")
	}
	if got := len(model.attachments); got != 0 {
		t.Fatalf("attachments before cmd completion = %d, want 0", got)
	}

	updated, _ := model.Update(cmd())
	model = *updated.(*Model)
	if attachmentReads != 1 {
		t.Fatalf("clipboard attachment reads = %d, want 1", attachmentReads)
	}
	if model.clipboardAttachmentPending {
		t.Fatal("clipboard attachment should not remain pending after completion")
	}
	if got := model.input.Value(); got != "[image1.png]" {
		t.Fatalf("input value = %q, want %q", got, "[image1.png]")
	}
	if got := len(model.attachments); got != 1 || model.attachments[0].MimeType != "image/png" {
		t.Fatalf("attachments = %#v, want one PNG", model.attachments)
	}
}

func TestAltVStartsClipboardAttachmentRead(t *testing.T) {
	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		return []byte("image"), "image/png", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModAlt}))
	if cmd == nil || !m.clipboardAttachmentPending {
		t.Fatal("alt+v should start a pending clipboard attachment read")
	}
}

func TestInsertAttachClipboardAddsPDFWithoutInlineImagePlaceholder(t *testing.T) {
	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		return []byte("%PDF-1.7\n/Encrypt"), "application/pdf", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected ctrl+v to start PDF clipboard read")
	}
	m.Update(cmd())

	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want no inline image placeholder", got)
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments = %d, want 1", got)
	}
	attachment := m.attachments[0]
	if attachment.FileName != "attachment1.pdf" || attachment.MimeType != "application/pdf" {
		t.Fatalf("attachment = %#v, want attachment1.pdf PDF", attachment)
	}
	if !attachment.Encrypted {
		t.Fatal("encrypted clipboard PDF should be marked encrypted")
	}
	if m.input.HasInlinePastes() {
		t.Fatal("clipboard PDF should not create an inline image placeholder")
	}
}

func TestInsertAttachClipboardReportsNoAttachmentWithoutTextFallback(t *testing.T) {
	origAttachment := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		return nil, "", clipboardread.ErrNoAttachment
	}
	origText := clipboardReadAll
	textReads := 0
	clipboardReadAll = func() (string, error) {
		textReads++
		return "clipboard text", nil
	}
	t.Cleanup(func() {
		readAttachmentFromClipboard = origAttachment
		clipboardReadAll = origText
	})

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected ctrl+v to start attachment read")
	}
	updated, resultCmd := m.Update(cmd())
	m = *updated.(*Model)
	if resultCmd == nil {
		t.Fatal("missing clipboard attachment should return a warning toast command")
	}
	if textReads != 0 {
		t.Fatalf("clipboard text reads = %d, want 0", textReads)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty", got)
	}
	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d, want 0", len(m.attachments))
	}
}

func TestCmdVPastesTextWithoutReadingClipboardAttachment(t *testing.T) {
	attachmentReads := 0
	origAttachment := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		attachmentReads++
		return []byte("image"), "image/png", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return "clipboard text", nil
	}
	t.Cleanup(func() {
		readAttachmentFromClipboard = origAttachment
		clipboardReadAll = origText
	})

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	cmd := m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModSuper}))
	if cmd == nil {
		t.Fatal("expected cmd+v to return a text clipboard command")
	}
	updated, _ := m.Update(cmd())
	m = *updated.(*Model)
	if got := m.input.Value(); got != "clipboard text" {
		t.Fatalf("input value = %q, want clipboard text", got)
	}
	if attachmentReads != 0 {
		t.Fatalf("clipboard attachment reads = %d, want 0", attachmentReads)
	}
}

func TestPasteImageSecondPasteAddsOneAttachment(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		return data, "image/jpeg", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	m.Update(cmd())
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after first ctrl+v = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "image1.jpg" {
		t.Fatalf("first attachment name = %q, want image1.jpg", got)
	}

	cmd = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	m.Update(cmd())
	if got := len(m.attachments); got != 2 {
		t.Fatalf("attachments after second ctrl+v = %d, want 2", got)
	}
	if got := m.attachments[1].FileName; got != "image2.jpg" {
		t.Fatalf("second attachment name = %q, want image2.jpg", got)
	}
	if got := m.input.Value(); got != "[image1.jpg][image2.jpg]" {
		t.Fatalf("input value after second ctrl+v = %q, want two placeholders", got)
	}
}

func TestPasteImageAfterPDFUsesNextInlineImageOrdinal(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.attachments = []Attachment{{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")}}

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	m.Update(cmd())
	if got := len(m.attachments); got != 2 {
		t.Fatalf("attachments after ctrl+v = %d, want 2", got)
	}
	if got := m.attachments[1].FileName; got != "image1.png" {
		t.Fatalf("clipboard image name = %q, want image1.png", got)
	}
	pastes := m.input.InlinePastes()
	if len(pastes) != 1 || pastes[0].RawContent != imagePlaceholder(1) {
		t.Fatalf("inline image paste = %#v, want image1 placeholder", pastes)
	}
	parts := interleaveAttachments([]message.ContentPart{{Type: "text", Text: imagePlaceholder(1), DisplayText: "[image1.png]", InlineToken: inlineImageTokenMarker}}, m.attachments)
	if len(parts) != 2 {
		t.Fatalf("interleaved parts len = %d, want 2", len(parts))
	}
	if parts[0].Type != "image" || parts[0].FileName != "image1.png" {
		t.Fatalf("parts[0] = %#v, want inline image1.png", parts[0])
	}
	if parts[1].Type != "pdf" || parts[1].FileName != "report.pdf" {
		t.Fatalf("parts[1] = %#v, want appended report.pdf", parts[1])
	}
}

func TestInsertAttachClipboardBlocksSubmitUntilImageReady(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	t.Cleanup(func() { readAttachmentFromClipboard = orig })

	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)
	m.mode = ModeInsert

	readCmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if readCmd == nil || !m.clipboardAttachmentPending {
		t.Fatal("ctrl+v should start a pending clipboard attachment read")
	}

	if cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd == nil {
		t.Fatal("enter while clipboard attachment is pending should return a toast command")
	}
	if got := len(backend.sentMultipart); got != 0 {
		t.Fatalf("SendUserMessageWithParts() calls while pending = %d, want 0", got)
	}

	m.Update(readCmd())
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after read completion = %d, want 1", got)
	}
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if got := len(backend.sentMultipart); got != 1 {
		t.Fatalf("SendUserMessageWithParts() calls after completion = %d, want 1", got)
	}
	parts := backend.sentMultipart[0]
	if len(parts) != 1 {
		t.Fatalf("sent parts = %#v, want single image part", parts)
	}
	if got := parts[0].Type; got != "image" {
		t.Fatalf("sent part type = %q, want image", got)
	}
	if got := parts[0].MimeType; got != "image/png" {
		t.Fatalf("sent image mime = %q, want image/png", got)
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("attachments after enter = %d, want 0", got)
	}
}

func TestConfirmCmdVPastesTextEvenWhenClipboardHasImage(t *testing.T) {
	attachmentReads := 0
	origAttachment := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		attachmentReads++
		return []byte{0x89, 'P', 'N', 'G'}, "image/png", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return `{"command":"echo pasted"}`, nil
	}
	t.Cleanup(func() {
		readAttachmentFromClipboard = origAttachment
		clipboardReadAll = origText
	})

	m := NewModelWithSize(nil, 100, 40)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo old"}`}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)
	m.confirm.editInput.SetValue("")

	cmd := m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModSuper}))
	if cmd == nil {
		t.Fatal("expected cmd+v in confirm edit mode to paste from clipboard")
	}
	msg := cmd()
	textMsg, ok := msg.(clipboardTextMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want clipboardTextMsg", msg)
	}
	updated, _ := m.Update(textMsg)
	model := updated.(*Model)
	if got := model.confirm.editInput.Value(); got != `{"command":"echo pasted"}` {
		t.Fatalf("confirm edit input = %q", got)
	}
	if got := len(model.attachments); got != 0 {
		t.Fatalf("attachments = %d, want 0", got)
	}
	if attachmentReads != 0 {
		t.Fatalf("clipboard attachment reads = %d, want 0", attachmentReads)
	}
}

func TestPasteTextFromClipboardReturnsNilWhenClipboardEmpty(t *testing.T) {
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return "", errors.New("empty")
	}
	defer func() { clipboardReadAll = origText }()

	cmd := pasteTextFromClipboard()
	if cmd == nil {
		t.Fatal("pasteTextFromClipboard() = nil")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("cmd() = %T, want nil", msg)
	}
}

func TestAttachmentReadyMsgErrorRollsBackPendingInlineImagePlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.InsertString("hello")
	m.input.SetCursorPosition(0, len([]rune(m.input.Value())))
	if !m.input.InsertImagePlaceholder(1) {
		t.Fatal("InsertImagePlaceholder(1) = false, want true")
	}
	m.insertComposerText(" world")
	if got := m.input.Value(); got != "hello"+inlineImagePlaceholderDisplay+" world" {
		t.Fatalf("input value after insert = %q", got)
	}

	updated, _ := m.Update(attachmentReadyMsg{err: errors.New("clipboard failed"), inlineImagePlaceholderRaw: imagePlaceholder(1)})
	model := updated.(*Model)
	if got := model.input.Value(); got != "hello world" {
		t.Fatalf("input value after rollback = %q, want %q", got, "hello world")
	}
	if got := len(model.attachments); got != 0 {
		t.Fatalf("attachments = %d, want 0", got)
	}
	if model.input.HasInlinePastes() {
		t.Fatal("inline image placeholder should be removed after rollback")
	}
}

func TestAttachmentReadyMsgSizeLimitRollsBackOnlyNewPendingInlineImagePlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.attachments = []Attachment{{FileName: "image1.png", MimeType: "image/png", Data: []byte{1}}}
	if !m.input.InsertImagePlaceholder(1) {
		t.Fatal("InsertImagePlaceholder(1) = false, want true")
	}
	m.insertComposerText("tail")
	m.input.SetCursorPosition(0, len([]rune(m.input.Value())))
	if !m.input.InsertImagePlaceholder(2) {
		t.Fatal("InsertImagePlaceholder(2) = false, want true")
	}
	if got := m.input.Value(); got != inlineImagePlaceholderDisplay+"tail"+inlineImagePlaceholderDisplay {
		t.Fatalf("input value after second insert = %q", got)
	}

	oversized := make([]byte, 5*1024*1024+1)
	updated, _ := m.Update(attachmentReadyMsg{attachment: Attachment{FileName: "image2.png", MimeType: "image/png", Data: oversized}, inlineImagePlaceholderRaw: imagePlaceholder(2)})
	model := updated.(*Model)
	if got := model.input.Value(); got != inlineImagePlaceholderDisplay+"tail" {
		t.Fatalf("input value after rollback = %q, want %q", got, inlineImagePlaceholderDisplay+"tail")
	}
	if got := len(model.attachments); got != 1 {
		t.Fatalf("attachments = %d, want 1", got)
	}
	if got := model.attachments[0].FileName; got != "image1.png" {
		t.Fatalf("remaining attachment = %q, want image1.png", got)
	}
	pastes := model.input.InlinePastes()
	if len(pastes) != 1 || pastes[0].RawContent != imagePlaceholder(1) {
		t.Fatalf("inline pastes after rollback = %#v, want only image1 placeholder", pastes)
	}
}

func TestEditingTextReclaimsDeletedInlineImageAttachments(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.attachments = []Attachment{{FileName: "image1.png", MimeType: "image/png", Data: []byte{1}, InlineImagePlaceholder: true}}
	if !m.input.InsertImagePlaceholder(1) {
		t.Fatal("InsertImagePlaceholder(1) = false, want true")
	}

	m.insertComposerText("")
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments before deletion = %d, want 1", got)
	}

	for m.input.Value() != "" {
		_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("attachments after deleting placeholder = %d, want 0", got)
	}
	if m.input.HasInlinePastes() {
		t.Fatal("inline image placeholder should be removed")
	}
}

func TestEditingTextKeepsPathAttachmentWithoutInlinePlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue("attached file")
	m.attachments = []Attachment{{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")}}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'x'}))
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after editing path attachment = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "report.pdf" {
		t.Fatalf("attachment after editing = %q, want report.pdf", got)
	}
}

func TestEditingTextKeepsPathImageWithoutInlinePlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue("attached image")
	m.attachments = []Attachment{{FileName: "path.png", MimeType: "image/png", Data: []byte("png")}}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'x'}))
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after editing path image = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "path.png" {
		t.Fatalf("attachment after editing = %q, want path.png", got)
	}
}

func TestRemovingInlineImagePlaceholderDoesNotRemovePDFBeforeImage(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.attachments = []Attachment{
		{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")},
		{FileName: "image1.png", MimeType: "image/png", Data: []byte("png"), InlineImagePlaceholder: true},
	}
	if !m.input.InsertImagePlaceholder(1) {
		t.Fatal("InsertImagePlaceholder(1) = false, want true")
	}

	for m.input.Value() != "" {
		_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after deleting image placeholder = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "report.pdf" {
		t.Fatalf("remaining attachment = %q, want report.pdf", got)
	}
}

func TestSyncAttachmentsReindexesKeptInlineImagesAfterDroppedMiddleImage(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.attachments = []Attachment{
		{FileName: "image1.png", MimeType: "image/png", Data: []byte("a"), InlineImagePlaceholder: true},
		{FileName: "image2.png", MimeType: "image/png", Data: []byte("b"), InlineImagePlaceholder: true},
		{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")},
		{FileName: "image3.png", MimeType: "image/png", Data: []byte("c"), InlineImagePlaceholder: true},
	}
	text := imagePlaceholder(1) + " keep " + imagePlaceholder(3)
	pastes := []inlineLargePaste{
		{Kind: inlineTokenImage, RawContent: imagePlaceholder(1), DisplayText: inlineImagePlaceholderDisplay, Start: 0, End: len([]rune(imagePlaceholder(1)))},
		{Kind: inlineTokenImage, RawContent: imagePlaceholder(3), DisplayText: inlineImagePlaceholderDisplay, Start: len([]rune(imagePlaceholder(1) + " keep ")), End: len([]rune(text))},
	}
	m.input.SetDisplayValueAndPastes(text, pastes, 4)

	m.syncAttachmentsToInlineImagePlaceholders()
	if got := len(m.attachments); got != 3 {
		t.Fatalf("attachments after sync = %d, want 3", got)
	}
	if got := []string{m.attachments[0].FileName, m.attachments[1].FileName, m.attachments[2].FileName}; got[0] != "image1.png" || got[1] != "report.pdf" || got[2] != "image3.png" {
		t.Fatalf("attachments after sync = %v, want [image1.png report.pdf image3.png]", got)
	}
	inlinePastes := m.input.InlinePastes()
	if got := len(inlinePastes); got != 2 {
		t.Fatalf("inline pastes after sync = %d, want 2", got)
	}
	if inlinePastes[0].RawContent != imagePlaceholder(1) || inlinePastes[1].RawContent != imagePlaceholder(2) {
		t.Fatalf("inline placeholders after sync = [%q %q], want [%q %q]", inlinePastes[0].RawContent, inlinePastes[1].RawContent, imagePlaceholder(1), imagePlaceholder(2))
	}
}

func TestSyncAttachmentsWithoutInlinePastesReclaimsOnlyInlineImageAttachments(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue("still has text")
	m.attachments = []Attachment{
		{FileName: "image1.png", MimeType: "image/png", Data: []byte("png"), InlineImagePlaceholder: true},
		{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")},
		{FileName: "path.png", MimeType: "image/png", Data: []byte("path")},
	}

	m.syncAttachmentsToInlineImagePlaceholders()
	if got := len(m.attachments); got != 2 {
		t.Fatalf("attachments after orphan reclaim = %d, want 2", got)
	}
	if got := []string{m.attachments[0].FileName, m.attachments[1].FileName}; got[0] != "report.pdf" || got[1] != "path.png" {
		t.Fatalf("attachments after orphan reclaim = %v, want [report.pdf path.png]", got)
	}
}

func TestToolCallCopyContentFormatsDoneAsMarkdown(t *testing.T) {
	block := &Block{
		Type:          BlockToolCall,
		ToolName:      "done",
		DoneReport:    "## Completion status\nDone\n\n- Verification passed",
		ResultContent: "Done rejected: coverage is too low\nrequired minimum is 80%",
	}

	got := blockCopyContent(block)
	for _, want := range []string{
		"# Tool call: Done",
		"## Report\n\n## Completion status\nDone",
		"## Rejection reason\n\ncoverage is too low\nrequired minimum is 80%",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("blockCopyContent = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "Done rejected:") {
		t.Fatalf("Done copy content should omit raw rejection prefix, got %q", got)
	}
}

func TestToolCallCopyContentFormatsGenericToolAsMarkdown(t *testing.T) {
	block := &Block{
		Type:          BlockToolCall,
		ToolName:      "shell",
		Content:       `{"command":"echo hi"}`,
		ResultContent: "hi",
	}

	got := blockCopyContent(block)
	for _, want := range []string{
		"# Tool call: shell",
		"## Arguments\n\n```json\n{\"command\":\"echo hi\"}\n```",
		"## Result\n\nhi",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("blockCopyContent = %q, want %q", got, want)
		}
	}
}

func TestMessageCardCopyContentIncludesCardType(t *testing.T) {
	tests := []struct {
		name  string
		block *Block
		want  string
	}{
		{
			name:  "user",
			block: &Block{Type: BlockUser, Content: "Run the focused test."},
			want:  "User:\n\nRun the focused test.",
		},
		{
			name:  "assistant",
			block: &Block{Type: BlockAssistant, Content: "The focused test passed."},
			want:  "Assistant:\n\nThe focused test passed.",
		},
		{
			name: "terminal",
			block: &Block{
				Type:                 BlockUser,
				Content:              "!ls sample.json",
				UserLocalShellCmd:    "ls sample.json",
				UserLocalShellResult: "sample.json\n",
			},
			want: "TERMINAL (!):\n\ncommand:\nls sample.json\n\noutput:\nsample.json",
		},
		{
			name:  "thinking",
			block: &Block{Type: BlockThinking, Content: "  check the failing assertion first.  "},
			want:  "Thinking:\n\ncheck the failing assertion first.",
		},
		{
			name:  "error",
			block: &Block{Type: BlockError, Content: "failed"},
			want:  "Error:\n\nfailed",
		},
		{
			name:  "boundary marker",
			block: &Block{Type: BlockBoundaryMarker, Content: "12 earlier messages"},
			want:  "Boundary:\n\n12 earlier messages",
		},
		{
			name:  "status with title",
			block: &Block{Type: BlockStatus, StatusTitle: "LOOP", Content: "continue working on the failing test"},
			want:  "LOOP:\n\ncontinue working on the failing test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blockCopyContent(tt.block); got != tt.want {
				t.Fatalf("blockCopyContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolCardCopyContentIsSelfContainedMarkdown(t *testing.T) {
	block := &Block{
		Type:          BlockToolCall,
		ToolName:      "grep",
		Content:       `{"pattern":"foo"}`,
		ResultContent: "grep failed: exit status 2",
	}

	got := blockCopyContent(block)
	if strings.Contains(got, "TOOL CALL (grep):") {
		t.Fatalf("tool copy content should not add outer tool label, got %q", got)
	}
	for _, want := range []string{
		"# Tool call: grep",
		"## Arguments\n\n```json\n{\"pattern\":\"foo\"}\n```",
		"## Result\n\ngrep failed: exit status 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("blockCopyContent(tool) = %q, want %q", got, want)
		}
	}
}

func TestToolCopyContentIsIndependentOfCollapsedStateForReadWriteAndApplyPatch(t *testing.T) {
	tests := []struct {
		name  string
		block *Block
	}{
		{
			name: "read",
			block: &Block{
				Type:          BlockToolCall,
				ToolName:      tools.NameRead,
				Content:       `{"path":"sample.go"}`,
				ResultContent: "READ_RESULT lines=1-2 total=2\nfirst\nsecond",
				ResultDone:    true,
			},
		},
		{
			name: "write",
			block: &Block{
				Type:          BlockToolCall,
				ToolName:      tools.NameWrite,
				Content:       `{"path":"sample.go","content":"first\nsecond\n"}`,
				ResultContent: "Successfully wrote 2 lines, 13 bytes",
				ResultDone:    true,
			},
		},
		{
			name: "apply_patch",
			block: &Block{
				Type:          BlockToolCall,
				ToolName:      tools.NameApplyPatch,
				Content:       `{"patch":"*** Begin Patch\n*** Update File: sample.go\n@@\n-old\n+new\n*** End Patch"}`,
				ResultContent: "Applied patch to sample.go (+1 -1)",
				Diff:          "--- sample.go\n+++ sample.go\n@@ -1 +1 @@\n-old\n+new\n",
				ResultDone:    true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collapsed := *tt.block
			collapsed.Collapsed = true
			collapsed.ToolCallDetailExpanded = false

			expanded := *tt.block
			expanded.Collapsed = false
			expanded.ToolCallDetailExpanded = true

			gotCollapsed := blockCopyContent(&collapsed)
			gotExpanded := blockCopyContent(&expanded)
			if gotCollapsed != gotExpanded {
				t.Fatalf("copy content mismatch\ncollapsed:\n%s\n\nexpanded:\n%s", gotCollapsed, gotExpanded)
			}
			for _, forbidden := range []string{"[space] expand", "[space] collapse", "more lines"} {
				if strings.Contains(gotCollapsed, forbidden) {
					t.Fatalf("copy content should not include UI hint %q: %s", forbidden, gotCollapsed)
				}
			}
		})
	}
}

func TestToolErrorCardDisplaysAndCopiesErrorResult(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "web_fetch",
		Content:       `{"raw":false,"timeout_ms":60000,"url":"https://raw.githubusercontent.com/datacurve-ai/pier/main/docs/agents.md"}`,
		ResultContent: "Error: HTTP 404: 404 Not Found",
		ResultStatus:  agent.ToolResultStatusError,
		ResultDone:    true,
		Collapsed:     true,
	}

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	// A short single-line failure already reads fully in its collapsed row, so
	// the card carries no disclosure marker.
	for _, want := range []string{"✗ web_fetch", "Error:", "HTTP 404: 404 Not Found"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered error tool card missing %q; got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, toolDisclosureCollapsed) {
		t.Fatalf("single-line failure should not carry a disclosure marker; got:\n%s", plain)
	}

	got := blockCopyContent(block)
	for _, want := range []string{
		"# Tool call: web_fetch",
		"## Arguments",
		"## Result\n\nError: HTTP 404: 404 Not Found",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("blockCopyContent(error tool) = %q, want %q", got, want)
		}
	}
}

func TestStandaloneToolResultCopyIncludesErrorContent(t *testing.T) {
	block := &Block{
		Type:          BlockToolResult,
		ToolName:      "web_fetch",
		Content:       "Error: HTTP 404: 404 Not Found",
		ResultContent: "Error: HTTP 404: 404 Not Found",
		ResultStatus:  agent.ToolResultStatusError,
		ResultDone:    true,
	}

	got := blockCopyContent(block)
	for _, want := range []string{
		"# Tool result: web_fetch",
		"## Result\n\nError: HTTP 404: 404 Not Found",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("blockCopyContent(standalone result) = %q, want %q", got, want)
		}
	}
}

func TestCopyFocusedBlockHydratesSpilledContent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 6)
	m.mode = ModeNormal
	m.viewport.maxHotBytes = 1024
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 600)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "tail"})

	if !m.viewport.blocks[0].spillCold {
		t.Fatalf("expected block 1 to spill, got spillCold=%v", m.viewport.blocks[0].spillCold)
	}
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlock()
	if cmd == nil {
		t.Fatal("copyFocusedBlock should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Message card copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Message card copied to clipboard")
	}
	block := m.viewport.GetFocusedBlock(1)
	if block == nil || block.spillCold {
		t.Fatalf("focused block after copy = %#v, want hydrated block", block)
	}
	if got := blockPlainContent(block); !strings.Contains(got, "alpha") {
		t.Fatalf("blockPlainContent after copy = %q, want alpha content", got)
	}
}

func TestCopyFocusedBlocksHydratesSpilledBlocks(t *testing.T) {
	m := NewModelWithSize(nil, 80, 6)
	m.mode = ModeNormal
	m.viewport.maxHotBytes = 1024
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 600)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("beta ", 600)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "tail"})

	m.focusedBlockID = 1
	m.refreshBlockFocus()
	cmd := m.copyFocusedBlocks(2)
	if cmd == nil {
		t.Fatal("copyFocusedBlocks should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "2 message cards copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "2 message cards copied to clipboard")
	}
	for _, id := range []int{1, 2} {
		block := m.viewport.GetFocusedBlock(id)
		if block == nil || block.spillCold {
			t.Fatalf("block %d after copy = %#v, want hydrated block", id, block)
		}
	}
}

func TestHandleNormalKeyYyIgnoresMouseSelectionAndCopiesFocusedImageCard(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	block := &Block{
		ID:      1,
		Type:    BlockUser,
		Content: "caption text",
		ImageParts: []BlockImagePart{{
			FileName:        "image1.jpg",
			RenderStartLine: 4,
			RenderEndLine:   6,
		}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = 1
	m.refreshBlockFocus()
	m.selStartBlockID = 1
	m.selStartLine = 4
	m.selStartCol = 0
	m.selEndBlockID = 1
	m.selEndLine = 4
	m.selEndCol = 10

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'})); cmd == nil {
		t.Fatal("first y of yy should start chord command")
	}
	if !m.chord.active() || m.chord.op != chordY {
		t.Fatal("expected first y of yy to start chordY")
	}
	if !m.hasMouseSelection() {
		t.Fatal("expected first y of yy to preserve mouse selection until second y")
	}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("second y of yy should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("yy clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Message card copied to clipboard" {
		t.Fatalf("yy clipboard success = %q, want %q", second.success, "Message card copied to clipboard")
	}

	want := blockCopyContent(block)
	if copied != want {
		t.Fatalf("yy copied = %q, want %q", copied, want)
	}
	if m.hasMouseSelection() {
		t.Fatal("second y of yy must clear the pending mouse selection so a text selection cannot divert a later copy")
	}
}

func TestCopyFocusedBlocksToolCallMatchesSingleBlockFormat(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	tool := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "grep",
		Content:       `{"pattern":"foo"}`,
		ResultContent: "grep failed: exit status 2",
	}
	m.viewport.AppendBlock(tool)
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlocks(2)
	if cmd == nil {
		t.Fatal("copyFocusedBlocks should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Message card copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Message card copied to clipboard")
	}

	want := blockCopyContent(tool)
	if copied != want {
		t.Fatalf("multi-card tool copy mismatch\n got: %q\nwant: %q", copied, want)
	}
	if strings.Contains(copied, "TOOL CALL (grep):") {
		t.Fatalf("multi-card tool copy should not include duplicate outer label, got %q", copied)
	}
}

func TestCopyFocusedBlocksMixedAssistantAndToolKeepsSingleToolHeading(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "Working on it"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockToolCall, ToolName: "grep", Content: `{"pattern":"foo"}`, ResultContent: "grep failed: exit status 2"})
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlocks(2)
	if cmd == nil {
		t.Fatal("copyFocusedBlocks should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "2 message cards copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "2 message cards copied to clipboard")
	}

	if !strings.Contains(copied, blockCopyContent(&Block{Type: BlockAssistant, Content: "Working on it"})) {
		t.Fatalf("mixed copy missing assistant block, got %q", copied)
	}
	if !strings.Contains(copied, "# Tool call: grep") {
		t.Fatalf("mixed copy missing tool markdown heading, got %q", copied)
	}
	if strings.Contains(copied, "TOOL CALL (grep):") {
		t.Fatalf("mixed copy should not include duplicate outer tool label, got %q", copied)
	}
	if !strings.Contains(copied, convformat.BlockSep) {
		t.Fatalf("mixed copy should include block separator, got %q", copied)
	}
}

func TestCopyFocusedBlocksJoinsPerCardCopyRepresentations(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	b1 := &Block{ID: 1, Type: BlockAssistant, Content: "Assistant reply"}
	b2 := &Block{ID: 2, Type: BlockThinking, Content: "Reasoning details"}
	b3 := &Block{ID: 3, Type: BlockToolCall, ToolName: "grep", Content: `{"pattern":"foo"}`, ResultContent: "grep failed: exit status 2"}
	m.viewport.AppendBlock(b1)
	m.viewport.AppendBlock(b2)
	m.viewport.AppendBlock(b3)
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlocks(3)
	if cmd == nil {
		t.Fatal("copyFocusedBlocks should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "3 message cards copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "3 message cards copied to clipboard")
	}

	want := convformat.JoinBlocks([]string{
		blockCopyContent(b1),
		blockCopyContent(b2),
		blockCopyContent(b3),
	})
	if copied != want {
		t.Fatalf("multi-card copy should join per-card copy content\n got: %q\nwant: %q", copied, want)
	}
}

func TestCopyFocusedBlocksPreservesConversationCardTypes(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeNormal
	blocks := []*Block{
		{ID: 1, Type: BlockUser, Content: "Check the file."},
		{ID: 2, Type: BlockAssistant, Content: "I will inspect it."},
		{
			ID:                   3,
			Type:                 BlockUser,
			Content:              "!ls sample.json",
			UserLocalShellCmd:    "ls sample.json",
			UserLocalShellResult: "sample.json\n",
		},
		{ID: 4, Type: BlockAssistant, Content: "The file exists."},
	}
	for _, block := range blocks {
		m.viewport.AppendBlock(block)
	}
	m.focusedBlockID = blocks[0].ID
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlocks(4)
	if cmd == nil {
		t.Fatal("copyFocusedBlocks should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	originalWriteAll := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	t.Cleanup(func() { clipboardWriteAll = originalWriteAll })
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "4 message cards copied to clipboard" {
		t.Fatalf("clipboard success = %q, want 4-card message", second.success)
	}

	want := convformat.JoinBlocks([]string{
		"User:\n\nCheck the file.",
		"Assistant:\n\nI will inspect it.",
		"TERMINAL (!):\n\ncommand:\nls sample.json\n\noutput:\nsample.json",
		"Assistant:\n\nThe file exists.",
	})
	if copied != want {
		t.Fatalf("copied conversation = %q, want %q", copied, want)
	}
}

func TestCopyFocusedBlockCopiesErrorCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "failed"})
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlock()
	if cmd == nil || blockCopyContent(m.viewport.GetFocusedBlock(1)) != "Error:\n\nfailed" {
		t.Fatal("copyFocusedBlock should copy labeled BlockError content")
	}
}

func TestSuperCopyMouseSelectionKeepsLastCharacter(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)
	m.mode = ModeNormal
	block := &Block{ID: 1, Type: BlockAssistant, Content: "prefix `app_id/app_secret` suffix"}
	m.viewport.AppendBlock(block)

	lines := block.Render(m.viewport.width, "")
	target := -1
	startCol := -1
	for i, line := range lines {
		plain := stripANSI(line)
		if before, _, ok := strings.Cut(plain, "app_id/app_secret"); ok {
			target = i
			startCol = ansi.StringWidth(before)
			break
		}
	}
	if target < 0 || startCol < 0 {
		t.Fatalf("failed to find rendered inline code in %#v", lines)
	}

	m.selStartBlockID = 1
	m.selStartLine = target
	m.selStartCol = startCol
	m.selEndBlockID = 1
	m.selEndLine = target
	m.selEndCol = startCol + len("app_id/app_secret") - 1
	m.selEndInclusiveForCopy = true

	cmd := m.handleSuperCopy()
	if cmd == nil {
		t.Fatal("handleSuperCopy should return clipboard command for mouse selection")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Selection copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Selection copied to clipboard")
	}
	if got := m.viewport.ExtractSelectionText(m.mouseSelectionRange()); got != "app_id/app_secret" {
		t.Fatalf("copied selection text = %q, want %q", got, "app_id/app_secret")
	}
}

func TestSuperCopyCopiesErrorCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "failed"})
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.handleSuperCopy()
	if cmd == nil || blockCopyContent(m.viewport.GetFocusedBlock(1)) != "Error:\n\nfailed" {
		t.Fatal("handleSuperCopy should copy labeled BlockError content")
	}
}

func TestNormalModeYankCopiesErrorCardAtViewport(t *testing.T) {
	originalWriteAll := clipboardWriteAll
	defer func() { clipboardWriteAll = originalWriteAll }()
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "failed"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "hello"})
	m.viewport.ScrollToTop()

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'})); cmd == nil {
		t.Fatal("first y should start yank chord")
	}
	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("yy should return clipboard command")
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID = %d, want 1 (copy BlockError)", m.focusedBlockID)
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Message card copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Message card copied to clipboard")
	}
	if copied != "Error:\n\nfailed" {
		t.Fatalf("copied text = %q, want labeled error content", copied)
	}
}

func TestNormalModeCountedYankCopiesVisibleBlocks(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "one"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "two"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "three"})

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'})); cmd == nil {
		t.Fatal("2 should start count prefix")
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'})); cmd == nil {
		t.Fatal("y should start yank chord")
	}
	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("2yy should return clipboard command")
	}
	if m.chord.active() {
		t.Fatal("2yy should clear chord state")
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID = %d, want 1 from viewport top", m.focusedBlockID)
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "2 message cards copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "2 message cards copied to clipboard")
	}
}
