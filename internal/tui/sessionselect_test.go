package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func sessionSelectRuneKey(text string) tea.KeyPressMsg {
	runes := []rune(text)
	if len(runes) == 0 {
		return tea.KeyPressMsg(tea.Key{})
	}
	return tea.KeyPressMsg(tea.Key{Code: runes[0], Text: text})
}

func testSessionSummaries() []agent.SessionSummary {
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.Local)
	return []agent.SessionSummary{
		{
			ID:                       "sess-100",
			OriginalFirstUserMessage: "Fix IME enter behavior",
			LastModTime:              base,
		},
		{
			ID:               "sess-200",
			FirstUserMessage: "Line one\nLine two",
			LastModTime:      base.Add(-time.Hour),
		},
		{
			ID:                       "sess-300",
			OriginalFirstUserMessage: "Legacy parser cleanup",
			ForkedFrom:               "parent-001",
			LastModTime:              base.Add(-2 * time.Hour),
		},
	}
}

func newSessionSelectTestModel(options []agent.SessionSummary) Model {
	m := NewModelWithSize(nil, 120, 32)
	m.mode = ModeSessionSelect
	m.sessionSelect = sessionSelectState{
		options:      append([]agent.SessionSummary(nil), options...),
		list:         NewOverlayList(nil, m.sessionSelectMaxVisible()),
		prevMode:     ModeNormal,
		searchCorpus: buildSessionSearchCorpus(options),
	}
	m.rebuildSessionSelectFilteredView(false)
	return m
}

func TestBuildSessionSearchCorpusAndFilterSessionOptions(t *testing.T) {
	options := testSessionSummaries()
	corpus := buildSessionSearchCorpus(options)
	if len(corpus) != len(options) {
		t.Fatalf("len(corpus) = %d, want %d", len(corpus), len(options))
	}
	if !strings.Contains(corpus[1], "line one line two") {
		t.Fatalf("corpus[1] = %q, want normalized newline text", corpus[1])
	}

	if got := filterSessionOptions(corpus, ""); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("filter empty = %v, want [0 1 2]", got)
	}
	if got := filterSessionOptions(corpus, "SESS-200"); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("filter by id = %v, want [1]", got)
	}
	if got := filterSessionOptions(corpus, "parent cleanup"); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("filter by multi token = %v, want [2]", got)
	}
	if got := filterSessionOptions(corpus, "missing-token"); len(got) != 0 {
		t.Fatalf("filter missing = %v, want empty", got)
	}
}

func TestRebuildSessionSelectFilteredViewResetsCursorAndHandlesNoMatch(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())

	if got, want := len(m.sessionSelect.filteredIdx), m.sessionSelect.list.Len(); got != want {
		t.Fatalf("filteredIdx/list mismatch: %d vs %d", got, want)
	}

	m.sessionSelect.list.SetCursor(2)
	m.sessionSelect.filter = "ime"
	m.rebuildSessionSelectFilteredView(true)
	if got := m.sessionSelect.list.CursorAt(); got != 0 {
		t.Fatalf("cursor after reset rebuild = %d, want 0", got)
	}
	if got := len(m.sessionSelect.filteredIdx); got != 1 {
		t.Fatalf("len(filteredIdx) after ime filter = %d, want 1", got)
	}

	m.sessionSelect.filter = "no-match"
	m.rebuildSessionSelectFilteredView(true)
	if got := len(m.sessionSelect.filteredIdx); got != 0 {
		t.Fatalf("len(filteredIdx) no-match = %d, want 0", got)
	}
	if got := m.sessionSelect.list.Len(); got != 0 {
		t.Fatalf("list len no-match = %d, want 0", got)
	}
}

func TestSessionSelectCurrentOptionMappingWithFilter(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())

	m.sessionSelect.filter = "parent"
	m.rebuildSessionSelectFilteredView(true)

	if got := m.sessionSelectCurrentOptionIndex(); got != 2 {
		t.Fatalf("current option index = %d, want 2", got)
	}
	sel, ok := m.sessionSelectCurrentOption()
	if !ok || sel.ID != "sess-300" {
		t.Fatalf("current option = %#v, %t, want sess-300", sel, ok)
	}

	m.sessionSelect.filter = "missing"
	m.rebuildSessionSelectFilteredView(true)
	if got := m.sessionSelectCurrentOptionIndex(); got != -1 {
		t.Fatalf("current option index with empty filtered list = %d, want -1", got)
	}
}

func TestSessionSelectFilterFocusEscBehavior(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())

	_ = m.handleSessionSelectKey(sessionSelectRuneKey("/"))
	if !m.sessionSelect.filterFocused {
		t.Fatal("expected filter focus after /")
	}
	if m.mode != ModeSessionSelect {
		t.Fatalf("mode = %v, want ModeSessionSelect", m.mode)
	}

	_ = m.handleSessionSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	if m.mode != ModeSessionSelect {
		t.Fatalf("mode after first esc = %v, want ModeSessionSelect", m.mode)
	}
	if m.sessionSelect.filterFocused {
		t.Fatal("first esc in filter focus should only exit focus")
	}

	_ = m.handleSessionSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	if m.mode != ModeNormal {
		t.Fatalf("mode after second esc = %v, want ModeNormal", m.mode)
	}
}

func TestSessionSelectFilterFocusTreatsJKAsInput(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())
	m.sessionSelect.list.SetCursor(1)

	_ = m.handleSessionSelectKey(sessionSelectRuneKey("/"))
	_ = m.handleSessionSelectKey(sessionSelectRuneKey("j"))

	if m.sessionSelect.filter != "j" {
		t.Fatalf("filter after j in focus = %q, want j", m.sessionSelect.filter)
	}
	if got := m.sessionSelect.list.CursorAt(); got != 0 {
		t.Fatalf("cursor after j input = %d, want reset to 0", got)
	}
}

func TestSelectSessionAtCursorUsesFilteredMapping(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 32)
	m.mode = ModeSessionSelect
	options := testSessionSummaries()
	m.sessionSelect = sessionSelectState{
		options:      options,
		list:         NewOverlayList(nil, m.sessionSelectMaxVisible()),
		prevMode:     ModeNormal,
		searchCorpus: buildSessionSearchCorpus(options),
	}
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.filter = "parent"
	m.rebuildSessionSelectFilteredView(true)

	_ = m.selectSessionAtCursor()
	if got := backend.resumeIDs; len(got) != 1 || got[0] != "sess-300" {
		t.Fatalf("ResumeSessionID() calls = %+v, want [sess-300]", got)
	}
}

func TestOpenSessionDeleteConfirmUsesFilteredMapping(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())
	m.sessionSelect.filter = "parent"
	m.rebuildSessionSelectFilteredView(true)

	_ = m.openSessionDeleteConfirm()
	if m.mode != ModeSessionDeleteConfirm {
		t.Fatalf("mode = %v, want ModeSessionDeleteConfirm", m.mode)
	}
	if m.sessionDeleteConfirm.session == nil || m.sessionDeleteConfirm.session.ID != "sess-300" {
		t.Fatalf("delete target = %#v, want sess-300", m.sessionDeleteConfirm.session)
	}
}

func TestConfirmSessionDeletionRemovesBySessionID(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 32)
	options := testSessionSummaries()
	m.mode = ModeSessionDeleteConfirm
	m.sessionSelect = sessionSelectState{
		options:      append([]agent.SessionSummary(nil), options...),
		list:         NewOverlayList(nil, m.sessionSelectMaxVisible()),
		prevMode:     ModeSessionSelect,
		searchCorpus: buildSessionSearchCorpus(options),
	}
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.list.SetCursor(0) // points to sess-100
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session:  &agent.SessionSummary{ID: "sess-300"},
		prevMode: ModeSessionSelect,
	}

	_ = m.confirmSessionDeletion()
	if got := backend.deleteSessionIDs; len(got) != 1 || got[0] != "sess-300" {
		t.Fatalf("DeleteSession() calls = %+v, want [sess-300]", got)
	}
	for _, option := range m.sessionSelect.options {
		if option.ID == "sess-300" {
			t.Fatalf("session options still contain deleted ID: %+v", m.sessionSelect.options)
		}
	}
}

func TestRenderSessionSelectDialogShowsFilterHintAndNoMatch(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())

	plain := stripANSI(m.renderSessionSelectDialog())
	if !strings.Contains(plain, "filter: (press / to search)") {
		t.Fatalf("dialog missing filter hint:\n%s", plain)
	}
	if !strings.Contains(plain, "3/3") {
		t.Fatalf("dialog missing filtered count:\n%s", plain)
	}

	m.sessionSelect.filter = "missing"
	m.rebuildSessionSelectFilteredView(true)
	plain = stripANSI(m.renderSessionSelectDialog())
	if !strings.Contains(plain, `No sessions match "missing"`) {
		t.Fatalf("dialog missing no-match text:\n%s", plain)
	}
	if !strings.Contains(plain, "0/3") {
		t.Fatalf("dialog missing no-match count:\n%s", plain)
	}
}

func TestSetSessionSelectFilterFocusedInvalidatesDialogCache(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())
	_ = m.renderSessionSelectDialog()
	if m.sessionSelect.renderCacheText == "" {
		t.Fatal("expected non-empty render cache after first render")
	}

	m.setSessionSelectFilterFocused(true)
	if m.sessionSelect.renderCacheText != "" {
		t.Fatal("filter focus change should invalidate dialog cache")
	}
	_ = m.renderSessionSelectDialog()
	m.setSessionSelectFilterFocused(false)
	if m.sessionSelect.renderCacheText != "" {
		t.Fatal("focus exit should invalidate dialog cache")
	}
}

func TestSessionSelectOptionIndexAtUsesListContentBaseRow(t *testing.T) {
	m := newSessionSelectTestModel(testSessionSummaries())
	_ = m.renderSessionSelectDialog()

	dialogRect := m.overlayRect(m.renderSessionSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + sessionSelectListBaseRow
	idx, ok := m.sessionSelectOptionIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first list row")
	}
	if idx != 0 {
		t.Fatalf("hit-test index = %d, want 0", idx)
	}
}
