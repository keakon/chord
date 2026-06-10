package tui

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
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

func stubClipboardImageReadError(t *testing.T) {
	t.Helper()
	orig := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return nil, "", errors.New("no image")
	}
	t.Cleanup(func() { readImageFromClipboard = orig })
}

func TestHandleNonKeyInputMsgKeepsImagePathPasteAsText(t *testing.T) {
	path := writeTinyPNG(t)
	stubClipboardImageReadError(t)
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
}

func TestPasteMsgPrefersClipboardImageOverText(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	orig := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	t.Cleanup(func() { readImageFromClipboard = orig })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleNonKeyInputMsg(tea.PasteMsg{Content: "hello"})
	if cmd == nil {
		t.Fatal("expected paste handler to return image-added toast cmd")
	}
	if got := m.input.Value(); got != "[image1.png]hello" {
		t.Fatalf("input value = %q, want %q", got, "[image1.png]hello")
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments = %d, want 1", got)
	}
	if attach := m.attachments[0]; attach.MimeType != "image/png" {
		t.Fatalf("attachment mime = %q, want image/png", attach.MimeType)
	}
}

func TestInsertAttachClipboardPrefersClipboardImageOverText(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	origImage := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return "fallback text", nil
	}
	t.Cleanup(func() {
		readImageFromClipboard = origImage
		clipboardReadAll = origText
	})

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected ctrl+v to return image-added toast cmd")
	}
	if got := m.input.Value(); got != "[image1.png]" {
		t.Fatalf("input value = %q, want %q", got, "[image1.png]")
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments = %d, want 1", got)
	}
	if attach := m.attachments[0]; attach.MimeType != "image/png" {
		t.Fatalf("attachment mime = %q, want image/png", attach.MimeType)
	}
}

func TestPasteImageDeduplicatesKeyAndPasteMsg(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	origImage := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return "fallback text", nil
	}
	t.Cleanup(func() {
		readImageFromClipboard = origImage
		clipboardReadAll = origText
	})

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected ctrl+v to return image-added toast cmd")
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after ctrl+v = %d, want 1", got)
	}

	cmd = m.handleNonKeyInputMsg(tea.PasteMsg{Content: "clipboard text"})
	if cmd != nil {
		t.Fatalf("expected deduplicated PasteMsg to return no command, got %T", cmd)
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after duplicate PasteMsg = %d, want 1", got)
	}
	if got := m.input.Value(); got != "[image1.png]" {
		t.Fatalf("input value after duplicate PasteMsg = %q, want %q", got, "[image1.png]")
	}
}

func TestPasteImageDeduplicatesPasteMsgAndKey(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	origImage := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return "fallback text", nil
	}
	t.Cleanup(func() {
		readImageFromClipboard = origImage
		clipboardReadAll = origText
	})

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleNonKeyInputMsg(tea.PasteMsg{Content: "clipboard text"})
	if cmd == nil {
		t.Fatal("expected PasteMsg to return image-added toast cmd")
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after PasteMsg = %d, want 1", got)
	}
	if got := m.input.Value(); got != "[image1.png]clipboard text" {
		t.Fatalf("input value after PasteMsg = %q, want %q", got, "[image1.png]clipboard text")
	}

	cmd = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd != nil {
		t.Fatalf("expected deduplicated ctrl+v to return no command, got %T", cmd)
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after duplicate ctrl+v = %d, want 1", got)
	}
	if got := m.input.Value(); got != "[image1.png]clipboard text" {
		t.Fatalf("input value after duplicate ctrl+v = %q, want %q", got, "[image1.png]clipboard text")
	}
}

func TestPasteImageSecondPasteAddsOneAttachment(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	origImage := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return data, "image/jpeg", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return "clipboard text", nil
	}
	t.Cleanup(func() {
		readImageFromClipboard = origImage
		clipboardReadAll = origText
	})

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after first ctrl+v = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "image1.jpg" {
		t.Fatalf("first attachment name = %q, want image1.jpg", got)
	}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
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

	origImage := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	t.Cleanup(func() { readImageFromClipboard = origImage })

	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.attachments = []Attachment{{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")}}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
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

func TestInsertAttachClipboardThenEnterSendsImageImmediately(t *testing.T) {
	path := writeTinyPNG(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tiny png: %v", err)
	}

	origImage := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return data, "image/png", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return "fallback text", nil
	}
	t.Cleanup(func() {
		readImageFromClipboard = origImage
		clipboardReadAll = origText
	})

	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after ctrl+v = %d, want 1", got)
	}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if got := len(backend.sentMultipart); got != 1 {
		t.Fatalf("SendUserMessageWithParts() calls = %d, want 1", got)
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
	origImage := readImageFromClipboard
	readImageFromClipboard = func() ([]byte, string, error) {
		return []byte{0x89, 'P', 'N', 'G'}, "image/png", nil
	}
	origText := clipboardReadAll
	clipboardReadAll = func() (string, error) {
		return `{"command":"echo pasted"}`, nil
	}
	t.Cleanup(func() {
		readImageFromClipboard = origImage
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
