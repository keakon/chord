package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

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
	var matches []atMentionOption
	if exact, ok := atMentionExactPathMatch(m.atMentionQuery, m.workingDir); ok {
		matches = []atMentionOption{exact}
	} else if atMentionShouldUsePathMatches(m.atMentionFiles, m.atMentionQuery) {
		matches = atMentionPathMatches(m.atMentionQuery, m.workingDir)
	} else {
		rootMatches := atMentionRootPrefixMatches(m.atMentionQuery, m.workingDir)
		if !m.atMentionLoaded || len(m.atMentionFiles) == 0 {
			matches = rootMatches
		} else {
			if exact, ok := atMentionExactIndexedMatch(m.atMentionFiles, m.atMentionQuery); ok {
				matches = []atMentionOption{exact}
			} else {
				matches = atMentionFuzzyMatches(m.atMentionFiles, m.atMentionQuery)
			}
			matches = mergeAtMentionOptions(matches, atMentionOptionsMissingFromIndex(rootMatches, m.atMentionFiles), m.atMentionQuery)
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
	items := make([]OverlayListItem, len(matches))
	for i, match := range matches {
		items[i] = OverlayListItem{Label: match.Path, Value: match}
	}
	if m.atMentionList == nil {
		m.atMentionList = NewOverlayList(items, 10)
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
	selection, _ := item.Value.(atMentionOption)
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
	prefix := string(rowRunes[:m.atMentionTriggerCol])
	suffix := string(rowRunes[cursorCol:])
	// Replace only the query segment after '@' so the '@' stays in place.
	replaceText := selection.Path
	if !selection.IsDir {
		replaceText += " "
	}
	cursorTarget := m.atMentionTriggerCol + len([]rune(replaceText))
	lines[line] = prefix + replaceText + suffix

	// Insert without losing inline large-paste placeholders (which carry the raw
	// pasted content for submission). Using Input.SetValue would clear inlinePastes
	// and cause the submitted message to contain only the placeholder text.
	start := runeOffsetFromRowCol(value, line, m.atMentionTriggerCol)
	end := runeOffsetFromRowCol(value, line, cursorCol)
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
