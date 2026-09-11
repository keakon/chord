package tui

import (
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

// stubTUITicks makes timer commands deliver their message immediately so tests
// that drive commands synchronously do not wait out real UI delays (toast
// lifetimes, chord timeouts, ticker cadences). Production keeps tea.Tick.
func stubTUITicks(t *testing.T) {
	t.Helper()
	orig := tickCmd
	tickCmd = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(time.Time{}) }
	}
	t.Cleanup(func() { tickCmd = orig })
}
