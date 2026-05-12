package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

type clipboardWriteResultMsg struct {
	success string
	err     error
}

type clipboardTextMsg string

var clipboardWriteAll = clipboard.WriteAll

func writeClipboardCmd(text, success string) tea.Cmd {
	return tea.Sequence(
		tea.SetClipboard(text),
		func() tea.Msg {
			err := clipboardWriteAll(text)
			return clipboardWriteResultMsg{success: success, err: err}
		},
	)
}

func attachmentFromImagePath(path string, index int) (Attachment, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return Attachment{}, fmt.Errorf("empty image path")
	}
	data, mimeType, err := readImageFile(cleanPath)
	if err != nil {
		return Attachment{}, err
	}
	fileName := filepath.Base(cleanPath)
	if fileName == "." || fileName == string(filepath.Separator) || strings.TrimSpace(fileName) == "" {
		fileName = fmt.Sprintf("image%d%s", index, attachmentExtForMimeType(mimeType))
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
		return attachmentReadyMsg{err: fmt.Errorf("failed to read image: %w", err)}
	}
	return attachmentReadyMsg{attachment: attachment}
}

func (m *Model) pasteFromClipboard() tea.Cmd {
	return func() tea.Msg {
		data, mimeType, err := readImageFromClipboard()
		if err == nil {
			imageName := fmt.Sprintf("image%d%s", len(m.attachments)+1, attachmentExtForMimeType(mimeType))
			return attachmentReadyMsg{attachment: Attachment{
				FileName: imageName,
				MimeType: mimeType,
				Data:     data,
			}}
		}
		text, err := clipboard.ReadAll()
		if err != nil || text == "" {
			return nil
		}
		return clipboardTextMsg(text)
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
