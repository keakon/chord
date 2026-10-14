package composer

import (
	"testing"

	"github.com/keakon/bubbles/v2/textarea"
)

func TestSpacingMarksKeepInputCursorAndRowsAligned(t *testing.T) {
	const glyph = "a\u093e\u093e"
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(6) // Two prompt columns and four content columns.
	in.SetValue(glyph + glyph)
	in.SyncHeight()
	rows := in.selectionDisplayRows()
	if len(rows) != 2 || rows[0].text != glyph+glyph || rows[1].text != "" {
		t.Fatalf("wrapped rows = %#v", rows)
	}
	in.SetCursorPosition(0, 3)
	in.Focus()
	cursor := in.Cursor()
	if cursor == nil || cursor.Position.X != PromptWidth+2 || cursor.Position.Y != 0 {
		t.Fatalf("cursor = %#v, want (%d, 0)", cursor, PromptWidth+2)
	}
	if offset, ok := in.SelectionPositionAt(PromptWidth+3, 0); !ok || offset != 3 {
		t.Fatalf("second cluster hit = %d, %v", offset, ok)
	}
}
