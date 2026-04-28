package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/muesli/reflow/truncate"
)

// ---------------------------------------------------------------------------
// Search types
// ---------------------------------------------------------------------------

// MatchPosition identifies a single search match within the viewport's blocks.
type MatchPosition struct {
	BlockIndex  int // index into the viewport's blocks slice
	BlockID     int // stable block id for deferred/windowed transcripts
	LineOffset  int // absolute line offset from the top of all content (for scrolling)
	InnerOffset int // approximate line offset within the matched block
	Query       string
}

// SearchState holds the current state of an in-progress or completed search.
type SearchState struct {
	Query   string          // the search query (empty when no search is active)
	Matches []MatchPosition // all match positions, in order
	Current int             // index into Matches for the currently focused match (-1 if none)
	Active  bool            // true while the search session remains active
}

// HasMatches reports whether the search has any results.
func (s *SearchState) HasMatches() bool {
	return len(s.Matches) > 0
}

// CurrentMatch returns the currently focused match position.
// Returns zero value and false if there are no matches.
func (s *SearchState) CurrentMatch() (MatchPosition, bool) {
	if len(s.Matches) == 0 || s.Current < 0 || s.Current >= len(s.Matches) {
		return MatchPosition{}, false
	}
	return s.Matches[s.Current], true
}

// ---------------------------------------------------------------------------
// SearchModel — Bubble Tea sub-component for search input
// ---------------------------------------------------------------------------

// SearchModel wraps a textinput for the search bar and manages match state.
// It is embedded in the main TUI Model (not wired in this file — integration
// is done separately by modifying app.go).
type SearchModel struct {
	Input    textinput.Model
	State    SearchState
	PrevMode Mode // mode to restore when search is dismissed
}

// NewSearchModel creates a search model with a focused text input.
func NewSearchModel(prevMode Mode) SearchModel {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.SetStyles(textinput.Styles{
		Focused: textinput.StyleState{Prompt: SearchPromptStyle},
		Blurred: textinput.StyleState{Prompt: SearchPromptStyle},
	})
	ti.Placeholder = "search..."
	ti.CharLimit = 256
	ti.Focus()
	return SearchModel{
		Input:    ti,
		PrevMode: prevMode,
		State: SearchState{
			Current: -1,
		},
	}
}

// Update delegates a Bubble Tea message to the search input.
func (sm SearchModel) Update(msg tea.Msg) (SearchModel, tea.Cmd) {
	var cmd tea.Cmd
	sm.Input, cmd = sm.Input.Update(msg)
	return sm, cmd
}

// View renders the search bar (input + match count indicator).
func (sm SearchModel) View(width int) string {
	inputView := sm.Input.View()
	if !sm.State.Active || sm.State.Query == "" {
		return inputView
	}
	total := len(sm.State.Matches)
	current := sm.State.Current + 1 // 1-based for display
	if total == 0 {
		current = 0
	}
	counter := DimStyle.Render(fmt.Sprintf(" [%d/%d]", current, total))
	// Truncate input view to leave room for counter (ANSI-aware).
	maxInput := width - len(fmt.Sprintf(" [%d/%d]", current, total)) - 2
	if maxInput < 10 {
		maxInput = 10
	}
	inputView = truncate.String(inputView, uint(maxInput))
	return inputView + counter
}

// ---------------------------------------------------------------------------
// Search functions
// ---------------------------------------------------------------------------

// FindMatches performs a case-insensitive search across all block content and
// returns the positions of blocks whose plain-text content contains the query.
//
// The search examines the raw Content field of each block (which is the
// unstyled text the user actually typed or the LLM produced). This avoids
// false matches on ANSI escape codes from rendered output.
//
// Each match includes the absolute LineOffset so the viewport can scroll
// directly to the match position.
func approximateSearchMatchInnerOffset(block *Block, query string, width int) int {
	if block == nil || query == "" {
		return 0
	}
	if width <= 0 {
		width = 80
	}
	lowerQuery := strings.ToLower(query)
	lines := wrapText(block.searchableTextLower(), width)
	for i, line := range lines {
		if strings.Contains(line, lowerQuery) {
			return i
		}
	}
	if block.ToolName == "Read" && block.ResultContent != "" {
		rows, _ := parseReadDisplayLines(block.ResultContent)
		for i, row := range rows {
			if strings.Contains(strings.ToLower(row.Content), lowerQuery) {
				return i + 1
			}
		}
	}
	return 0
}

func blockVisibleForSearch(block *Block, width int) bool {
	if block == nil {
		return false
	}
	if width <= 0 {
		width = 80
	}
	if block.Type == BlockThinking {
		inspect, temporary := block.inspectionBlock()
		if inspect == nil {
			return false
		}
		content := strings.TrimSpace(preprocessThinkingMarkdown(inspect.Content))
		visible := content != ""
		if temporary {
			inspect.InvalidateCache()
		}
		return visible
	}
	return true
}

func searchDiagnosticArtifactExcluded(blockType BlockType, searchableTextLower string) bool {
	searchableTextLower = strings.TrimSpace(searchableTextLower)
	if searchableTextLower == "" {
		return false
	}
	switch blockType {
	case BlockToolCall, BlockToolResult:
		return strings.Contains(searchableTextLower, "tui-dumps/")
	default:
		return false
	}
}

func renderedSearchMatchInnerOffset(block *Block, query string, width int) int {
	if block == nil || query == "" {
		return 0
	}
	if width <= 0 {
		width = 80
	}
	inspect, temporary := block.inspectionBlock()
	if inspect == nil {
		return 0
	}
	lowerQuery := strings.ToLower(query)
	lines := inspect.Render(width, "")
	for i, line := range lines {
		if strings.Contains(strings.ToLower(stripANSI(line)), lowerQuery) {
			if temporary {
				inspect.InvalidateCache()
			}
			return i
		}
	}
	if temporary {
		inspect.InvalidateCache()
	}
	return approximateSearchMatchInnerOffset(inspect, query, width)
}

func searchMatchInnerOffset(block *Block, query string, width int) int {
	if block == nil {
		return 0
	}
	switch block.Type {
	case BlockToolCall, BlockToolResult:
		return renderedSearchMatchInnerOffset(block, query, width)
	default:
		return approximateSearchMatchInnerOffset(block, query, width)
	}
}

func FindMatches(blocks []*Block, query string) []MatchPosition {
	if query == "" || len(blocks) == 0 {
		return nil
	}

	lowerQuery := strings.ToLower(query)
	var matches []MatchPosition
	lineOffset := 0

	for i, block := range blocks {
		inspect, temporary := block.inspectionBlock()
		if inspect == nil {
			continue
		}
		blockLineCount := inspect.LineCount(80) // approximate; recalculated when scrolling
		searchableTextLower := inspect.searchableTextLower()
		if strings.Contains(searchableTextLower, lowerQuery) && blockVisibleForSearch(inspect, 80) && !searchDiagnosticArtifactExcluded(inspect.Type, searchableTextLower) {
			matches = append(matches, MatchPosition{
				BlockIndex:  i,
				BlockID:     inspect.ID,
				LineOffset:  lineOffset,
				InnerOffset: searchMatchInnerOffset(inspect, query, 80),
				Query:       query,
			})
		}
		if temporary {
			inspect.InvalidateCache()
		}
		lineOffset += blockLineCount
	}

	return matches
}

// FindMatchesAtWidth performs FindMatches but uses the given width for accurate
// line offset calculation. Use this when the viewport width is known.
func FindMatchesAtWidth(blocks []*Block, query string, width int) []MatchPosition {
	if query == "" || len(blocks) == 0 {
		return nil
	}
	if width <= 0 {
		width = 80
	}

	lowerQuery := strings.ToLower(query)
	var matches []MatchPosition
	lineOffset := 0

	for i, block := range blocks {
		inspect, temporary := block.inspectionBlock()
		if inspect == nil {
			continue
		}
		blockLineCount := inspect.LineCount(width)
		searchableTextLower := inspect.searchableTextLower()
		if strings.Contains(searchableTextLower, lowerQuery) && blockVisibleForSearch(inspect, width) && !searchDiagnosticArtifactExcluded(inspect.Type, searchableTextLower) {
			matches = append(matches, MatchPosition{
				BlockIndex:  i,
				BlockID:     inspect.ID,
				LineOffset:  lineOffset,
				InnerOffset: searchMatchInnerOffset(inspect, query, width),
				Query:       query,
			})
		}
		if temporary {
			inspect.InvalidateCache()
		}
		lineOffset += blockLineCount
	}

	return matches
}

// NextMatch advances the search state to the next match and returns it.
// Wraps around to the first match when at the end.
// Returns the match position and true if a match exists, zero value and false
// if there are no matches.
func NextMatch(state *SearchState) (MatchPosition, bool) {
	if len(state.Matches) == 0 {
		return MatchPosition{}, false
	}
	state.Current++
	if state.Current >= len(state.Matches) {
		state.Current = 0 // wrap around
	}
	return state.Matches[state.Current], true
}

// PrevMatch moves the search state to the previous match and returns it.
// Wraps around to the last match when at the beginning.
// Returns the match position and true if a match exists, zero value and false
// if there are no matches.
func PrevMatch(state *SearchState) (MatchPosition, bool) {
	if len(state.Matches) == 0 {
		return MatchPosition{}, false
	}
	state.Current--
	if state.Current < 0 {
		state.Current = len(state.Matches) - 1 // wrap around
	}
	return state.Matches[state.Current], true
}

// ExecuteSearch runs a search with the given query, updating the state with
// results. The viewport's blocks and width are used for accurate match
// positioning. When anchorBlockIndex is valid, the initial current match is the
// first hit at or after that block; otherwise it starts at the first match.
func ExecuteSearch(state *SearchState, blocks []*Block, query string, width int, anchorBlockIndex int) {
	state.Query = query
	state.Active = true
	if query == "" {
		state.Matches = nil
		state.Current = -1
		return
	}
	state.Matches = FindMatchesAtWidth(blocks, query, width)
	state.Current = initialSearchCurrentIndex(state.Matches, anchorBlockIndex)
}

// ClearSearch resets the search state, removing all match highlights.
func ClearSearch(state *SearchState) {
	state.Query = ""
	state.Matches = nil
	state.Current = -1
	state.Active = false
}

// ---------------------------------------------------------------------------
// Search key bindings (constants for documentation — wiring is in app.go)
// ---------------------------------------------------------------------------
//
// These key bindings should be added to the KeyMap struct and wired into the
// appropriate mode handlers during integration:
//
//   Normal mode:
//     "/" → enter search mode (ModeSearch), show search input
//     "n" → next match (when search results exist)
//     "N" → previous match (when search results exist)
//
//   Search mode (ModeSearch):
//     "enter" → confirm search, execute query, go to first match, return to Normal
//     "esc"   → cancel search, clear results, return to previous mode
//     other   → delegated to textinput for typing

// SearchKeyBindings holds the default key strings for search actions.
// These are provided as reference for the integration layer (app.go / keymap.go).
var SearchKeyBindings = struct {
	EnterSearch string
	SearchNext  string
	SearchPrev  string
	Confirm     string
	Cancel      string
}{
	EnterSearch: "/",
	SearchNext:  "n",
	SearchPrev:  "N",
	Confirm:     "enter",
	Cancel:      "esc",
}
