package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

type viewportSelection struct {
	active, dragging, inclusive bool
	rangeValue                  SelectionRange
	lastClick                   time.Time
	lastX, lastY, clicks        int
}

func (s *viewportSelection) copyRange() SelectionRange {
	r := s.rangeValue
	if s.inclusive {
		if posLess(r.StartBlockID, r.StartLine, r.StartCol, r.EndBlockID, r.EndLine, r.EndCol) {
			r.EndCol++
		} else if posLess(r.EndBlockID, r.EndLine, r.EndCol, r.StartBlockID, r.StartLine, r.StartCol) {
			r.StartCol++
		}
	}
	return r
}

func (s *viewportSelection) rangePtr() *SelectionRange {
	if !s.active {
		return nil
	}
	r := s.copyRange()
	if r.StartBlockID == r.EndBlockID && r.StartLine == r.EndLine && r.StartCol == r.EndCol {
		return nil
	}
	return &r
}

func (s *viewportSelection) clear() {
	// Keep the click sequence when clearing a drag, as single-click actions
	// may clear the selection before the next click selects a word.
	s.active, s.dragging, s.inclusive = false, false, false
	s.rangeValue = SelectionRange{}
}

func (s *viewportSelection) start(blockID, line, col int) {
	s.active, s.dragging, s.inclusive = true, true, true
	s.rangeValue = SelectionRange{StartBlockID: blockID, StartLine: line, StartCol: col, EndBlockID: blockID, EndLine: line, EndCol: col}
}

// begin returns whether a multi-click consumed the click, so the caller can
// keep card actions restricted to single clicks.
func (s *viewportSelection) begin(v *Viewport, block *Block, line, col int, mouse tea.Mouse) bool {
	now := time.Now()
	if now.Sub(s.lastClick) <= doubleClickThreshold && abs(mouse.X-s.lastX) <= mouseClickTolerance && abs(mouse.Y-s.lastY) <= mouseClickTolerance {
		s.clicks++
	} else {
		s.clicks = 1
	}
	s.lastClick, s.lastX, s.lastY = now, mouse.X, mouse.Y
	s.start(block.ID, line, col)
	if s.clicks < 2 {
		return false
	}
	plain, width := v.GetLinePlain(block.ID, line)
	start, end := 0, width
	if s.clicks == 2 {
		start, end = WordBoundsAtCol(plain, col)
	} else {
		s.clicks = 0
	}
	if start < end {
		s.rangeValue.StartCol, s.rangeValue.EndCol = start, end
		s.dragging, s.inclusive = false, false
	}
	return true
}

func (s *viewportSelection) extend(blockID, line, col int) {
	if s.dragging {
		s.rangeValue.EndBlockID, s.rangeValue.EndLine, s.rangeValue.EndCol = blockID, line, col
		s.inclusive = true
	}
}
