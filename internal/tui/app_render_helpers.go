package tui

import (
	"fmt"
	"github.com/keakon/golog/log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

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
		return 7 * time.Second
	case "warn":
		return 5 * time.Second
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

// logToastExpired logs a toast that expired naturally.
func logToastExpired(t *toastItem) {
	log.Infof("toast expired level=%v message=%v category=%v", t.Level, t.Message, t.Category)
}

// logToastPreempted logs a toast that was replaced by a higher-priority one.
func logToastPreempted(t *toastItem, byLevel string) {
	log.Infof("toast preempted level=%v message=%v category=%v by=%v", t.Level, t.Message, t.Category, byLevel)
}

// logToastMerged logs a toast that was replaced in queue by a newer one of the same category.
func logToastMerged(oldMsg, newMsg string, level string) {
	log.Infof("toast merged level=%v message=%v replaced=%v", level, newMsg, oldMsg)
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
		m.recalcViewportSize()
		if m.shouldPriorityFlushToast(t.Level) {
			return tea.Batch(m.requestStreamBoundaryFlush(), toastTickCmdForLevel(t.Level, m.toastGeneration))
		}
		return toastTickCmdForLevel(t.Level, m.toastGeneration)
	}

	// Preempt: new toast has strictly higher priority than the active one.
	if toastLevelPriority(t.Level) > toastLevelPriority(m.activeToast.Level) {
		logToastPreempted(m.activeToast, t.Level)
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
				logToastMerged(m.toastQueue[i].Message, t.Message, t.Level)
				m.toastQueue = append(m.toastQueue[:i], m.toastQueue[i+1:]...)
				break
			}
		}
	}

	m.toastQueue = append(m.toastQueue, t)
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
	if len(m.attachments) == 0 {
		return ""
	}
	var lines []string
	for i, a := range m.attachments {
		size := fmt.Sprintf("%.1f KB", float64(len(a.Data))/1024)
		lines = append(lines, DimStyle.Render(fmt.Sprintf("  [%d] %s (%s)", i+1, a.FileName, size)))
	}
	return strings.Join(lines, "\n")
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
