package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tui/composer"
)

func TestInlinePasteInsertionAfterEmptyMouseDragPreservesObjects(t *testing.T) {
	for _, kind := range []string{"image", "large-paste"} {
		t.Run(kind, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			m.mode = ModeInsert
			if !m.input.InsertLargePaste(strings.Repeat("sample\n", 11)) {
				t.Fatal("initial paste was not admitted")
			}
			original := m.input.InlinePastes()[0]
			m.input.SyncHeight()
			m.recalcViewportSize()
			m.ensureLayout()
			mouse := tea.Mouse{X: m.layout.input.Min.X + composer.PromptWidth + 1, Y: m.layout.input.Min.Y + 1, Button: tea.MouseLeft}
			hits := mouseHitZones{inInputZone: true}
			m.handleInputZoneMouseClick(mouse, hits)
			m.handleMouseMotion(mouse, hits)
			if m.input.HasSelection() {
				t.Fatal("motion within the same character should leave an empty selection")
			}
			var inserted bool
			if kind == "image" {
				inserted = m.input.InsertImagePlaceholder(1)
			} else {
				inserted = m.input.InsertLargePaste(strings.Repeat("other\n", 11))
			}
			if !inserted || !validComposerTokens(&m.input) {
				t.Fatalf("insertion damaged inline objects: display=%q tokens=%+v", m.input.DisplayValue(), m.input.InlinePastes())
			}
			objects := m.input.InlinePastes()
			if len(objects) != 2 || objects[0] != original || objects[1].Start != original.End {
				t.Fatalf("insertion should preserve the original object and append the new one: %+v", objects)
			}
		})
	}
}
