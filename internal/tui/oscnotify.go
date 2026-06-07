package tui

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	tea "github.com/keakon/bubbletea/v2"
)

const maxNotificationRunes = 256

// sanitizeNotificationPayload strips bytes that would break terminal notification OSC
// sequences or annoy the terminal.
func sanitizeNotificationPayload(s string) string {
	s = strings.ReplaceAll(s, "\x07", " ")
	s = strings.ReplaceAll(s, "\x1b", " ")
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		if n >= maxNotificationRunes {
			break
		}
		if r == '\n' || r == '\r' {
			b.WriteByte(' ')
			n++
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "Chord"
	}
	return out
}

func emitTerminalNotificationOSC9(w io.Writer, msg string) {
	if w == nil {
		return
	}
	msg = sanitizeNotificationPayload(msg)
	if msg == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "\x1b]9;%s\x07", msg)
}

func emitOSC777(w io.Writer, title, body string) {
	if w == nil {
		return
	}
	title = sanitizeNotificationPayload(title)
	body = sanitizeNotificationPayload(body)
	if title == "" {
		title = "Chord"
	}
	if body == "" {
		body = "Chord"
	}
	_, _ = fmt.Fprintf(w, "\x1b]777;notify;%s;%s\x07", title, body)
}

func emitTerminalNotification(w io.Writer, protocol terminalNotificationProtocol, msg string) {
	switch protocol {
	case terminalNotificationOSC777:
		emitOSC777(w, "Chord", msg)
	default:
		emitTerminalNotificationOSC9(w, msg)
	}
}

func (m *Model) maybeTerminalNotifyCmd(msg string) tea.Cmd {
	if !m.desktopNotificationsEnabled || m.oscNotifyOut == nil {
		return nil
	}
	if m.terminalAppFocused {
		return nil
	}
	w := m.oscNotifyOut
	protocol := m.terminalNotificationProtocol
	return func() tea.Msg {
		emitTerminalNotification(w, protocol, msg)
		return nil
	}
}

func (m *Model) idleNotificationText() string {
	if msg, ok := m.lastAssistantOrErrorTextForNotification(); ok {
		return msg
	}
	return "Chord: Ready for input"
}

func (m *Model) lastAssistantOrErrorTextForNotification() (string, bool) {
	if m == nil || m.viewport == nil {
		return "", false
	}
	blocks := m.viewport.visibleBlocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block == nil {
			continue
		}
		switch block.Type {
		case BlockAssistant, BlockError:
			content := strings.TrimSpace(block.Content)
			if content == "" {
				continue
			}
			return content, true
		}
	}
	return "", false
}

func isLoopTerminalInfo(msg string) bool {
	trimmed := strings.TrimSpace(msg)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "Loop completed:") ||
		strings.HasPrefix(trimmed, "Loop blocked:") ||
		strings.HasPrefix(trimmed, "Loop blocked (") ||
		strings.HasPrefix(trimmed, "Loop stopped:")
}

func isLoopInfoMessage(msg string) bool {
	trimmed := strings.TrimSpace(msg)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "Loop enabled") ||
		strings.HasPrefix(trimmed, "Loop disabled") ||
		isLoopTerminalInfo(trimmed)
}
