package tui

import (
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// SetIMESwitchTarget sets the im-select target (e.g. com.apple.keylayout.ABC). When set,
// we get current IM before switching and restore it when entering Insert.
func (m *Model) SetIMESwitchTarget(target string) {
	m.imeSwitchTarget = target
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
	m.imeMu.Lock()
	m.imeSeq++
	seq := m.imeSeq
	m.imeMu.Unlock()
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
		m.imePending = false
		m.imePendingTarget = ""
		return
	}
	m.imePending = true
	m.imePendingTarget = target
	if m.imeApplying {
		return
	}
	m.imeApplying = true
	go m.runIMEApplyLoop()
}

func (m *Model) queueIMEApply(target string) {
	m.imeMu.Lock()
	defer m.imeMu.Unlock()
	m.queueIMEApplyLocked(target)
}

func modeNeedsEnglishIME(mode Mode) bool {
	switch mode {
	case ModeNormal, ModeDirectory, ModeSessionSelect, ModeSessionDeleteConfirm, ModeConfirm, ModeQuestion, ModeRules:
		return true
	default:
		return false
	}
}

func (m *Model) reapplyIMEForCurrentModeOnFocus() {
	if !modeNeedsEnglishIME(m.mode) || m.imeSwitchTarget == "" {
		return
	}
	m.queueIMEApply(m.imeSwitchTarget)
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
		m.imeMu.Lock()
		if !m.imePending || strings.TrimSpace(m.imePendingTarget) == "" {
			m.imePending = false
			m.imePendingTarget = ""
			m.imeApplying = false
			m.imeMu.Unlock()
			return
		}
		target := m.imePendingTarget
		m.imePending = false
		m.imePendingTarget = ""
		m.imeMu.Unlock()

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
	if from == to || m.imeSwitchTarget == "" {
		return nil
	}
	if from == ModeInsert {
		return m.getIMECurrentCmd()
	}
	m.queueIMEApply(m.imeSwitchTarget)
	return nil
}

// runIMERestoreIfNeeded restores the saved input method when entering Insert (uses same im-select binary as switch).
func (m *Model) runIMERestoreIfNeeded() {
	if m.imeBeforeNormal == "" {
		return
	}
	target := m.imeBeforeNormal
	m.imeBeforeNormal = "" // clear so rapid re-entry doesn't double-restore or use a stale value
	m.queueIMEApply(target)
}
