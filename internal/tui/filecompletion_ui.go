package tui

import (
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tui/composer"
	"github.com/keakon/chord/internal/tui/filecompletion"
	"github.com/keakon/chord/internal/tui/overlaylist"
)

// openAtMentionAtCursor uses the position after insertion, including selection
// replacements, so completion and ordinary typing share the same trigger rule.
func (m *Model) openAtMentionAtCursor() tea.Cmd {
	line, col := m.input.Line(), m.input.Column()
	row, _ := inputLineAt(m.input.Value(), line)
	runes := []rune(row)
	if col <= 0 || col > len(runes) || runes[col-1] != '@' || !canTriggerAtMention(row, col-1) {
		m.closeAtMention()
		return nil
	}
	m.atMentionOpen = true
	m.atMentionLine, m.atMentionTriggerCol = line, col
	m.atMentionQuery = ""
	return m.syncAtMentionQuery()
}

func (m *Model) syncAtMentionIfOpen() tea.Cmd {
	if !m.atMentionOpen {
		return nil
	}
	return m.syncAtMentionQuery()
}

func (m *Model) syncAtMentionQuery() tea.Cmd {
	if !m.atMentionOpen {
		return nil
	}
	line := m.input.Line()
	col := m.input.Column()
	if line != m.atMentionLine || col < m.atMentionTriggerCol {
		m.closeAtMention()
		return nil
	}
	token, ok := inputTokenAt(m.input.Value(), line, m.atMentionTriggerCol, col)
	if !ok {
		m.closeAtMention()
		return nil
	}
	m.atMentionQuery = token
	m.refreshAtMentionList()
	return m.startAtMentionFileLoadIfStale(time.Now())
}

func (m *Model) refreshAtMentionList() {
	var matches []filecompletion.Option
	if exact, ok := filecompletion.ExactPathMatch(m.atMentionQuery, m.workingDir); ok {
		matches = []filecompletion.Option{exact}
	} else if filecompletion.ShouldUsePathMatches(m.atMentionFiles, m.atMentionQuery) {
		matches = filecompletion.PathMatches(m.atMentionQuery, m.workingDir)
	} else {
		rootMatches := filecompletion.RootPrefixMatches(m.atMentionQuery, m.workingDir)
		if !m.atMentionLoaded || len(m.atMentionFiles) == 0 {
			matches = rootMatches
		} else {
			if exact, ok := filecompletion.ExactIndexedMatch(m.atMentionFiles, m.atMentionQuery); ok {
				matches = []filecompletion.Option{exact}
			} else {
				matches = filecompletion.FuzzyMatches(m.atMentionFiles, m.atMentionFilesLower, m.atMentionQuery, &m.atMentionNarrow)
			}
			matches = filecompletion.MergeOptions(matches, filecompletion.OptionsMissingFromIndex(rootMatches, m.atMentionFiles), m.atMentionQuery)
		}
	}

	if len(matches) == 0 {
		m.atMentionList = nil
		return
	}
	matches = m.filterAtMentionOptionsByInputSupport(matches)
	if len(matches) == 0 {
		m.atMentionList = nil
		return
	}
	items := make([]overlaylist.Item, len(matches))
	for i, match := range matches {
		items[i] = overlaylist.Item{Label: match.Path, Value: match}
	}
	if m.atMentionList == nil {
		m.atMentionList = overlaylist.New(items, 10)
	} else {
		m.atMentionList.SetItems(items)
	}
}

func (m *Model) closeAtMention() {
	m.atMentionOpen = false
	m.atMentionQuery = ""
	m.atMentionTriggerCol = 0
	m.atMentionList = nil
}

func (m *Model) insertAtMentionSelection() tea.Cmd {
	if m.atMentionList == nil {
		return nil
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		return nil
	}
	selection, _ := item.Value.(filecompletion.Option)
	if selection.Path == "" {
		return nil
	}
	value := m.input.Value()
	line := m.input.Line()
	lines := strings.Split(value, "\n")
	if line < 0 || line >= len(lines) {
		return nil
	}
	rowRunes := []rune(lines[line])
	cursorCol := min(m.input.Column(), len(rowRunes))
	if m.atMentionTriggerCol < 0 || m.atMentionTriggerCol > len(rowRunes) {
		return nil
	}
	replaceEndCol := cursorCol
	lineSuffixRuneLen := 0
	if !selection.IsDir {
		query := string(rowRunes[m.atMentionTriggerCol:cursorCol])
		if _, lineSuffix, ok := filecompletion.SplitLineRangeSuffix(query); ok {
			normalized := filecompletion.NormalizeQuery(query)
			if !filecompletion.PathExists(normalized, m.workingDir) {
				lineSuffixRuneLen = len([]rune(lineSuffix))
				replaceEndCol -= lineSuffixRuneLen
			}
		}
	}
	prefix := string(rowRunes[:m.atMentionTriggerCol])
	suffix := string(rowRunes[replaceEndCol:])
	// Replace only the query segment after '@' so the '@' stays in place.
	replaceText := selection.Path
	if !selection.IsDir && lineSuffixRuneLen == 0 {
		replaceText += " "
	}
	cursorTarget := m.atMentionTriggerCol + len([]rune(replaceText)) + lineSuffixRuneLen
	lines[line] = prefix + replaceText + suffix

	// Insert without losing inline large-paste placeholders (which carry the raw
	// pasted content for submission). Using Input.SetValue would clear inlinePastes
	// and cause the submitted message to contain only the placeholder text.
	start := composer.RuneOffsetFromRowCol(value, line, m.atMentionTriggerCol)
	end := composer.RuneOffsetFromRowCol(value, line, replaceEndCol)
	if !m.input.ReplaceRuneRangePreserveInlinePastes(start, end, replaceText) {
		// Fallback: keep existing behaviour if we cannot safely update ranges.
		m.input.SetValue(strings.Join(lines, "\n"))
	}
	m.input.SetCursorPosition(line, cursorTarget)
	if selection.IsDir {
		m.atMentionOpen = true
		m.atMentionLine = line
		return m.syncAtMentionQuery()
	}
	m.closeAtMention()
	return nil
}
