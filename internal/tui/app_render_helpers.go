package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/keakon/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

const maxQueuedToasts = 10

func renderDisabledInputArea(content string) string {
	if content == "" {
		return ""
	}
	trailingNewline := strings.HasSuffix(content, "\n")
	body := strings.TrimSuffix(content, "\n")
	if body == "" {
		return content
	}
	rendered := InputBoxDimmedStyle.Render(body)
	if trailingNewline {
		rendered += "\n"
	}
	return rendered
}

// toastDurationForLevel returns the auto-dismiss duration for a toast level.
func toastDurationForLevel(level string) time.Duration {
	switch strings.ToLower(level) {
	case "error":
		return 5 * time.Second
	case "warn":
		return 4 * time.Second
	default:
		return 3 * time.Second
	}
}

// toastLevelPriority returns a numeric priority for comparison (higher = more urgent).
func toastLevelPriority(level string) int {
	switch strings.ToLower(level) {
	case "error":
		return 3
	case "warn":
		return 2
	default:
		return 1
	}
}

func toastTickCmdForLevel(level string, generation uint64) tea.Cmd {
	d := toastDurationForLevel(level)
	return tea.Tick(d, func(time.Time) tea.Msg {
		return toastTickMsg{generation: generation}
	})
}

// enqueueToast enqueues a toast with no category (no merge behavior).
func (m *Model) enqueueToast(msg, level string) tea.Cmd {
	return m.enqueueToastWithCategory(msg, level, "")
}

func (m *Model) shouldPriorityFlushToast(level string) bool {
	if m == nil {
		return false
	}
	if m.displayState != stateForeground {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "warn", "error":
		return true
	default:
		return false
	}
}

func (m *Model) recalcViewportSizeForToastBoundary() tea.Cmd {
	if m == nil || m.viewport == nil {
		return nil
	}
	oldW, oldH := m.viewport.width, m.viewport.height
	m.recalcViewportSize()
	if m.viewport.width == oldW && m.viewport.height == oldH {
		return nil
	}
	m.recordTUIDiagnostic("toast-boundary", "viewport=%dx%d->%dx%d active=%t queue=%d", oldW, oldH, m.viewport.width, m.viewport.height, m.activeToast != nil, len(m.toastQueue))
	return nil
}

// enqueueToastWithCategory enqueues a toast. Same-category toasts in the queue are merged
// (replaced by the newer one). Active toast is not merged — the new toast enters the queue
// unless it can preempt (higher priority).
func (m *Model) enqueueToastWithCategory(msg, level, category string) tea.Cmd {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil
	}
	t := toastItem{Message: msg, Level: strings.ToLower(level), Category: category}

	if m.activeToast == nil {
		m.activeToast = &t
		m.toastGeneration++
		redrawCmd := m.recalcViewportSizeForToastBoundary()
		tickCmd := toastTickCmdForLevel(t.Level, m.toastGeneration)
		if m.shouldPriorityFlushToast(t.Level) {
			return tea.Batch(m.requestStreamBoundaryFlush(), redrawCmd, tickCmd)
		}
		return tea.Batch(redrawCmd, tickCmd)
	}

	// Preempt: new toast has strictly higher priority than the active one.
	if toastLevelPriority(t.Level) > toastLevelPriority(m.activeToast.Level) {
		m.activeToast = &t
		m.toastGeneration++
		if m.shouldPriorityFlushToast(t.Level) {
			return tea.Batch(m.requestStreamBoundaryFlush(), toastTickCmdForLevel(t.Level, m.toastGeneration))
		}
		return toastTickCmdForLevel(t.Level, m.toastGeneration)
	}

	// Merge: if category is set and the queue already has a toast with the same category,
	// replace it with the new one (moved to the tail of the queue).
	if t.Category != "" {
		for i := len(m.toastQueue) - 1; i >= 0; i-- {
			if m.toastQueue[i].Category == t.Category {
				m.toastQueue = append(m.toastQueue[:i], m.toastQueue[i+1:]...)
				break
			}
		}
	}

	m.toastQueue = append(m.toastQueue, t)
	if len(m.toastQueue) > maxQueuedToasts {
		dropIndex := 0
		dropPriority := toastLevelPriority(m.toastQueue[0].Level)
		for i := 1; i < len(m.toastQueue); i++ {
			priority := toastLevelPriority(m.toastQueue[i].Level)
			if priority < dropPriority {
				dropIndex = i
				dropPriority = priority
			}
		}
		m.toastQueue = append(m.toastQueue[:dropIndex], m.toastQueue[dropIndex+1:]...)
	}
	return nil
}

func (m Model) renderQueuedDrafts(width, maxLines int) string {
	drafts := m.visibleQueuedDrafts()
	if len(drafts) == 0 || width <= 0 || maxLines <= 0 {
		return ""
	}
	var lines []string
	visible := min(len(drafts), maxLines)
	for i := range visible {
		d := drafts[i]
		text, imageCount := queuedDraftTextAndImageCount(d)
		if text == "" && imageCount > 0 {
			text = fmt.Sprintf("[%d image(s)]", imageCount)
		}
		prefix := fmt.Sprintf("  [%d] ", i+1)
		deleteWidth := runewidth.StringWidth(queuedDraftDeleteToken)
		maxTextWidth := width - runewidth.StringWidth(prefix) - deleteWidth - queuedDraftDeleteRightMargin - 1
		if maxTextWidth < 8 {
			maxTextWidth = 8
		}
		line := prefix + truncateOneLine(text, maxTextWidth)
		padding := width - runewidth.StringWidth(line) - deleteWidth - queuedDraftDeleteRightMargin
		if padding < 1 {
			padding = 1
		}
		lines = append(lines, DimStyle.Render(line+strings.Repeat(" ", padding)+queuedDraftDeleteToken+strings.Repeat(" ", queuedDraftDeleteRightMargin)))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderAttachments() string {
	mentionAttachments := m.atMentionAttachmentPreviews()
	if len(m.attachments) == 0 && len(mentionAttachments) == 0 {
		return ""
	}
	var lines []string
	for i, a := range m.attachments {
		lines = append(lines, renderAttachmentLine(i+1, a))
	}
	for i, a := range mentionAttachments {
		lines = append(lines, renderAttachmentLine(len(m.attachments)+i+1, a))
	}
	return strings.Join(lines, "\n")
}

func renderAttachmentLine(index int, a Attachment) string {
	sizeBytes := a.SizeBytes
	if sizeBytes == 0 {
		sizeBytes = len(a.Data)
	}
	size := fmt.Sprintf("%.1f KB", float64(sizeBytes)/1024)
	name := a.FileName
	if a.MimeType == "application/pdf" {
		name = "📄 " + name
	}
	var flags []string
	if a.Unsupported {
		flags = append(flags, "unsupported")
	}
	if a.Encrypted {
		flags = append(flags, "encrypted")
	}
	suffix := ""
	if len(flags) > 0 {
		suffix = " ⚠ " + strings.Join(flags, ", ")
	}
	return DimStyle.Render(fmt.Sprintf("  [%d] %s (%s)%s", index, name, size, suffix))
}

func (m Model) renderToast() string {
	if m.activeToast == nil {
		return ""
	}
	var style lipgloss.Style
	switch m.activeToast.Level {
	case "warn":
		style = ToastWarnStyle
	case "error":
		style = ToastErrorStyle
	default:
		style = ToastInfoStyle
	}
	return style.Width(m.width).Render(m.activeToast.Message)
}
