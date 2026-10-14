package composer

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"
)

func TestSelectionPositionScrollResizeAndAnchor(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(16)
	in.SetValue(strings.Repeat("sample line\n", 14) + "final line")
	in.Update(nil)
	in.SyncHeight()
	offset, ok := in.SelectionPositionAt(PromptWidth+2, 0)
	if !ok || in.ScrollYOffset() == 0 {
		t.Fatal("expected visible scrolled text")
	}
	beforeScroll := in.ScrollYOffset()
	in.StartSelection(offset)
	in.UpdateSelection(offset + 5)
	if in.ScrollYOffset() != beforeScroll {
		t.Fatal("visible click scrolled the input")
	}
	anchor := in.selection.anchor
	next, ok := in.SelectionPositionAt(PromptWidth+3, 1)
	if !ok {
		t.Fatal("missing second visible row")
	}
	in.UpdateSelection(next)
	if in.selection.anchor != anchor || in.ScrollYOffset() != beforeScroll {
		t.Fatal("drag moved anchor or viewport")
	}
	in.UpdateSelection(offset + 5)
	if got := in.SelectionText(); got != "mple " {
		t.Fatalf("scroll mapping = %q, offset %d", got, beforeScroll)
	}
	in.SetWidth(8)
	in.SyncHeight()
	if got := in.SelectionText(); got != "mple " {
		t.Fatalf("resize selection = %q", got)
	}
	if _, ok := in.SelectionPositionAt(PromptWidth, -1); ok {
		t.Fatal("separator is content")
	}
	if _, ok := in.SelectionPositionAt(PromptWidth, in.Height()); ok {
		t.Fatal("bottom padding is content")
	}
}

func TestSelectionCopiesSoftWrapAndTrailingSpaces(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(9)
	in.SetValue("alpha beta gamma  ")
	in.SyncHeight()
	in.SelectRuneRange(0, len([]rune(in.Value())))
	if in.SelectionText() != in.Value() {
		t.Fatalf("soft wrap inserted newlines: %q", in.SelectionText())
	}
	if !strings.Contains(in.ViewWithSelection(), "\x1b[7m") {
		t.Fatal("selection has no highlight")
	}
}

func TestSelectionGraphemeMappingAndRendering(t *testing.T) {
	for _, glyph := range []string{"中", "e\u0301", "👍🏽", "👩‍💻", "🇺🇳", "a\u093e\u093e", "a\u0cf3"} {
		t.Run(glyph, func(t *testing.T) {
			in := New(textarea.DefaultDarkStyles(), nil)
			in.SetWidth(40)
			in.SetValue("a" + glyph + "z")
			in.SyncHeight()
			for x := 1; x < 1+ansi.StringWidth(glyph); x++ {
				offset, ok := in.SelectionPositionAt(PromptWidth+x, 0)
				if !ok || offset != 1 {
					t.Fatalf("glyph hit x=%d = %d, %v", x, offset, ok)
				}
			}
			after, _ := in.SelectionPositionAt(PromptWidth+1+ansi.StringWidth(glyph), 0)
			if after != 1+utf8.RuneCountInString(glyph) {
				t.Fatalf("post-glyph hit = %d", after)
			}
			in.StartSelection(1)
			in.UpdateSelection(2)
			if in.SelectionText() != glyph {
				t.Fatalf("split grapheme: %q", in.SelectionText())
			}
			view := in.ViewWithSelection()
			_, selected, ok := strings.Cut(view, "\x1b[7m")
			selected, _, _ = strings.Cut(selected, "\x1b[27m")
			if !ok || ansi.Strip(selected) != glyph {
				t.Fatalf("highlight split glyph: %q", view)
			}
			in.ReplaceSelection("")
			if in.Value() != "az" {
				t.Fatalf("grapheme deletion = %q", in.Value())
			}
		})
	}
}

func TestInlinePositionDoesNotHitPadding(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(40)
	in.InsertImagePlaceholder(1)
	in.SyncHeight()
	offset, hit := in.RunePositionAt(PromptWidth+2, 0)
	if !hit || offset != 2 {
		t.Fatal("image character not hit")
	}
	if _, hit := in.RunePositionAt(PromptWidth+len(in.Value())+1, 0); hit {
		t.Fatal("padding hit adjacent image")
	}
	if _, hit := in.RunePositionAt(0, 0); hit {
		t.Fatal("prompt hit adjacent image")
	}
}

func TestKeyboardSelectionTreatsObjectsAsOneCharacter(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.InsertImagePlaceholder(1)
	token := in.InlinePastes()[0]
	in.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft, Mod: tea.ModShift}))
	if in.SelectionText() != token.DisplayText {
		t.Fatal("shift left split object")
	}
	in.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModShift}))
	if in.HasSelection() || in.Column() != token.End {
		t.Fatal("reverse extension did not collapse object selection")
	}
}
