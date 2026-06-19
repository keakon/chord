package tui

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"

	tea "github.com/keakon/bubbletea/v2"
)

// imeState groups the input-method-switching subsystem state, previously seven
// loose Model fields. It drives im-select target switching (to an English
// layout for Normal-style modes) and restores the prior IM when re-entering
// Insert. mu is a pointer because the Bubble Tea Model is copied by value during
// Update; the pointer keeps every copy sharing one lock for the apply queue.
type imeState struct {
	switchTarget  string      // im-select target (e.g. com.apple.keylayout.ABC); "" disables switching
	beforeNormal  string      // saved IM before switching to English; restored when entering Insert
	mu            *sync.Mutex // guards the apply queue fields below and seq
	seq           uint64      // generation counter for getIMECurrentCmd staleness checks
	applying      bool        // true while the apply-loop goroutine is running
	pending       bool        // true when an apply is queued
	pendingTarget string      // the queued apply target
}

// SetIMESwitchTarget sets the im-select target (e.g. com.apple.keylayout.ABC). When set,
// we get current IM before switching and restore it when entering Insert.
func (m *Model) SetIMESwitchTarget(target string) {
	m.ime.switchTarget = target
}

// imeCurrentMsg is sent after getting current IM (im-select with no args); we then switch to target and save for restore.
type imeCurrentMsg struct {
	seq     uint64
	current string
}

var imeQueryCurrent = func() (string, error) {
	return queryIMECurrent(exec.Command(imSelectBinary()))
}

var imeApplyTarget = func(target string) error {
	return suppressExternalCommandOutput(exec.Command(imSelectBinary(), target)).Run()
}

func queryIMECurrent(cmd *exec.Cmd) (string, error) {
	out, err := outputWithoutExternalCommandStderr(cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// imSelectBinary returns the im-select executable name for the current platform (im-select.exe on Windows).
func imSelectBinary() string {
	if runtime.GOOS == "windows" {
		return "im-select.exe"
	}
	return "im-select"
}

// getIMECurrentCmd returns a Cmd that runs im-select (no args) and sends the current IM key as imeCurrentMsg.
func (m *Model) getIMECurrentCmd() tea.Cmd {
	m.ime.mu.Lock()
	m.ime.seq++
	seq := m.ime.seq
	m.ime.mu.Unlock()
	return func() tea.Msg {
		current, err := imeQueryCurrent()
		if err != nil {
			current = ""
		}
		return imeCurrentMsg{seq: seq, current: current}
	}
}

func (m *Model) queueIMEApplyLocked(target string) {
	if strings.TrimSpace(target) == "" {
		m.ime.pending = false
		m.ime.pendingTarget = ""
		return
	}
	m.ime.pending = true
	m.ime.pendingTarget = target
	if m.ime.applying {
		return
	}
	m.ime.applying = true
	go m.runIMEApplyLoop()
}

func (m *Model) queueIMEApply(target string) {
	m.ime.mu.Lock()
	defer m.ime.mu.Unlock()
	m.queueIMEApplyLocked(target)
}

func modeNeedsEnglishIME(mode Mode) bool {
	switch mode {
	case ModeNormal, ModeDirectory, ModeMCPSelect, ModeSessionSelect, ModeSessionDeleteConfirm, ModeConfirm, ModeQuestion, ModeRules, ModeContentViewer:
		return true
	default:
		return false
	}
}

func (m *Model) imeSwitchAllowed() bool {
	return m != nil && m.terminalAppFocused
}

func (m *Model) reapplyIMEForCurrentModeOnFocus() {
	if !m.imeSwitchAllowed() || !modeNeedsEnglishIME(m.mode) || m.ime.switchTarget == "" {
		return
	}
	m.queueIMEApply(m.ime.switchTarget)
}

func (m *Model) switchModeWithIME(to Mode) tea.Cmd {
	from := m.mode
	if from == to {
		return nil
	}

	if to != ModeNormal && to != ModeSearch {
		m.clearActiveSearch()
	}
	m.clearChordState()
	m.mode = to
	if from == ModeInsert {
		m.input.Blur()
	}
	if to == ModeInsert {
		m.runIMERestoreIfNeeded()
	}
	return m.runIMESwitchIfTransition(from, to)
}

func (m *Model) restoreModeWithIME(prev Mode) tea.Cmd {
	return m.switchModeWithIME(prev)
}

func (m *Model) runIMEApplyLoop() {
	for {
		m.ime.mu.Lock()
		if !m.ime.pending || strings.TrimSpace(m.ime.pendingTarget) == "" {
			m.ime.pending = false
			m.ime.pendingTarget = ""
			m.ime.applying = false
			m.ime.mu.Unlock()
			return
		}
		target := m.ime.pendingTarget
		m.ime.pending = false
		m.ime.pendingTarget = ""
		m.ime.mu.Unlock()

		_ = imeApplyTarget(target)
	}
}

// runIMESwitchIfTransition runs the IME switch when transitioning to an English-IME mode.
// When entering one of these modes from Insert, it snapshots the current IM first so Insert
// can restore it later. Leaving other modes for these modes switches directly.
func (m *Model) runIMESwitchIfTransition(from, to Mode) tea.Cmd {
	if !modeNeedsEnglishIME(to) {
		return nil
	}
	if from == to || m.ime.switchTarget == "" || !m.imeSwitchAllowed() {
		return nil
	}
	if from == ModeInsert {
		return m.getIMECurrentCmd()
	}
	m.queueIMEApply(m.ime.switchTarget)
	return nil
}

// runIMERestoreIfNeeded restores the saved input method when entering Insert (uses same im-select binary as switch).
func (m *Model) runIMERestoreIfNeeded() {
	if m.ime.beforeNormal == "" {
		return
	}
	target := m.ime.beforeNormal
	m.ime.beforeNormal = "" // clear so rapid re-entry doesn't double-restore or use a stale value
	m.queueIMEApply(target)
}
