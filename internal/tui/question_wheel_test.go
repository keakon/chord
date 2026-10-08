package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

func questionWheelTestModel(t *testing.T, width, height int, question string) Model {
	t.Helper()
	m := NewModelWithSize(nil, width, height)
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{
			Header:   "Choice",
			Question: question,
			Options:  []tools.QuestionOption{{ID: "plain", Label: "Plain"}},
		}},
		selected: map[int]bool{},
	}
	m.renderQuestionDialog()
	if m.question.dialogRect.Empty() {
		t.Fatal("question dialog rendered without bounds")
	}
	return m
}

func TestQuestionWheelScrollsBodyOnlyUnderPointerWithRoom(t *testing.T) {
	m := questionWheelTestModel(t, 80, 12, strings.Repeat("Read this before choosing. ", 20))
	if m.question.bodyHeight <= m.question.visibleBodyHeight {
		t.Fatal("question body does not overflow; the wheel has no room in any direction")
	}
	inside := m.question.dialogRect.Min
	inX, inY := inside.X+1, inside.Y+1
	outside := m.question.dialogRect.Min
	outX, outY := outside.X-1, outside.Y-1
	wheel := func(x, y int, button tea.MouseButton) (tea.Cmd, bool) {
		return m.handleModalMouseMsg(tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: button}))
	}

	// Top edge under the pointer: the body cannot move up, so the wheel reads
	// the conversation instead of doing nothing.
	if _, handled := wheel(inX, inY, tea.MouseWheelUp); !handled {
		t.Fatal("wheel at the body top edge was dropped")
	}
	if m.question.scrollOffset != 0 || m.pendingScrollDelta != -mouseWheelScrollStep {
		t.Fatalf("wheel at the body top edge: offset=%d pending=%d", m.question.scrollOffset, m.pendingScrollDelta)
	}

	// Pointer over the dialog with room down: the wheel scrolls the question
	// and never touches the conversation.
	m.pendingScrollDelta = 0
	if _, handled := wheel(inX, inY, tea.MouseWheelDown); !handled {
		t.Fatal("wheel over the scrollable dialog was dropped")
	}
	if m.question.scrollOffset != mouseWheelScrollStep || m.pendingScrollDelta != 0 {
		t.Fatalf("wheel over the dialog: offset=%d pending=%d", m.question.scrollOffset, m.pendingScrollDelta)
	}

	// Pointer outside: the wheel always reads the conversation.
	if _, handled := wheel(outX, outY, tea.MouseWheelDown); !handled {
		t.Fatal("wheel outside the dialog was dropped")
	}
	if m.pendingScrollDelta != mouseWheelScrollStep || m.question.scrollOffset != mouseWheelScrollStep {
		t.Fatalf("wheel outside the dialog: offset=%d pending=%d", m.question.scrollOffset, m.pendingScrollDelta)
	}

	// Bottom edge under the pointer: the body cannot move down either.
	m.pendingScrollDelta = 0
	m.question.scrollOffset = m.question.bodyHeight - m.question.visibleBodyHeight
	bottom := m.question.scrollOffset
	if _, handled := wheel(inX, inY, tea.MouseWheelDown); !handled {
		t.Fatal("wheel at the body bottom edge was dropped")
	}
	if m.question.scrollOffset != bottom || m.pendingScrollDelta != mouseWheelScrollStep {
		t.Fatalf("wheel at the body bottom edge: offset=%d pending=%d", m.question.scrollOffset, m.pendingScrollDelta)
	}
}

func TestQuestionWheelReadsConversationWhenBodyFits(t *testing.T) {
	m := questionWheelTestModel(t, 80, 24, "Pick a format")
	if m.question.bodyHeight > m.question.visibleBodyHeight {
		t.Fatal("question body unexpectedly overflows at 80x24")
	}
	inside := m.question.dialogRect.Min
	if _, handled := m.handleModalMouseMsg(tea.MouseWheelMsg(tea.Mouse{X: inside.X + 1, Y: inside.Y + 1, Button: tea.MouseWheelDown})); !handled {
		t.Fatal("wheel over a dialog that cannot scroll was dropped")
	}
	if m.pendingScrollDelta != mouseWheelScrollStep || m.question.scrollOffset != 0 {
		t.Fatalf("pending=%d offset=%d", m.pendingScrollDelta, m.question.scrollOffset)
	}
}

func TestQuestionPagingReachesTranscriptAtBodyEdges(t *testing.T) {
	m := questionWheelTestModel(t, 80, 12, strings.Repeat("Read this before choosing. ", 20))
	for i := range 20 {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("sample ", 40)})
	}
	m.layout = m.generateLayout(m.width, m.height)
	maxOffset := m.question.bodyHeight - m.question.visibleBodyHeight
	if maxOffset <= 0 {
		t.Fatal("question body does not overflow")
	}
	paging := func(code rune) { m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: code})) }

	// Body room first: paging scrolls the question, not the conversation.
	viewportOffset := m.viewport.offset
	paging(tea.KeyPgDown)
	if m.question.scrollOffset == 0 || m.viewport.offset != viewportOffset {
		t.Fatalf("page down moved the wrong surface: body=%d viewport=%d->%d", m.question.scrollOffset, viewportOffset, m.viewport.offset)
	}

	// Bottom edge: the same key pages the conversation.
	m.question.scrollOffset = maxOffset
	m.viewport.ScrollToTop()
	viewportOffset = m.viewport.offset
	paging(tea.KeyPgDown)
	if m.viewport.offset <= viewportOffset {
		t.Fatalf("page down at the body edge did not page the conversation: %d -> %d", viewportOffset, m.viewport.offset)
	}

	// Top edge: paging walks back up the conversation.
	m.question.scrollOffset = 0
	viewportOffset = m.viewport.offset
	paging(tea.KeyPgUp)
	if m.viewport.offset >= viewportOffset {
		t.Fatalf("page up at the body edge did not page the conversation: %d -> %d", viewportOffset, m.viewport.offset)
	}
}
