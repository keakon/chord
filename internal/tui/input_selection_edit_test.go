package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tui/composer"
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

func TestSelectionKeyboardSharesMouseRange(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetValue("alpha beta")
	m.input.SelectRuneRange(2, 5)
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModShift}))
	if m.input.SelectionText() != "pha " {
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
	if m.input.SelectionText() != m.input.Value() {
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
		m.attachments = append(m.attachments, Attachment{FileName: composer.ImagePlaceholder(idx) + ".png", MimeType: "image/png", Data: []byte{byte(idx)}, InlineImagePlaceholder: true})
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
	if len(m.attachments) != 2 || m.attachments[0].Data[0] != 3 || len(kept) != 2 || kept[0].RawContent != "[image1]" || kept[1].RawContent != pastes[3].RawContent || !validComposerTokens(&m.input) {
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
				m.input.SetCursorOffset(0)
				m.input.SetCursorOffset(5)
			case "selection":
				m.input.ClearSelection()
				m.input.SelectRuneRange(0, 5)
			case "agent":
				m.focusedAgentID = "worker"
			case "session":
				m.sessionTranscriptEpoch++
			case "boundary":
				m.input.BeginDraft()
			case "resize":
				m.input.SetWidth(10)
				m.input.SyncHeight()
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
	m.input.SyncHeight()
	mouse := tea.Mouse{X: composer.PromptWidth + 4, Y: 0, Button: tea.MouseLeft}
	m.handleInputSelectionClick(mouse, mouse.X, mouse.Y)
	m.handleInputSelectionClick(mouse, mouse.X, mouse.Y)
	if m.input.SelectionText() != "beta" {
		t.Fatalf("emoji shifted word hit: %q", m.input.SelectionText())
	}
}
