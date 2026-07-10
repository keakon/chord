package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/imageutil"
)

type clipboardWriteResultMsg struct {
	success string
	err     error
}

type clipboardTextMsg string

type clipboardAttachmentReadyMsg struct {
	requestID uint64
	agentID   string
	data      []byte
	mimeType  string
	err       error
}

var (
	clipboardReadAll  = clipboard.ReadAll
	clipboardWriteAll = clipboard.WriteAll
)

func writeClipboardCmd(text, success string) tea.Cmd {
	return tea.Sequence(
		tea.SetClipboard(text),
		func() tea.Msg {
			err := clipboardWriteAll(text)
			return clipboardWriteResultMsg{success: success, err: err}
		},
	)
}

func readClipboardTextMsg() tea.Msg {
	text, err := clipboardReadAll()
	if err != nil || text == "" {
		return nil
	}
	return clipboardTextMsg(text)
}

func pasteTextFromClipboard() tea.Cmd {
	return func() tea.Msg {
		return readClipboardTextMsg()
	}
}

func attachmentFromImagePath(path string, index int) (Attachment, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return Attachment{}, fmt.Errorf("empty attachment path")
	}
	data, mimeType, err := imageutil.ReadAttachmentFile(cleanPath)
	if err != nil {
		return Attachment{}, err
	}
	fileName := filepath.Base(cleanPath)
	if fileName == "." || fileName == string(filepath.Separator) || strings.TrimSpace(fileName) == "" {
		fileName = fmt.Sprintf("attachment%d%s", index, attachmentExtForMimeType(mimeType))
	}
	return Attachment{
		FileName:  fileName,
		MimeType:  mimeType,
		Data:      data,
		ImagePath: cleanPath,
	}, nil
}

func pasteAttachmentFromPath(path string, index int) tea.Msg {
	attachment, err := attachmentFromImagePath(path, index)
	if err != nil {
		return attachmentReadyMsg{err: fmt.Errorf("failed to read attachment: %w", err)}
	}
	return attachmentReadyMsg{attachment: attachment}
}

func (m *Model) pasteAttachmentFromClipboard() tea.Cmd {
	if m.clipboardAttachmentPending {
		return m.enqueueToast("Already reading a clipboard attachment", "info")
	}
	if len(m.attachments) >= maxInlineImageAttachments {
		return m.enqueueToast(fmt.Sprintf("max %d attachments supported", maxInlineImageAttachments), "warn")
	}

	m.clipboardAttachmentSeq++
	requestID := m.clipboardAttachmentSeq
	agentID := m.focusedAgentID
	m.clipboardAttachmentPending = true
	m.clipboardAttachmentAgentID = agentID
	m.clipboardPasteSuppressUntil = time.Now().Add(150 * time.Millisecond)
	return func() tea.Msg {
		data, mimeType, err := readAttachmentFromClipboard()
		return clipboardAttachmentReadyMsg{
			requestID: requestID,
			agentID:   agentID,
			data:      data,
			mimeType:  mimeType,
			err:       err,
		}
	}
}

func (m *Model) cancelClipboardAttachmentPaste() {
	if !m.clipboardAttachmentPending {
		return
	}
	m.clipboardAttachmentPending = false
	m.clipboardAttachmentAgentID = ""
	m.clipboardAttachmentSeq++
}

func (m *Model) handleClipboardAttachmentReady(msg clipboardAttachmentReadyMsg) tea.Cmd {
	if !m.clipboardAttachmentPending || msg.requestID != m.clipboardAttachmentSeq {
		return nil
	}
	m.clipboardAttachmentPending = false
	m.clipboardAttachmentAgentID = ""
	if msg.agentID != m.focusedAgentID {
		return m.enqueueToast("Clipboard attachment was not added because the active composer changed", "warn")
	}
	if msg.err != nil {
		return m.enqueueToast(msg.err.Error(), "warn")
	}

	attachment := Attachment{MimeType: msg.mimeType, Data: msg.data}
	if msg.mimeType == "application/pdf" {
		attachment.FileName = fmt.Sprintf("attachment%d.pdf", len(m.attachments)+1)
		return m.handleAttachmentReadyMsg(attachmentReadyMsg{attachment: attachment})
	}

	imageOrdinal := m.nextInlineImageOrdinal()
	attachment.FileName = fmt.Sprintf("image%d%s", imageOrdinal, attachmentExtForMimeType(msg.mimeType))
	m.input.ClearSelection()
	placeholderRaw := imagePlaceholder(imageOrdinal)
	m.input.InsertImagePlaceholderWithDisplay(imageOrdinal, attachmentReferenceText(attachment))
	m.input.syncHeight()
	syncCmd := m.syncAtMentionIfOpen()
	m.recalcViewportSize()
	return tea.Batch(syncCmd, m.handleAttachmentReadyMsg(attachmentReadyMsg{
		attachment:                attachment,
		inlineImagePlaceholderRaw: placeholderRaw,
	}))
}

func writeStatusPathClipboardCmd(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return writeClipboardCmd(filepath.Clean(text), "Path copied to clipboard")
}

func writeStatusSessionClipboardCmd(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return writeClipboardCmd(text, "Session ID copied to clipboard")
}
