package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/buildinfo"
)

func TestViewShowsWelcomeVersionOnEmptySession(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.layout = m.generateLayout(m.width, m.height)

	got := stripANSI(m.View().Content)
	wantVersion := buildinfo.Current().Short()
	if !strings.Contains(got, wantVersion) {
		t.Fatalf("View() should show welcome version %q, got %q", wantVersion, got)
	}
	if !strings.Contains(got, "ctrl+p: model pool") {
		t.Fatalf("View() should describe Ctrl+P as model pool selection, got %q", got)
	}
	if !strings.Contains(got, "terminal paste: text") {
		t.Fatalf("View() should describe text paste without guessing the client OS, got %q", got)
	}
	if strings.Contains(got, "cmd+v: paste text") || strings.Contains(got, "ctrl+shift+v: paste text") {
		t.Fatalf("View() should not infer the terminal paste shortcut from the host OS, got %q", got)
	}
	// The wordmark (block art, or "chor♩" on small viewports) is closed by its
	// swash, then one blank line, then the version.
	lines := strings.Split(got, "\n")
	for i, line := range lines {
		if !strings.Contains(line, wantVersion) {
			continue
		}
		if i < 3 {
			t.Fatalf("View() should render the wordmark above the version, got:\n%s", got)
		}
		if strings.TrimSpace(lines[i-1]) != "" {
			t.Fatalf("View() should leave one blank line between the wordmark and version, got:\n%s", got)
		}
		if strings.TrimSpace(lines[i-2]) == "" {
			t.Fatalf("View() should render the swash above that blank line, got:\n%s", got)
		}
		// The large wordmark keeps a baseline gap between its letters and the
		// swash, so the art is a row or two further up.
		if !slices.ContainsFunc(lines[:i-2], func(l string) bool { return strings.TrimSpace(l) != "" }) {
			t.Fatalf("View() should render the wordmark above its swash, got:\n%s", got)
		}
		return
	}
	t.Fatalf("View() should show the welcome version %q, got %q", wantVersion, got)
}

func TestViewShowsRestoringSessionPlaceholderDuringStartupRestore(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.startupRestorePending = true
	m.beginSessionSwitch("resume", "123")
	m.layout = m.generateLayout(m.width, m.height)

	got := stripANSI(m.View().Content)
	if !strings.Contains(got, "Restoring session...") {
		t.Fatalf("View() should show restoring placeholder, got %q", got)
	}
	if strings.Contains(got, "No messages yet. Start a conversation!") {
		t.Fatalf("View() should suppress empty welcome text during startup restore, got %q", got)
	}
	if !strings.Contains(got, "Resuming 123...") {
		t.Fatalf("View() should show status-bar resume progress during startup restore, got %q", got)
	}
}

func TestViewRefreshesComposerAfterInputChanges(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeInsert

	initial := stripANSI(m.View().Content)
	if strings.Contains(initial, "hello cache") {
		t.Fatalf("initial View() unexpectedly contains future composer text: %q", initial)
	}

	m.input.SetValue("hello cache")
	m.input.syncHeight()

	updated := stripANSI(m.View().Content)
	if !strings.Contains(updated, "hello cache") {
		t.Fatalf("View() should refresh composer text after input changes, got:\n%s", updated)
	}
}

func TestViewReadCardWithCRLFResultDoesNotLeakCarriageReturnIntoCanvas(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.csv","limit":20}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"     1\tissue,label\r",
			"     2\t\"a\",\"b\"\r",
		}, "\n"),
	})
	m.recalcViewportSize()

	view := m.View()
	if containsRawCarriageReturnForTest(view.Content) {
		t.Fatalf("View().Content should not contain raw carriage returns: %q", view.Content)
	}
	plain := stripANSI(view.Content)
	if !strings.Contains(plain, "issue,label") {
		t.Fatalf("expected View() content to contain first CSV row, got:\n%s", plain)
	}
	if !strings.Contains(plain, `"a","b"`) {
		t.Fatalf("expected View() content to contain second CSV row, got:\n%s", plain)
	}
}
