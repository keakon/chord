package tui

import (
	"slices"
	"strings"
	"unicode"

	tea "github.com/keakon/bubbletea/v2"
)

const maxNotificationRunes = 256

// The standalone bell follows OSC's own BEL terminator. Terminals consume the
// first byte as part of OSC and interpret this one as an attention signal.
const desktopNotificationBell = "\a"

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

func terminalNotificationOSC9Sequence(msg string) string {
	return "\x1b]9;" + sanitizeNotificationPayload(msg) + "\x07" + desktopNotificationBell
}

func terminalNotificationOSC777Sequence(title, body string) string {
	return "\x1b]777;notify;" + sanitizeNotificationPayload(title) + ";" + sanitizeNotificationPayload(body) + "\x07" + desktopNotificationBell
}

func terminalNotificationSequence(protocol terminalNotificationProtocol, msg string) string {
	switch protocol {
	case terminalNotificationOSC777:
		return terminalNotificationOSC777Sequence("Chord", msg)
	default:
		return terminalNotificationOSC9Sequence(msg)
	}
}

func (m *Model) maybeTerminalNotifyCmd(msg string) tea.Cmd {
	if !m.desktopNotificationsEnabled {
		return nil
	}
	if !m.desktopNotificationsForeground && m.terminalAppFocused {
		return nil
	}
	return tea.Raw(terminalNotificationSequence(m.terminalNotificationProtocol, msg))
}

func (m *Model) idleNotificationText() string {
	if msg, ok := m.lastAssistantOrErrorTextForNotification(); ok {
		return msg
	}
	return "Chord: Ready for input"
}

// lastAssistantOrErrorTextForNotification returns the text that describes how
// the transcript currently ends. The scan stops at the first tool or user block
// so it cannot reach back past them: assistant text followed by tool activity is
// mid-turn narration, and a turn whose reply never materialized (for example an
// output-limit truncation that produced no assistant block) must not be
// announced with a line the user already read.
func (m *Model) lastAssistantOrErrorTextForNotification() (string, bool) {
	if m == nil || m.viewport == nil {
		return "", false
	}
	blocks := m.viewport.visibleBlocks()
	for _, block := range slices.Backward(blocks) {

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
		case BlockUser, BlockToolCall, BlockToolResult:
			return "", false
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
