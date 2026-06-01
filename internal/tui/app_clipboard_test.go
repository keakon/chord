package tui

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
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
	if got := m.input.Value(); got != inlineImagePlaceholderDisplay+"hello" {
		t.Fatalf("input value = %q, want %q", got, inlineImagePlaceholderDisplay+"hello")
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
	if got := m.input.Value(); got != inlineImagePlaceholderDisplay {
		t.Fatalf("input value = %q, want %q", got, inlineImagePlaceholderDisplay)
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
	if got := m.input.Value(); got != inlineImagePlaceholderDisplay {
		t.Fatalf("input value after duplicate PasteMsg = %q, want %q", got, inlineImagePlaceholderDisplay)
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
	if got := m.input.Value(); got != inlineImagePlaceholderDisplay+"clipboard text" {
		t.Fatalf("input value after PasteMsg = %q, want %q", got, inlineImagePlaceholderDisplay+"clipboard text")
	}

	cmd = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	if cmd != nil {
		t.Fatalf("expected deduplicated ctrl+v to return no command, got %T", cmd)
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("attachments after duplicate ctrl+v = %d, want 1", got)
	}
	if got := m.input.Value(); got != inlineImagePlaceholderDisplay+"clipboard text" {
		t.Fatalf("input value after duplicate ctrl+v = %q, want %q", got, inlineImagePlaceholderDisplay+"clipboard text")
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
