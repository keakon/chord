package tui

import (
	"fmt"
	"html"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/bubbles/v2/textinput"
	tea "github.com/keakon/bubbletea/v2"
	"github.com/muesli/reflow/truncate"

	"github.com/keakon/chord/internal/tools"
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
// The search examines the unstyled source fields that feed each block's
// visible text. This avoids false matches on ANSI escape codes while covering
// structured displays such as tool diffs, reports, and attachment labels.
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
	if offset, ok := wrappedSearchMatchLineOffset(block.searchableTextLower(), lowerQuery, width); ok {
		return offset
	}
	if block.ToolName == tools.NameRead && block.ResultContent != "" {
		rows, _ := parseReadDisplayLines(block.ResultContent, 1)
		for i, row := range rows {
			if strings.Contains(strings.ToLower(row.Content), lowerQuery) {
				return i + 1
			}
		}
	}
	return 0
}

func wrappedSearchMatchLineOffset(textLower, queryLower string, width int) (int, bool) {
	matchStart := strings.Index(textLower, queryLower)
	if matchStart < 0 {
		return 0, false
	}
	matchEnd := matchStart + len(queryLower)
	for matchEnd < len(textLower) {
		r, size := utf8.DecodeRuneInString(textLower[matchEnd:])
		if unicode.IsSpace(r) {
			break
		}
		matchEnd += size
	}
	for i, line := range wrapText(textLower[:matchEnd], width) {
		if strings.Contains(line, queryLower) {
			return i, true
		}
	}
	return 0, false
}

func assistantMarkdownMayContainQuery(content, queryLower string) bool {
	if content == "" || queryLower == "" {
		return false
	}
	if assistantMarkdownSourceMayContainQuery(content, queryLower) {
		return true
	}
	if strings.Contains(content, "&") {
		decoded := html.UnescapeString(content)
		if decoded != content {
			return assistantMarkdownSourceMayContainQuery(decoded, queryLower)
		}
	}
	return false
}

func assistantMarkdownSourceMayContainQuery(content, queryLower string) bool {
	if isASCII(content) && isASCII(queryLower) {
		if assistantMarkdownContainsASCII(content, queryLower) {
			return true
		}
		if strings.Contains(content, "](") || strings.Contains(content, "<") {
			return asciiSubsequenceFold(content, queryLower)
		}
		return false
	}
	var visibleSyntax strings.Builder
	visibleSyntax.Grow(len(content))
	for _, r := range content {
		if !markdownSearchIgnorableRune(r) {
			visibleSyntax.WriteRune(unicode.ToLower(r))
		}
	}
	if strings.Contains(visibleSyntax.String(), queryLower) {
		return true
	}
	// Link targets and HTML/comment bodies can disappear entirely when rendered.
	// A linear subsequence check is a deliberately broad candidate filter; the
	// rendered-line validation that follows rejects hidden-only false positives.
	if strings.Contains(content, "](") || strings.Contains(content, "<") {
		if isASCII(queryLower) {
			return asciiSubsequenceFold(content, queryLower)
		}
	}
	return false
}

func assistantMarkdownContainsASCII(content, queryLower string) bool {
	const stackPrefixLimit = 256
	var stackPrefix [stackPrefixLimit]int
	prefix := stackPrefix[:]
	if len(queryLower) <= len(stackPrefix) {
		prefix = prefix[:len(queryLower)]
	} else {
		prefix = make([]int, len(queryLower))
	}
	for i, matched := 1, 0; i < len(queryLower); i++ {
		for matched > 0 && queryLower[i] != queryLower[matched] {
			matched = prefix[matched-1]
		}
		if queryLower[i] == queryLower[matched] {
			matched++
		}
		prefix[i] = matched
	}
	matched := 0
	for i := 0; i < len(content); i++ {
		c := content[i]
		if markdownSearchIgnorableRune(rune(c)) {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		for matched > 0 && c != queryLower[matched] {
			matched = prefix[matched-1]
		}
		if c == queryLower[matched] {
			matched++
			if matched == len(queryLower) {
				return true
			}
		}
	}
	return false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func asciiSubsequenceFold(content, queryLower string) bool {
	matched := 0
	for i := 0; i < len(content) && matched < len(queryLower); i++ {
		c := content[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == queryLower[matched] {
			matched++
		}
	}
	return matched == len(queryLower)
}

func markdownSearchIgnorableRune(r rune) bool {
	switch r {
	case '*', '_', '~', '`', '[', ']', '(', ')', '#', '>', '!':
		return true
	default:
		return false
	}
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
	if offset, ok := renderedSearchMatchLineOffset(block, query, width); ok {
		return offset
	}
	return approximateSearchMatchInnerOffset(block, query, width)
}

func renderedSearchMatchLineOffset(block *Block, query string, width int) (int, bool) {
	if block == nil || query == "" {
		return 0, false
	}
	if width <= 0 {
		width = 80
	}
	inspect, temporary := block.inspectionBlock()
	if inspect == nil {
		return 0, false
	}
	if temporary {
		defer inspect.InvalidateCache()
	}
	if offset, ok := inspect.searchMatchLineOffsetInCachedRender(query, width); ok {
		return offset, true
	}

	revealed := cloneBlockForDeferredSource(inspect)
	if !revealSearchMatchedBlock(revealed) {
		return 0, false
	}
	revealed.InvalidateCache()
	return searchMatchLineOffsetInRenderedLines(revealed.Render(width, ""), query, width)
}

func (b *Block) searchMatchLineOffsetInCachedRender(query string, width int) (int, bool) {
	if b == nil || query == "" {
		return 0, false
	}
	if width <= 0 {
		width = 80
	}
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if b.searchMatchReady && b.searchMatchWidth == width && b.searchMatchQueryLower == lowerQuery {
		return b.searchMatchOffset, b.searchMatchFound
	}
	offset, found := searchMatchLineOffsetInRenderedLines(b.RenderRange(width, "", 0, b.LineCount(width)), lowerQuery, width)
	b.searchMatchQueryLower = lowerQuery
	b.searchMatchWidth = width
	b.searchMatchOffset = offset
	b.searchMatchFound = found
	b.searchMatchReady = true
	b.hotBytesMemoValid = false
	return offset, found
}

func searchMatchLineOffsetInRenderedLines(lines []string, query string, width int) (int, bool) {
	for i, line := range lines {
		line = expandTabsForDisplayANSI(line, preformattedTabWidth)
		line = truncateLineToDisplayWidth(line, width)
		if _, _, ok := searchMatchColumnRangeInLine(line, query); ok {
			return i, true
		}
	}
	return 0, false
}

func searchMatchInnerOffset(block *Block, query string, width int) int {
	if block == nil {
		return 0
	}
	offset, _ := visibleSearchMatchInnerOffset(block, query, width)
	return offset
}

func visibleSearchMatchInnerOffset(block *Block, query string, width int) (int, bool) {
	if block == nil {
		return 0, false
	}
	switch block.Type {
	case BlockUser, BlockAssistant, BlockThinking, BlockToolCall, BlockToolResult, BlockCompactionSummary:
		return renderedSearchMatchLineOffset(block, query, width)
	default:
		return approximateSearchMatchInnerOffset(block, query, width), true
	}
}

func FindMatches(blocks []*Block, query string) []MatchPosition {
	return findMatchesAtWidth(blocks, query, 80)
}

// FindMatchesAtWidth performs FindMatches but uses the given width for accurate
// line offset calculation. Use this when the viewport width is known.
func FindMatchesAtWidth(blocks []*Block, query string, width int) []MatchPosition {
	if width <= 0 {
		width = 80
	}
	return findMatchesAtWidth(blocks, query, width)
}

func findMatchesAtWidth(blocks []*Block, query string, width int) []MatchPosition {
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
		blockLineCount := inspect.LineCount(width)
		searchableTextLower := inspect.searchableTextLower()
		candidate := strings.Contains(searchableTextLower, lowerQuery)
		if !candidate && inspect.Type == BlockAssistant {
			candidate = assistantMarkdownMayContainQuery(inspect.Content, lowerQuery)
		}
		if candidate && blockVisibleForSearch(inspect, width) && !searchDiagnosticArtifactExcluded(inspect.Type, searchableTextLower) {
			innerOffset, visible := visibleSearchMatchInnerOffset(inspect, query, width)
			if visible {
				matches = append(matches, MatchPosition{
					BlockIndex:  i,
					BlockID:     inspect.ID,
					LineOffset:  lineOffset,
					InnerOffset: innerOffset,
					Query:       query,
				})
			}
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
