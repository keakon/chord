package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
)

var imagePlaceholderRE = regexp.MustCompile(`\[image(\d+)\]`)

const inlineImagePlaceholderDisplay = "[image]"

func imagePlaceholder(index int) string {
	if index <= 0 {
		index = 1
	}
	return fmt.Sprintf("[image%d]", index)
}

func inlineImagePlaceholderIndex(raw string) (int, bool) {
	m := imagePlaceholderRE.FindStringSubmatch(raw)
	if len(m) != 2 || m[0] != raw {
		return 0, false
	}
	idx, err := strconv.Atoi(m[1])
	if err != nil || idx < 1 {
		return 0, false
	}
	return idx, true
}

func isInlineImagePlaceholderPart(part message.ContentPart) bool {
	if part.Type != "text" {
		return false
	}
	if part.InlineToken != "" && part.InlineToken != inlineImageTokenMarker {
		return false
	}
	if part.InlineToken == "" && part.DisplayText != inlineImagePlaceholderDisplay {
		return false
	}
	_, ok := inlineImagePlaceholderIndex(part.Text)
	return ok
}

func attachmentContentPart(att Attachment) message.ContentPart {
	return message.ContentPart{Type: "image", MimeType: att.MimeType, Data: att.Data, ImagePath: att.ImagePath, FileName: att.FileName}
}

func interleaveImageAttachmentsInTextPart(part message.ContentPart, attachments []Attachment, used []bool) []message.ContentPart {
	if part.Type != "text" || part.Text == "" || message.IsFileRefContent(part.Text) {
		return []message.ContentPart{part}
	}
	if !isInlineImagePlaceholderPart(part) {
		return []message.ContentPart{part}
	}
	imageIndex, ok := inlineImagePlaceholderIndex(part.Text)
	if !ok || imageIndex < 1 || imageIndex > len(attachments) {
		return []message.ContentPart{{Type: "text", Text: part.Text}}
	}
	used[imageIndex-1] = true
	return []message.ContentPart{attachmentContentPart(attachments[imageIndex-1])}
}

// interleaveImageAttachments replaces atomic inline image placeholder parts with
// image content parts in the same positions.
//
// Placeholders:
// - N is 1-based and refers to the Nth pending attachment (current attachments slice order).
// - Only explicit inline image placeholder parts are converted.
// - Unknown/out-of-range placeholders are kept as literal text.
// - Any attachment not referenced by a placeholder is appended to the end.
func interleaveImageAttachments(parts []message.ContentPart, attachments []Attachment) []message.ContentPart {
	if len(parts) == 0 && len(attachments) == 0 {
		return nil
	}

	used := make([]bool, len(attachments))
	out := make([]message.ContentPart, 0, len(parts)+len(attachments))
	for _, part := range parts {
		out = append(out, interleaveImageAttachmentsInTextPart(part, attachments, used)...)
	}
	for i, att := range attachments {
		if used[i] {
			continue
		}
		out = append(out, attachmentContentPart(att))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Model) removeAttachmentForInlinePaste(paste inlineLargePaste) {
	if paste.Kind != inlineTokenImage {
		return
	}
	imageIndex, ok := inlineImagePlaceholderIndex(paste.RawContent)
	if !ok || imageIndex < 1 || imageIndex > len(m.attachments) {
		return
	}
	m.attachments = append(m.attachments[:imageIndex-1], m.attachments[imageIndex:]...)
	m.input.ReindexInlineImagePlaceholdersAfterRemoval(imageIndex)
}

func (m *Model) insertComposerText(text string) tea.Cmd {
	m.input.ClearSelection()
	if !m.input.InsertLargePaste(text) {
		m.input.InsertStringPreserveInlinePastes(text)
	}
	m.input.syncHeight()
	cmd := m.syncAtMentionIfOpen()
	m.recalcViewportSize()
	return cmd
}

func (m *Model) rollbackPendingInlineImagePlaceholder(raw string) tea.Cmd {
	if raw == "" || !m.input.RemoveInlineImagePlaceholderByRaw(raw) {
		return nil
	}
	m.input.syncHeight()
	cmd := m.syncAtMentionIfOpen()
	m.recalcViewportSize()
	return cmd
}

func (m *Model) shouldSuppressDuplicateImagePasteAction(source string) bool {
	now := time.Now()
	if !m.lastImagePasteAt.IsZero() && now.Sub(m.lastImagePasteAt) <= 150*time.Millisecond {
		return source != "" && m.lastImagePasteSource != "" && source != m.lastImagePasteSource
	}
	return false
}

func (m *Model) shouldHandleImagePaste(source string) bool {
	return !m.shouldSuppressDuplicateImagePasteAction(source)
}

func (m *Model) markImagePasteHandled(source string) {
	m.lastImagePasteAt = time.Now()
	m.lastImagePasteSource = source
}

func (m *Model) tryPasteImageIntoComposer(source, pastedText string) tea.Cmd {
	if !m.shouldHandleImagePaste(source) {
		return nil
	}
	if len(m.attachments) >= maxInlineImageAttachments {
		return nil
	}
	img := m.pasteImageFromClipboard()
	if img == nil {
		return nil
	}
	m.markImagePasteHandled(source)
	attach, ok := img.(attachmentReadyMsg)
	if !ok {
		return func() tea.Msg { return img }
	}
	m.input.ClearSelection()
	placeholderRaw := imagePlaceholder(len(m.attachments) + 1)
	m.input.InsertImagePlaceholder(len(m.attachments) + 1)
	var syncCmd tea.Cmd
	if pastedText != "" {
		syncCmd = m.insertComposerText(pastedText)
	} else {
		m.input.syncHeight()
		syncCmd = m.syncAtMentionIfOpen()
		m.recalcViewportSize()
	}
	attach.inlineImagePlaceholderRaw = placeholderRaw
	return tea.Batch(syncCmd, m.handleAttachmentReadyMsg(attach))
}

func (m *Model) handleNonKeyInputMsg(msg tea.Msg) tea.Cmd {
	switch m.mode {
	case ModeInsert:
		// PasteMsg (bracket paste): prefer an image from the system clipboard.
		// Many terminals either emit an empty PasteMsg for non-text clipboard
		// content, or paste a textual representation. We do NOT auto-convert
		// pasted paths/URIs into attachments.
		if pm, ok := msg.(tea.PasteMsg); ok {
			if m.shouldSuppressDuplicateImagePasteAction("paste") {
				return nil
			}
			if cmd := m.tryPasteImageIntoComposer("paste", pm.Content); cmd != nil {
				return cmd
			}
			if strings.TrimSpace(pm.Content) == "" {
				return pasteTextFromClipboard()
			}
			return m.insertComposerText(pm.Content)
		}
		return m.input.Update(msg)
	case ModeConfirm:
		// Bubble Tea v2 may deliver terminal paste as tea.PasteMsg (bracketed paste),
		// not as a KeyMsg (Super+V). The bubbles/textarea component doesn't handle
		// tea.PasteMsg, so we must handle it here.
		//
		// Confirm dialogs are text editors; we do not attach images here.
		// If the terminal emits an empty PasteMsg for non-text clipboard content,
		// fall back to reading the textual clipboard content.
		if pm, ok := msg.(tea.PasteMsg); ok {
			if strings.TrimSpace(pm.Content) == "" {
				return pasteTextFromClipboard()
			}
			if input, _, ok := m.activeConfirmTextarea(); ok {
				input.InsertString(pm.Content)
				m.recalcViewportSize()
				return nil
			}
		}
		if input, _, ok := m.activeConfirmTextarea(); ok {
			updated, cmd := input.Update(msg)
			*input = updated
			return cmd
		}
	case ModeHandoffSelect:
		if m.handoffSelect.denyingWithReason {
			if pm, ok := msg.(tea.PasteMsg); ok {
				if strings.TrimSpace(pm.Content) == "" {
					return pasteTextFromClipboard()
				}
				m.handoffSelect.denyReasonInput.InsertString(pm.Content)
				m.recalcViewportSize()
				return nil
			}
			var cmd tea.Cmd
			m.handoffSelect.denyReasonInput, cmd = m.handoffSelect.denyReasonInput.Update(msg)
			return cmd
		}
	case ModeQuestion:
		if m.question.custom || (m.question.request != nil &&
			m.question.currentQ < len(m.question.request.Questions) &&
			len(m.question.request.Questions[m.question.currentQ].Options) == 0) {
			var cmd tea.Cmd
			m.question.input, cmd = m.question.input.Update(msg)
			return cmd
		}
	case ModeSearch:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return cmd
	}
	return nil
}
