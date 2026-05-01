package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
)

type attachmentReadyMsg struct {
	attachment Attachment
	err        error
}

// shellBangResultMsg carries output from a local !command (TUI shell).
type shellBangResultMsg struct {
	userLine string // full input e.g. "!ls"
	cmd      string // command after ! e.g. "ls"
	output   string
	err      error
	agentID  string
	blockID  int // merged USER block to update
}

func shellBangCmd(workDir, userLine, bashLine, agentID string, blockID int) tea.Cmd {
	return func() tea.Msg {
		out, err := runBangShell(workDir, bashLine)
		return shellBangResultMsg{userLine: userLine, cmd: bashLine, output: out, err: err, agentID: agentID, blockID: blockID}
	}
}

func localShellContextMessage(userLine, cmd, output string, err error) message.Message {
	failed := err != nil
	readable := convformat.UserShellReadableBody(userLine, cmd, output, failed)
	persisted := convformat.UserShellPersistedBody(userLine, cmd, output, failed)
	return message.Message{
		Role:    "user",
		Content: convformat.BlockString(convformat.LabelUser, persisted),
		Parts: []message.ContentPart{{
			Type: "text",
			Text: convformat.BlockString(convformat.LabelUser, readable),
		}},
	}
}

func userLocalShellCopyBody(b *Block) string {
	if b.UserLocalShellPending {
		return b.Content + "\n\n(local shell running…)"
	}
	return convformat.UserShellReadableBody(b.Content, b.UserLocalShellCmd, b.UserLocalShellResult, b.UserLocalShellFailed)
}

// pickImageFile reads an image file whose path is currently selected/typed in the input.
// If the input value looks like a file path, try to read it as an image.
func (m *Model) pickImageFile() tea.Cmd {
	path := strings.TrimSpace(m.input.Value())
	if path == "" {
		return m.enqueueToast("Enter image path in the input box then press ctrl+f", "info")
	}
	return func() tea.Msg {
		data, mimeType, err := readImageFile(path)
		if err != nil {
			return attachmentReadyMsg{err: fmt.Errorf("failed to read image: %w", err)}
		}
		return attachmentReadyMsg{attachment: Attachment{
			FileName:  filepath.Base(path),
			MimeType:  mimeType,
			Data:      data,
			ImagePath: path,
		}}
	}
}
