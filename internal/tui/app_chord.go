package tui

import (
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
)

const normalChordTimeout = 5 * time.Second
const normalChordMaxCount = 9999

type chordOp int

const (
	chordNone chordOp = iota
	chordG
	chordY
	chordD
	chordE
)

type chordState struct {
	count   int
	op      chordOp
	startAt time.Time
}

type chordTimeoutMsg struct {
	generation uint64
}

func chordTimeoutTick(generation uint64) tea.Cmd {
	return tea.Tick(normalChordTimeout, func(time.Time) tea.Msg {
		return chordTimeoutMsg{generation: generation}
	})
}

func (c chordState) active() bool {
	return c.count > 0 || c.op != chordNone
}

func (c chordState) display() string {
	if !c.active() {
		return ""
	}
	text := ""
	if c.count > 0 {
		text = strconv.Itoa(c.count)
	}
	switch c.op {
	case chordG:
		text += "g"
	case chordY:
		text += "y"
	case chordD:
		text += "d"
	case chordE:
		text += "e"
	}
	return text
}

func normalCountDigit(key string) (digit int, ok bool) {
	if len(key) != 1 || key[0] < '0' || key[0] > '9' {
		return 0, false
	}
	return int(key[0] - '0'), true
}

func (m *Model) clearChordState() {
	if !m.chord.active() {
		return
	}
	m.chord = chordState{}
	m.chordTickGeneration++
}

func (m *Model) updateChordState(count int, op chordOp) tea.Cmd {
	m.chord = chordState{
		count:   count,
		op:      op,
		startAt: time.Now(),
	}
	m.chordTickGeneration++
	return chordTimeoutTick(m.chordTickGeneration)
}

func (m *Model) startChordCount(digit int) tea.Cmd {
	if digit < 1 || digit > 9 {
		return nil
	}
	return m.updateChordState(digit, chordNone)
}

func (m *Model) appendChordCount(digit int) tea.Cmd {
	count := m.chord.count
	if count <= 0 {
		if digit == 0 {
			return nil
		}
		return m.startChordCount(digit)
	}
	if count >= normalChordMaxCount {
		return nil
	}
	count = min(normalChordMaxCount, count*10+digit)
	return m.updateChordState(count, m.chord.op)
}

func (m *Model) startChordOp(op chordOp) tea.Cmd {
	return m.updateChordState(m.chord.count, op)
}

func (m *Model) chordCountOr(defaultCount int) int {
	if m.chord.count > 0 {
		return m.chord.count
	}
	return defaultCount
}
