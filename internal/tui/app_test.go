package tui

import (
	"os"
	"testing"

	"github.com/charmbracelet/x/ansi"
	uv "github.com/keakon/ultraviolet"
)

func TestMain(m *testing.M) {
	origClipboardWriteAll := clipboardWriteAll
	clipboardWriteAll = func(string) error { return nil }
	code := m.Run()
	clipboardWriteAll = origClipboardWriteAll
	os.Exit(code)
}

type countingScreen struct {
	*uv.Buffer
	method   ansi.Method
	setCalls int
}

func newCountingScreen(width, height int) *countingScreen {
	return &countingScreen{
		Buffer: uv.NewBuffer(width, height),
		method: ansi.WcWidth,
	}
}

func (s *countingScreen) Bounds() uv.Rectangle {
	return s.Buffer.Bounds()
}

func (s *countingScreen) CellAt(x, y int) *uv.Cell {
	return s.Buffer.CellAt(x, y)
}

func (s *countingScreen) SetCell(x, y int, c *uv.Cell) {
	s.setCalls++
	s.Buffer.SetCell(x, y, c)
}

func (s *countingScreen) WidthMethod() uv.WidthMethod {
	return s.method
}

// ---------------------------------------------------------------------------
// IME switch / restore tests
// ---------------------------------------------------------------------------

func preventIMEApplyInTests(m *Model) {
	m.ime.mu.Lock()
	m.ime.applying = true
	m.ime.mu.Unlock()
}

func findThinkingBlockInViewport(v *Viewport) *Block {
	for _, b := range v.visibleBlocks() {
		if b.Type == BlockThinking {
			return b
		}
	}
	return nil
}
