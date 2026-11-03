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

// testCmdMsgs runs cmd and flattens any tea.BatchMsg produced by tea.Batch so
// tests can assert on individual follow-up messages regardless of batch
// composition or ordering.
func testCmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var msgs []tea.Msg
	for _, sub := range batch {
		msgs = append(msgs, testCmdMsgs(sub)...)
	}
	return msgs
}

func testFindMsg[T any](msgs []tea.Msg) (T, bool) {
	for _, msg := range msgs {
		if typed, ok := msg.(T); ok {
			return typed, true
		}
	}
	var zero T
	return zero, false
}
