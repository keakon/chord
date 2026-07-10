package tui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	clipboard "golang.design/x/clipboard"
	"golang.org/x/image/bmp"

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

func TestHandleNonKeyInputMsgKeepsImagePathPasteAsText(t *testing.T) {
	path := writeTinyPNG(t)
	attachmentReads := 0
	orig := readAttachmentFromClipboard
	readAttachmentFromClipboard = func() ([]byte, string, error) {
		attachmentReads++
		return nil, "", errNoClipboardAttachment
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

func TestReadAttachmentFromClipboardFallsBackToBMP(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(1, 1, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var encoded bytes.Buffer
	if err := bmp.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}

	bmpFormat := clipboard.Register("image/bmp")
	origInit := clipboardInit
	origFormats := clipboardFormats
	origRead := clipboardRead
	clipboardInit = func() error { return nil }
	clipboardFormats = func() []clipboard.Format { return []clipboard.Format{bmpFormat} }
	clipboardRead = func(format clipboard.Format) []byte {
		if format == bmpFormat {
			return encoded.Bytes()
		}
		return nil
	}
	t.Cleanup(func() {
		clipboardInit = origInit
		clipboardFormats = origFormats
		clipboardRead = origRead
	})

	data, mimeType, err := readAttachmentFromClipboardImpl()
	if err != nil {
		t.Fatalf("readAttachmentFromClipboardImpl: %v", err)
	}
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		t.Fatalf("mime type = %q, want normalized PNG/JPEG", mimeType)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("normalized clipboard BMP is not decodable: %v", err)
	}
}

func TestReadAttachmentFromClipboardPrefersPNGOverOtherImageMIMEs(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, nil); err != nil {
		t.Fatal(err)
	}

	pngFormat := clipboard.Register("image/png")
	jpegFormat := clipboard.Register("image/jpeg")
	origInit := clipboardInit
	origFormats := clipboardFormats
	origRead := clipboardRead
	clipboardInit = func() error { return nil }
	clipboardFormats = func() []clipboard.Format { return []clipboard.Format{jpegFormat, pngFormat} }
	clipboardRead = func(format clipboard.Format) []byte {
		switch format {
		case pngFormat:
			return pngBuf.Bytes()
		case jpegFormat:
			return jpegBuf.Bytes()
		default:
			return nil
		}
	}
	t.Cleanup(func() {
		clipboardInit = origInit
		clipboardFormats = origFormats
		clipboardRead = origRead
	})

	data, mimeType, err := readAttachmentFromClipboardImpl()
	if err != nil {
		t.Fatalf("readAttachmentFromClipboardImpl: %v", err)
	}
	if len(data) == 0 || (mimeType != "image/png" && mimeType != "image/jpeg") {
		t.Fatalf("read clipboard PNG = %d bytes, %q", len(data), mimeType)
	}
}

func TestReadAttachmentFromClipboardFallsBackWhenFmtImageIsInvalid(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, nil); err != nil {
		t.Fatal(err)
	}

	jpegFormat := clipboard.Register("image/jpeg")
	origInit := clipboardInit
	origFormats := clipboardFormats
	origRead := clipboardRead
	clipboardInit = func() error { return nil }
	clipboardFormats = func() []clipboard.Format { return []clipboard.Format{clipboard.FmtImage, jpegFormat} }
	clipboardRead = func(format clipboard.Format) []byte {
		if format == clipboard.FmtImage {
			return []byte("invalid PNG")
		}
		if format == jpegFormat {
			return jpegBuf.Bytes()
		}
		return nil
	}
	t.Cleanup(func() {
		clipboardInit = origInit
		clipboardFormats = origFormats
		clipboardRead = origRead
	})

	data, mimeType, err := readAttachmentFromClipboardImpl()
	if err != nil {
		t.Fatalf("readAttachmentFromClipboardImpl: %v", err)
	}
	if mimeType != "image/jpeg" || !bytes.Equal(data, jpegBuf.Bytes()) {
		t.Fatalf("clipboard fallback = %d bytes, %q; want original JPEG", len(data), mimeType)
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
		return nil, "", errNoClipboardAttachment
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
