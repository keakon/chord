package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/imageutil"
)

type clipboardWriteResultMsg struct {
	success string
	err     error
}

type clipboardTextMsg string

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

func (m *Model) pasteImageFromClipboard() tea.Msg {
	data, mimeType, err := readImageFromClipboard()
	if err != nil {
		return nil
	}
	imageName := fmt.Sprintf("image%d%s", m.nextInlineImageOrdinal(), attachmentExtForMimeType(mimeType))
	return attachmentReadyMsg{attachment: Attachment{
		FileName: imageName,
		MimeType: mimeType,
		Data:     data,
	}}
}

func (m *Model) pasteFromClipboard() tea.Cmd {
	return func() tea.Msg {
		return readClipboardTextMsg()
	}
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
