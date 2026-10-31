package composer

import (
	"strings"
	"testing"

	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
)

func newTestInput() Input {
	return New(textarea.DefaultDarkStyles(), nil)
}

func typeKey(i *Input, r rune) {
	i.Update(tea.KeyPressMsg(tea.Key{Text: string(r), Code: r}))
}

// The memoized Value must observe edits from every mutation path: key input,
// direct setters, history restore, draft reset, and paste replacement.
func TestInputValueCacheObservesAllMutations(t *testing.T) {
	i := newTestInput()
	i.SetWidth(80)
	i.SetHeight(4)

	settle := func() {
		// Warm the cache, then verify a repeated read is stable.
		if got := i.Value(); got != i.Value() {
			t.Fatalf("Value is unstable across reads: %q vs %q", i.Value(), got)
		}
	}

	i.SetValue("first draft")
	if got := i.Value(); got != "first draft" {
		t.Fatalf("after SetValue = %q, want %q", got, "first draft")
	}
	settle()

	i.InsertString(" plus")
	if got := i.Value(); got != "first draft plus" {
		t.Fatalf("after InsertString = %q, want %q", got, "first draft plus")
	}
	settle()

	// A keystroke through Update must show up: replace the content and type.
	i.SetValue("")
	typeKey(&i, 'x')
	if got := i.Value(); got != "x" {
		t.Fatalf("after key press = %q, want %q", got, "x")
	}
	settle()

	i.Reset()
	if got := i.Value(); got != "" {
		t.Fatalf("after Reset = %q, want empty", got)
	}
	settle()

	i.Restore(Draft{Entry: HistoryEntry{Display: "restored entry"}})
	if got := i.Value(); got != "restored entry" {
		t.Fatalf("after Restore = %q, want %q", got, "restored entry")
	}
	settle()
}

// Typing around an inline large-paste placeholder must keep the placeholder
// token ranges in sync with the display text: the submitted parts carry the
// typed characters where they were typed and the raw paste where the
// placeholder sits.
func TestInputValueCacheKeepsInlinePastesAlignedAcrossEdits(t *testing.T) {
	i := newTestInput()
	i.SetWidth(80)
	i.SetHeight(4)
	raw := strings.Repeat("line\n", 40)

	// An edit before the placeholder must shift the token range so the
	// submitted parts keep every typed character.
	i.SetValue("hello")
	if !i.InsertLargePaste(raw) {
		t.Fatal("precondition: paste content is not large enough for an inline placeholder")
	}
	i.SetCursorOffset(2)
	_ = i.Value() // a render reads the memo before the keystroke lands
	typeKey(&i, 'x')
	if got := i.Value(); !strings.HasPrefix(got, "hexllo") {
		t.Fatalf("value after typing before the placeholder = %q, want prefix %q", got, "hexllo")
	}
	if text := joinedPlainTextParts(i.ContentParts()); text != "hexllo" {
		t.Fatalf("text parts after typing before the placeholder = %q, want %q", text, "hexllo")
	}

	// A keystroke with the cursor on the placeholder boundary must not be
	// swallowed by the stale-value round-trip through
	// ensureCursorOutsideInlinePastes.
	i.SetValue("")
	if !i.InsertLargePaste(raw) {
		t.Fatal("precondition: paste content is not large enough for an inline placeholder")
	}
	i.SetCursorOffset(0)
	_ = i.Value()
	typeKey(&i, 'x')
	if got := i.Value(); !strings.HasPrefix(got, "x") {
		t.Fatalf("value after typing at the placeholder boundary = %q, want the typed key preserved", got)
	}
	if text := joinedPlainTextParts(i.ContentParts()); text != "x" {
		t.Fatalf("text parts after typing at the placeholder boundary = %q, want %q", text, "x")
	}
}

// joinedPlainTextParts concatenates the plain text segments of submitted parts,
// skipping inline paste substitutions (which carry a display text).
func joinedPlainTextParts(parts []message.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == message.ContentPartText && p.DisplayText == "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
