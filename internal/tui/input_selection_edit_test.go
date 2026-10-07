package tui

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
)

func TestSelectionDeletesSourceRangeInBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, code := range []rune{tea.KeyBackspace, tea.KeyDelete} {
			m := NewModel(nil)
			m.mode = ModeInsert
			m.input.SetWidth(10)
			m.input.SetValue("alpha beta  \n\ngamma delta")
			start, end := 3, 19
			if reverse {
				start, end = end, start
			}
			m.input.SelectRuneRange(start, end)
			if got := m.input.SelectionText(); got != "ha beta  \n\ngamma" {
				t.Fatalf("copied source = %q", got)
			}
			m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: code}))
			if m.input.Value() != "alp delta" || m.input.Line() != 0 || m.input.Column() != 3 {
				t.Fatalf("delete = %q at %d:%d", m.input.Value(), m.input.Line(), m.input.Column())
			}
			m.undoComposerEdit()
			if m.input.Value() != "alpha beta  \n\ngamma delta" {
				t.Fatal("undo lost source whitespace")
			}
		}
	}
}

func TestSelectionPositionScrollResizeAndAnchor(t *testing.T) {
	in := NewInput()
	in.SetWidth(16)
	in.SetValue(strings.Repeat("sample line\n", 14) + "final line")
	in.Update(nil)
	in.syncHeight()
	offset, ok := in.SelectionPositionAt(inputPromptWidth+2, 0)
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
	next, ok := in.SelectionPositionAt(inputPromptWidth+3, 1)
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
	in.syncHeight()
	if got := in.SelectionText(); got != "mple " {
		t.Fatalf("resize selection = %q", got)
	}
	if _, ok := in.SelectionPositionAt(inputPromptWidth, -1); ok {
		t.Fatal("separator is content")
	}
	if _, ok := in.SelectionPositionAt(inputPromptWidth, in.Height()); ok {
		t.Fatal("bottom padding is content")
	}
}

func TestSelectionCopiesSoftWrapAndTrailingSpaces(t *testing.T) {
	in := NewInput()
	in.SetWidth(9)
	in.SetValue("alpha beta gamma  ")
	in.syncHeight()
	in.SelectRuneRange(0, len([]rune(in.Value())))
	if in.SelectionText() != in.Value() {
		t.Fatalf("soft wrap inserted newlines: %q", in.SelectionText())
	}
	if !strings.Contains(in.ViewWithSelection(), "\x1b[7m") {
		t.Fatal("selection has no highlight")
	}
}

func TestSelectionGraphemeMappingAndRendering(t *testing.T) {
	for _, glyph := range []string{"中", "e\u0301", "👍🏽", "👩‍💻", "🇺🇳"} {
		t.Run(glyph, func(t *testing.T) {
			in := NewInput()
			in.SetWidth(40)
			in.SetValue("a" + glyph + "z")
			in.syncHeight()
			for x := 1; x < 1+ansi.StringWidth(glyph); x++ {
				offset, ok := in.SelectionPositionAt(inputPromptWidth+x, 0)
				if !ok || offset != 1 {
					t.Fatalf("glyph hit x=%d = %d, %v", x, offset, ok)
				}
			}
			after, _ := in.SelectionPositionAt(inputPromptWidth+1+ansi.StringWidth(glyph), 0)
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

func TestSelectionKeyboardSharesMouseRange(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetValue("alpha beta")
	m.input.SelectRuneRange(2, 5)
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModShift}))
	if m.input.SelectionText() != "pha " || m.input.textarea.HasSelection() {
		t.Fatal("keyboard did not extend sole selection")
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	if m.input.Column() != 2 || m.input.HasSelection() {
		t.Fatal("left did not collapse to start")
	}
	m.input.SelectRuneRange(6, 2)
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'f', Mod: tea.ModCtrl}))
	if m.input.Column() != 6 {
		t.Fatal("right advanced past end")
	}
	m.input.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	if m.input.SelectionText() != m.input.Value() || m.input.textarea.HasSelection() {
		t.Fatal("select-all is not unified")
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if m.input.HasSelection() || m.input.Column() != 0 {
		t.Fatal("home did not clear selection")
	}
}

func TestSelectionReplaceStartsSeparateUndoTransaction(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	for _, r := range "alpha" {
		m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	m.input.SelectRuneRange(1, 4)
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: '!', Text: "!"}))
	if m.input.Value() != "a!a" || m.input.BangMode() {
		t.Fatal("selection replacement entered shell mode")
	}
	m.undoComposerEdit()
	if m.input.Value() != "alpha" {
		t.Fatal("replacement merged with prior typing")
	}
	m.undoComposerEdit()
	if m.input.Value() != "" {
		t.Fatal("prior typing undo was lost")
	}
}

func TestSelectionDeletesMultipleImagesAndPreservesOtherObjects(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetWidth(60)
	m.input.InsertString("prefix ")
	for idx := 1; idx <= 3; idx++ {
		m.input.InsertImagePlaceholder(idx)
		m.attachments = append(m.attachments, Attachment{FileName: imagePlaceholder(idx) + ".png", MimeType: "image/png", Data: []byte{byte(idx)}, InlineImagePlaceholder: true})
	}
	m.attachments = append(m.attachments, Attachment{FileName: "sample.pdf", MimeType: "application/pdf", Data: []byte("pdf")})
	m.input.InsertLargePaste(strings.Repeat("sample\n", 12))
	before := m.composerEditSnapshot()
	pastes := m.input.InlinePastes()
	m.input.SelectRuneRange(pastes[0].Start+1, pastes[1].End-1)
	start, end, _ := m.input.SelectionRange()
	if start != pastes[0].Start || end != pastes[1].End {
		t.Fatal("partial objects not expanded")
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete}))
	kept := m.input.InlinePastes()
	if len(m.attachments) != 2 || m.attachments[0].Data[0] != 3 || len(kept) != 2 || kept[0].RawContent != "[image1]" || kept[1].RawContent != pastes[3].RawContent || !m.input.inlinePastesValid() {
		t.Fatalf("remaining bindings/objects = %+v / %+v", m.attachments, kept)
	}
	m.undoComposerEdit()
	if !sameComposerContent(before, m.composerEditSnapshot()) {
		t.Fatal("undo lost attachment/object identity")
	}
	m.input.StartSelection(pastes[0].End)
	m.input.UpdateSelection(pastes[0].End)
	if m.input.ReplaceSelection("") {
		t.Fatal("empty range deleted adjacent object")
	}
}

func TestSelectionPasteReplacementAndUndo(t *testing.T) {
	for _, text := range []string{"replacement", "  ", "\n", strings.Repeat("sample\n", 12), "a\tb"} {
		m := NewModel(nil)
		m.mode = ModeInsert
		m.input.SetValue("alpha beta gamma")
		m.input.SelectRuneRange(6, 10)
		m.handleNonKeyInputMsg(tea.PasteMsg{Content: text})
		parts := m.input.ContentParts()
		var content strings.Builder
		for _, part := range parts {
			content.WriteString(part.Text)
		}
		wantText := text
		if text == "a\tb" {
			wantText = "a    b"
		}
		if content.String() != "alpha "+wantText+" gamma" {
			t.Fatalf("paste expanded content = %q", content.String())
		}
		m.undoComposerEdit()
		if m.input.Value() != "alpha beta gamma" || m.input.HasInlinePastes() {
			t.Fatal("paste undo incomplete")
		}
	}
}

func TestComposerClipboardRejectsStaleResults(t *testing.T) {
	for _, scenario := range []string{"text", "cursor", "selection", "agent", "session", "boundary", "resize", "error", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			m := NewModel(nil)
			m.mode = ModeInsert
			m.input.SetValue("alpha beta")
			m.input.SelectRuneRange(0, 5)
			msg := composerClipboardTextMsg{target: m.composerClipboardTarget(), text: "new"}
			switch scenario {
			case "text":
				m.input.SetValue("current")
			case "cursor":
				m.input.setCursorRuneOffset(0)
				m.input.setCursorRuneOffset(5)
			case "selection":
				m.input.ClearSelection()
				m.input.SelectRuneRange(0, 5)
			case "agent":
				m.focusedAgentID = "worker"
			case "session":
				m.sessionTranscriptEpoch++
			case "boundary":
				m.input.editBoundary++
			case "resize":
				m.input.SetWidth(10)
				m.input.syncHeight()
			case "error":
				msg.err = errors.New("sample read error")
			case "empty":
				msg.text = ""
			}
			before := m.input.Value()
			m.handleComposerClipboardText(msg)
			if scenario == "resize" {
				if m.input.Value() != "new beta" {
					t.Fatal("layout invalidated clipboard request")
				}
			} else if m.input.Value() != before {
				t.Fatal("stale/failed clipboard replaced current draft")
			}
		})
	}
}

func TestInlinePositionDoesNotHitPadding(t *testing.T) {
	in := NewInput()
	in.SetWidth(40)
	in.InsertImagePlaceholder(1)
	in.syncHeight()
	offset, hit := in.RunePositionAt(inputPromptWidth+2, 0)
	if !hit || offset != 2 {
		t.Fatal("image character not hit")
	}
	if _, hit := in.RunePositionAt(inputPromptWidth+len(in.Value())+1, 0); hit {
		t.Fatal("padding hit adjacent image")
	}
	if _, hit := in.RunePositionAt(0, 0); hit {
		t.Fatal("prompt hit adjacent image")
	}
}

func TestKeyboardSelectionTreatsObjectsAsOneCharacter(t *testing.T) {
	in := NewInput()
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

func TestSelectionReplacementOpensAtMention(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetValue("sample")
	m.input.SelectRuneRange(0, len(m.input.Value()))
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: '@', Text: "@"}))
	if m.input.Value() != "@" || !m.atMentionOpen || m.atMentionTriggerCol != 1 {
		t.Fatal("replacement did not start file completion")
	}
}

func TestDoubleClickWordAfterEmojiUsesGraphemeColumns(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue("👩‍💻 beta")
	m.input.syncHeight()
	mouse := tea.Mouse{X: inputPromptWidth + 4, Y: 0, Button: tea.MouseLeft}
	m.handleInputSelectionClick(mouse, mouse.X, mouse.Y)
	m.handleInputSelectionClick(mouse, mouse.X, mouse.Y)
	if m.input.SelectionText() != "beta" {
		t.Fatalf("emoji shifted word hit: %q", m.input.SelectionText())
	}
}
