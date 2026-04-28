package tui

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

const osc9MaxRunes = 256

// sanitizeOSC9Payload strips bytes that would break OSC 9 or annoy the terminal.
func sanitizeOSC9Payload(s string) string {
	s = strings.ReplaceAll(s, "\x07", " ")
	s = strings.ReplaceAll(s, "\x1b", " ")
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		if n >= osc9MaxRunes {
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

func emitOSC9(w io.Writer, msg string) {
	if w == nil {
		return
	}
	msg = sanitizeOSC9Payload(msg)
	if msg == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "\x1b]9;%s\x07", msg)
}

func (m *Model) maybeOSC9NotifyCmd(msg string) tea.Cmd {
	if !m.desktopOSC9Enabled || m.oscNotifyOut == nil {
		return nil
	}
	if m.terminalAppFocused {
		return nil
	}
	w := m.oscNotifyOut
	return func() tea.Msg {
		emitOSC9(w, msg)
		return nil
	}
}

func (m *Model) osc9IdleNotificationText() string {
	if msg, ok := m.lastOSC9AssistantOrErrorText(); ok {
		return msg
	}
	return "Chord: Ready for input"
}

func (m *Model) lastOSC9AssistantOrErrorText() (string, bool) {
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
