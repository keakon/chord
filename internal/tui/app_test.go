package tui

import (
	"fmt"
	"os"
	"testing"

	uv "github.com/keakon/ultraviolet"
	"github.com/keakon/x/ansi"
)

func TestMain(m *testing.M) {
	os.Exit(runTestsWithTemporaryRoot(m))
}

// Keep fallback spill files and subprocess scratch inside this test process's
// own directory. Never sweep the shared system temporary directory.
func runTestsWithTemporaryRoot(m *testing.M) (code int) {
	root, err := os.MkdirTemp("", "ch-tui-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test temporary root:", err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintln(os.Stderr, "remove test temporary root:", err)
			code = 1
		}
	}()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(key, root); err != nil {
			fmt.Fprintln(os.Stderr, "set test temporary root:", err)
			return 1
		}
	}
	origClipboardWriteAll := clipboardWriteAll
	clipboardWriteAll = func(string) error { return nil }
	code = m.Run()
	clipboardWriteAll = origClipboardWriteAll
	return code
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
