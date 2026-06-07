package tui

import (
	"fmt"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
)

type attachmentReadyMsg struct {
	attachment                Attachment
	err                       error
	inlineImagePlaceholderRaw string
}

func (m *Model) handleAttachmentReadyMsg(msg attachmentReadyMsg) tea.Cmd {
	if msg.err != nil {
		return tea.Batch(m.rollbackPendingInlineImagePlaceholder(msg.inlineImagePlaceholderRaw), m.enqueueToast(msg.err.Error(), "error"))
	}
	if len(m.attachments) >= maxInlineImageAttachments {
		return tea.Batch(m.rollbackPendingInlineImagePlaceholder(msg.inlineImagePlaceholderRaw), m.enqueueToast(fmt.Sprintf("max %d attachments supported", maxInlineImageAttachments), "warn"))
	}
	isPDF := msg.attachment.MimeType == "application/pdf"
	maxBytes := imageutil.MaxImageBytes
	if isPDF {
		maxBytes = imageutil.MaxPDFBytes
	}
	if len(msg.attachment.Data) > maxBytes {
		kind := "Image"
		if isPDF {
			kind = "PDF"
		}
		return tea.Batch(m.rollbackPendingInlineImagePlaceholder(msg.inlineImagePlaceholderRaw), m.enqueueToast(fmt.Sprintf("%s exceeds %d MB limit", kind, maxBytes/1024/1024), "warn"))
	}
	msg.attachment = m.markAttachmentSupport(msg.attachment)
	if msg.attachment.SizeBytes == 0 {
		msg.attachment.SizeBytes = len(msg.attachment.Data)
	}
	m.attachments = append(m.attachments, msg.attachment)
	m.recalcViewportSize()
	var cmds []tea.Cmd
	cmds = append(cmds, m.enqueueToast(fmt.Sprintf("Attachment added: %s", msg.attachment.FileName), "info"))
	if warning := m.unsupportedAttachmentWarning(msg.attachment); warning != "" {
		cmds = append(cmds, m.enqueueToast(warning, "warn"))
	}
	if msg.attachment.Encrypted {
		cmds = append(cmds, m.enqueueToast("PDF appears to be encrypted and may not be readable by the model: "+msg.attachment.FileName, "warn"))
	}
	return tea.Batch(cmds...)
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
		return b.Content + "\n\n(terminal running…)"
	}
	return convformat.UserShellReadableBody(b.Content, b.UserLocalShellCmd, b.UserLocalShellResult, b.UserLocalShellFailed)
}

// pickImageFile reads an image file whose path is currently selected/typed in the input.
// If the input value looks like a file path, try to read it as an image.
func (m *Model) pickImageFile() tea.Cmd {
	path := strings.TrimSpace(m.input.Value())
	if path == "" {
		return m.enqueueToast("Enter an image path in the input box, then use a custom insert_attach_file binding", "info")
	}
	return func() tea.Msg {
		return pasteAttachmentFromPath(path, len(m.attachments)+1)
	}
}
