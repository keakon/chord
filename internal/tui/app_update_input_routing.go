package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
)

var imagePlaceholderRE = regexp.MustCompile(`\[image(\d+)\]`)

const inlineImagePlaceholderDisplay = "[image]"

func attachmentReferenceText(att Attachment) string {
	name := strings.TrimSpace(att.FileName)
	if name == "" {
		name = "attachment"
	}
	return "[" + name + "]"
}

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
	if part.Type != message.ContentPartText {
		return false
	}
	if part.InlineToken != "" && part.InlineToken != inlineImageTokenMarker {
		return false
	}
	if part.InlineToken == "" && part.DisplayText == "" {
		return false
	}
	_, ok := inlineImagePlaceholderIndex(part.Text)
	return ok
}

func attachmentPartType(mimeType string) message.ContentPartType {
	if mimeType == "application/pdf" {
		return message.ContentPartPDF
	}
	return message.ContentPartImage
}

func attachmentContentPart(att Attachment) message.ContentPart {
	return message.ContentPart{Type: attachmentPartType(att.MimeType), MimeType: att.MimeType, Data: att.Data, ImagePath: att.ImagePath, FileName: att.FileName}
}

func interleaveAttachmentsInTextPart(part message.ContentPart, attachments []Attachment, used []bool) []message.ContentPart {
	if part.Type != message.ContentPartText || part.Text == "" || message.IsFileRefContent(part.Text) {
		return []message.ContentPart{part}
	}
	if !isInlineImagePlaceholderPart(part) {
		return interleavePlainImagePlaceholdersInTextPart(part, attachments, used)
	}
	imageIndex, ok := inlineImagePlaceholderIndex(part.Text)
	if !ok {
		return []message.ContentPart{{Type: message.ContentPartText, Text: part.Text}}
	}
	attachmentIndex, ok := imageAttachmentIndex(attachments, imageIndex)
	if !ok {
		return []message.ContentPart{{Type: message.ContentPartText, Text: part.Text}}
	}
	used[attachmentIndex] = true
	return []message.ContentPart{attachmentContentPart(attachments[attachmentIndex])}
}

func interleavePlainImagePlaceholdersInTextPart(part message.ContentPart, attachments []Attachment, used []bool) []message.ContentPart {
	if out, ok := interleaveAttachmentReferencePlaceholdersInTextPart(part, attachments, used); ok {
		return out
	}
	placeholderCount := strings.Count(part.Text, inlineImagePlaceholderDisplay)
	if placeholderCount == 0 {
		return []message.ContentPart{part}
	}
	if placeholderCount != countUnusedInlineImageAttachments(attachments, used) {
		return []message.ContentPart{part}
	}
	segments := strings.Split(part.Text, inlineImagePlaceholderDisplay)
	out := make([]message.ContentPart, 0, len(segments)*2-1)
	for i, segment := range segments {
		if segment != "" {
			out = append(out, message.ContentPart{Type: message.ContentPartText, Text: segment})
		}
		if i == len(segments)-1 {
			break
		}
		attachmentIndex, ok := nextUnusedImageAttachmentIndex(attachments, used)
		if !ok {
			out = append(out, message.ContentPart{Type: message.ContentPartText, Text: inlineImagePlaceholderDisplay})
			continue
		}
		used[attachmentIndex] = true
		out = append(out, attachmentContentPart(attachments[attachmentIndex]))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func interleaveAttachmentReferencePlaceholdersInTextPart(part message.ContentPart, attachments []Attachment, used []bool) ([]message.ContentPart, bool) {
	type ref struct {
		index int
		text  string
		pos   int
	}
	refs := make([]ref, 0, len(attachments))
	for i, att := range attachments {
		if used[i] {
			continue
		}
		text := attachmentReferenceText(att)
		pos := strings.Index(part.Text, text)
		if pos < 0 {
			continue
		}
		if strings.Count(part.Text, text) != 1 {
			return nil, false
		}
		refs = append(refs, ref{index: i, text: text, pos: pos})
	}
	if len(refs) == 0 {
		return nil, false
	}
	slices.SortFunc(refs, func(a, b ref) int { return a.pos - b.pos })
	out := make([]message.ContentPart, 0, len(refs)*2+1)
	cursor := 0
	for _, r := range refs {
		if r.pos < cursor {
			return nil, false
		}
		if r.pos > cursor {
			out = append(out, message.ContentPart{Type: message.ContentPartText, Text: part.Text[cursor:r.pos]})
		}
		used[r.index] = true
		out = append(out, attachmentContentPart(attachments[r.index]))
		cursor = r.pos + len(r.text)
	}
	if cursor < len(part.Text) {
		out = append(out, message.ContentPart{Type: message.ContentPartText, Text: part.Text[cursor:]})
	}
	return out, true
}

func countUnusedInlineImageAttachments(attachments []Attachment, used []bool) int {
	count := 0
	for i, att := range attachments {
		if used[i] || !att.InlineImagePlaceholder || attachmentPartType(att.MimeType) != message.ContentPartImage {
			continue
		}
		count++
	}
	return count
}

func nextUnusedImageAttachmentIndex(attachments []Attachment, used []bool) (int, bool) {
	for i, att := range attachments {
		if used[i] || !att.InlineImagePlaceholder || attachmentPartType(att.MimeType) != message.ContentPartImage {
			continue
		}
		return i, true
	}
	return 0, false
}

// interleaveImageAttachments replaces inline image placeholders with image
// content parts in the same positions.
//
// Placeholders:
//   - N is 1-based and refers to the Nth pending attachment (current attachments slice order).
//   - Explicit inline image placeholder parts are converted.
//   - Plain text placeholders are converted only when their count exactly matches
//     the unused inline image attachments, avoiding ambiguous literal [image] text.
//   - Unknown/out-of-range placeholders are kept as literal text.
//   - Any attachment not referenced by a placeholder is appended to the end.
func interleaveAttachments(parts []message.ContentPart, attachments []Attachment) []message.ContentPart {
	if len(parts) == 0 && len(attachments) == 0 {
		return nil
	}

	used := make([]bool, len(attachments))
	out := make([]message.ContentPart, 0, len(parts)+len(attachments))
	for _, part := range parts {
		out = append(out, interleaveAttachmentsInTextPart(part, attachments, used)...)
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

func imageAttachmentIndex(attachments []Attachment, imageOrdinal int) (int, bool) {
	if imageOrdinal < 1 {
		return 0, false
	}
	seen := 0
	for idx, att := range attachments {
		if !att.InlineImagePlaceholder {
			continue
		}
		seen++
		if seen == imageOrdinal {
			return idx, true
		}
	}
	return 0, false
}

func (m *Model) removeAttachmentForInlinePaste(paste inlineLargePaste) {
	if paste.Kind != inlineTokenImage {
		return
	}
	imageIndex, ok := inlineImagePlaceholderIndex(paste.RawContent)
	if !ok {
		return
	}
	attachmentIndex, ok := imageAttachmentIndex(m.attachments, imageIndex)
	if !ok {
		return
	}
	m.attachments = append(m.attachments[:attachmentIndex], m.attachments[attachmentIndex+1:]...)
	m.input.ReindexInlineImagePlaceholdersAfterRemoval(imageIndex)
}

func (m *Model) syncAttachmentsToInlineImagePlaceholders() {
	if len(m.attachments) == 0 {
		return
	}
	usedImages := make(map[int]bool)
	for _, paste := range m.input.InlinePastes() {
		if paste.Kind != inlineTokenImage {
			continue
		}
		imageIndex, ok := inlineImagePlaceholderIndex(paste.RawContent)
		if !ok {
			continue
		}
		attachmentIndex, ok := imageAttachmentIndex(m.attachments, imageIndex)
		if !ok {
			continue
		}
		usedImages[attachmentIndex] = true
	}
	mapping := make(map[int]int, len(m.attachments))
	kept := make([]Attachment, 0, len(m.attachments))
	oldImageOrdinal := 0
	newImageOrdinal := 0
	for idx, att := range m.attachments {
		if att.InlineImagePlaceholder {
			oldImageOrdinal++
		}
		if att.InlineImagePlaceholder && !usedImages[idx] {
			continue
		}
		kept = append(kept, att)
		if att.InlineImagePlaceholder {
			newImageOrdinal++
			mapping[oldImageOrdinal] = newImageOrdinal
		}
	}
	m.attachments = kept
	m.input.ReindexInlineImagePlaceholders(mapping)
}

func (m *Model) insertComposerText(text string) tea.Cmd {
	m.input.ClearSelection()
	if !m.input.InsertLargePaste(text) {
		m.input.InsertStringPreserveInlinePastes(text)
	}
	m.syncAttachmentsToInlineImagePlaceholders()
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

func (m *Model) nextInlineImageOrdinal() int {
	ordinal := 1
	for _, att := range m.attachments {
		if att.InlineImagePlaceholder {
			ordinal++
		}
	}
	return ordinal
}

func (m *Model) handleNonKeyInputMsg(msg tea.Msg) tea.Cmd {
	switch m.mode {
	case ModeInsert:
		// Terminal paste events are text-only. Clipboard attachments use the
		// explicit insert_attach_clipboard binding (Ctrl+V by default).
		if pm, ok := msg.(tea.PasteMsg); ok {
			suppress := time.Now().Before(m.clipboardPasteSuppressUntil)
			m.clipboardPasteSuppressUntil = time.Time{}
			if suppress {
				return nil
			}
			if strings.TrimSpace(pm.Content) == "" {
				return nil
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
