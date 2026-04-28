package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/keakon/chord/internal/terminaltitle"
)

const (
	terminalTitleRequestIcon   = "❓"
	terminalTitleRequestSpacer = " "
)

type terminalTitleMode int

const (
	terminalTitleModeStatic terminalTitleMode = iota
	terminalTitleModeSpinner
	// terminalTitleModeRequest shows a question-mark icon (?" in the title bar.
	// The icon blinks (toggles between ? and a space) only when the terminal is
	// in the background state. This is by design: the blink acts as an attention
	// signal to draw the user back when they have switched away. When the terminal
	// is in the foreground the icon stays static to avoid unnecessary distraction.
	terminalTitleModeRequest
)

// terminalTitleTickMsg is sent when the terminal title ticker fires.
type terminalTitleTickMsg struct{ generation uint64 }

// deriveTerminalTitle extracts a short title from the first user message.
// It collapses whitespace, strips control characters, and truncates to a
// sensible length for tab bars.
func deriveTerminalTitle(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	// Remove control characters (ESC, BEL, etc.)
	var b strings.Builder
	for _, ch := range s {
		if ch < 0x20 || ch == 0x7F {
			continue
		}
		b.WriteRune(ch)
	}
	s = b.String()
	s = strings.Join(strings.Fields(s), " ") // collapse + trim
	if s == "" {
		return terminaltitle.DefaultTitle
	}
	// Truncate to MaxTitleRunes, appending ellipsis if needed.
	if len([]rune(s)) > terminaltitle.MaxTitleRunes {
		s = string([]rune(s)[:terminaltitle.MaxTitleRunes]) + "…"
	}
	return s
}

// setTerminalTitle writes the current title to stdout via OSC.
func (m *Model) setTerminalTitle(mode terminalTitleMode) {
	if !terminaltitle.IsTerminal() {
		return
	}

	title := m.terminalTitleBase
	if title == "" {
		title = terminaltitle.DefaultTitle
	}

	switch mode {
	case terminalTitleModeSpinner:
		frame := terminaltitle.NextSpinnerFrame()
		_ = terminaltitle.SetWindowTitleWithSpinner(os.Stdout, title, frame)
	case terminalTitleModeRequest:
		prefix := terminalTitleRequestIcon
		if m.displayState == stateBackground && m.terminalTitleRequestBlinkOff {
			prefix = terminalTitleRequestSpacer
		}
		_ = terminaltitle.SetWindowTitleWithPrefix(os.Stdout, title, prefix)
	default:
		_ = terminaltitle.SetWindowTitle(os.Stdout, title)
	}
}

// terminalTitleTickCmd returns a tea.Cmd that sends terminalTitleTickMsg.
func terminalTitleTickCmd(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		return nil
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return terminalTitleTickMsg{generation: generation}
	})
}

func (m *Model) terminalTitleNeedsUserResponse() bool {
	if m == nil {
		return false
	}
	return m.confirm.request != nil || m.question.request != nil
}

func (m *Model) currentTitleMode() terminalTitleMode {
	if m == nil {
		return terminalTitleModeStatic
	}
	if m.terminalTitleNeedsUserResponse() {
		return terminalTitleModeRequest
	}
	if m.hasActiveAnimation() {
		return terminalTitleModeSpinner
	}
	return terminalTitleModeStatic
}

func (m *Model) syncTerminalTitleState() tea.Cmd {
	if m == nil {
		return nil
	}
	switch m.currentTitleMode() {
	case terminalTitleModeSpinner:
		return m.syncTerminalTitleTickerWithCadence()
	case terminalTitleModeRequest:
		if m.currentTitleTickerDelay() <= 0 {
			m.stopTerminalTitleTicker()
			m.setTerminalTitle(terminalTitleModeRequest)
			return nil
		}
		if !m.terminalTitleTickRunning {
			return m.startTerminalTitleTicker()
		}
		m.setTerminalTitle(terminalTitleModeRequest)
		return nil
	default:
		m.stopTerminalTitleTicker()
		m.setTerminalTitle(terminalTitleModeStatic)
		return nil
	}
}

func (m *Model) currentTitleTickerDelay() time.Duration {
	if m == nil {
		return 0
	}
	if m.terminalTitleNeedsUserResponse() {
		if m.displayState == stateForeground {
			return 0
		}
		return backgroundActiveCadence.titleTickerDelay
	}
	return m.currentCadence().titleTickerDelay
}

func (m *Model) syncTerminalTitleTickerWithCadence() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.terminalTitleNeedsUserResponse() {
		return nil
	}
	delay := m.currentTitleTickerDelay()
	if delay <= 0 || !m.hasActiveAnimation() {
		m.stopTerminalTitleTicker()
		return nil
	}
	if m.terminalTitleTickRunning {
		return nil
	}
	return m.startTerminalTitleTicker()
}

// startTerminalTitleTicker begins the independent title spinner ticker.
// Returns a tea.Cmd that should be batched into the Update return.
func (m *Model) startTerminalTitleTicker() tea.Cmd {
	delay := m.currentTitleTickerDelay()
	if delay <= 0 {
		m.stopTerminalTitleTicker()
		return nil
	}
	if m.terminalTitleTickRunning {
		return nil
	}
	m.terminalTitleTickRunning = true
	m.terminalTitleTickGeneration++
	gen := m.terminalTitleTickGeneration
	m.terminalTitleRequestBlinkOff = false
	// Write initial title frame immediately.
	m.setTerminalTitle(m.currentTitleMode())
	// Return the first tick command.
	return terminalTitleTickCmd(gen, delay)
}

// stopTerminalTitleTicker stops the title spinner and writes a final static title.
func (m *Model) stopTerminalTitleTicker() {
	if !m.terminalTitleTickRunning {
		return
	}
	m.terminalTitleTickRunning = false
	m.terminalTitleTickGeneration++
	m.terminalTitleRequestBlinkOff = false
	m.setTerminalTitle(m.currentTitleMode())
}

// setTerminalTitleFromMessage updates the base title (without spinner) from
// a user message and writes it to the terminal.
func (m *Model) setTerminalTitleFromMessage(raw string) {
	m.terminalTitleBase = deriveTerminalTitle(raw)
	m.setTerminalTitle(m.currentTitleMode())
}

// resetTerminalTitle resets the title to the default and writes it.
func (m *Model) resetTerminalTitle() {
	m.terminalTitleBase = ""
	m.setTerminalTitle(m.currentTitleMode())
}
