package tui

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
)

// overlayListSelectorState is a reusable building block for modal selector
// dialogs that render an OverlayList inside a standard RenderOverlay frame.
//
// It centralizes:
//   - overlay wrapper render caching (width/height/theme/maxVisible/list version)
//   - list base-row calculation for mouse hit-testing (incl. scroll windows)
//
// Usage pattern:
//   - Keep the OverlayList itself as the source of truth for items/cursor.
//   - Whenever items change, call list.SetItems(...) yourself.
//   - Whenever cursor changes, call list.CursorUp/Down/SetCursor(...).
//   - In Render(), provide an extraKey that captures non-list render inputs
//     (e.g. title, prefix text, plan path). If extraKey changes, updateList is
//     invoked to allow rebuilding list items/cursor.
//
// This avoids the common pitfall where a selector re-builds list items on every
// cursor move, which defeats OverlayList's internal render cache.
type overlayListSelectorState struct {
	list *OverlayList

	// listBaseRow is the number of rows from the overlay body start (after the top
	// border), to the first visible list row.
	//
	// It matches overlayItemIndexAt's contentBaseRow coordinate system.
	listBaseRow int

	renderCacheWidth      int
	renderCacheHeight     int
	renderCacheMaxVisible int
	renderCacheTheme      string
	renderCacheListVer    uint64
	renderCacheExtraKey   string
	renderCacheText       string
}

func (s *overlayListSelectorState) ensureList(maxVisible int) {
	if s.list == nil {
		s.list = NewOverlayList(nil, maxVisible)
		return
	}
	s.list.SetMaxVisible(maxVisible)
}

func (s *overlayListSelectorState) List() *OverlayList { return s.list }

// Render builds and caches the selector dialog.
//
//   - prefix is optional already-rendered text displayed above the list.
//   - gapBlankLines inserts N blank lines between prefix and list.
//   - extraKey must include any state that affects prefix/overlay content outside
//     listVersion.
//   - updateList is called only when extraKey changes (or cache empty), so callers
//     can rebuild list items/cursor without doing it on every cursor move.
func (s *overlayListSelectorState) Render(
	m *Model,
	overlayCfg OverlayConfig,
	prefix string,
	gapBlankLines int,
	maxVisible int,
	extraKey string,
	updateList func(list *OverlayList),
	area image.Rectangle,
) string {
	if m == nil {
		return ""
	}

	s.ensureList(maxVisible)
	if s.list == nil {
		return ""
	}

	listVersion := s.list.RenderVersion()
	if s.renderCacheText != "" &&
		s.renderCacheWidth == m.width &&
		s.renderCacheHeight == m.height &&
		s.renderCacheMaxVisible == maxVisible &&
		s.renderCacheTheme == m.theme.Name &&
		s.renderCacheListVer == listVersion &&
		s.renderCacheExtraKey == extraKey {
		return s.renderCacheText
	}

	// If extraKey changed, allow the caller to update items/cursor.
	if updateList != nil && (s.renderCacheText == "" || s.renderCacheExtraKey != extraKey) {
		updateList(s.list)
		listVersion = s.list.RenderVersion()
	}

	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	contentWidth := overlayCfg.MaxWidth - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	prefix = strings.TrimSuffix(prefix, "\n")
	prefixLines := 0
	contentParts := make([]string, 0, 3)
	if prefix != "" {
		contentParts = append(contentParts, prefix)
		prefixLines = 1 + strings.Count(prefix, "\n")

		// Always move the list to the next line.
		sepNewlines := 1 + gapBlankLines
		if sepNewlines < 1 {
			sepNewlines = 1
		}
		contentParts = append(contentParts, strings.Repeat("\n", sepNewlines))
	}
	contentParts = append(contentParts, s.list.Render(contentWidth))
	content := strings.Join(contentParts, "")

	s.listBaseRow = 0
	if strings.TrimSpace(overlayCfg.Title) != "" {
		s.listBaseRow += 2 // title + blank
	}
	// When prefix is present, the list starts after:
	//   prefixLines rows + gapBlankLines blank rows.
	// (The newline that terminates the prefix line does not itself add a row.)
	if prefix != "" {
		s.listBaseRow += prefixLines + gapBlankLines
	}

	dialog, _ := RenderOverlay(overlayCfg, content, lipgloss.Height(content), area)

	s.renderCacheWidth = m.width
	s.renderCacheHeight = m.height
	s.renderCacheMaxVisible = maxVisible
	s.renderCacheTheme = m.theme.Name
	s.renderCacheListVer = listVersion
	s.renderCacheExtraKey = extraKey
	s.renderCacheText = dialog
	return dialog
}

// IndexAt maps a mouse x/y within the rendered dialog to the absolute list item index.
func (s *overlayListSelectorState) IndexAt(m *Model, dialog string, x, y int) (int, bool) {
	if m == nil || s.list == nil || s.list.Len() == 0 {
		return 0, false
	}
	dialogRect := m.overlayRect(dialog)
	if x < dialogRect.Min.X || x >= dialogRect.Max.X || y < dialogRect.Min.Y || y >= dialogRect.Max.Y {
		return 0, false
	}
	start, end := s.list.WindowRange()
	idx, ok := overlayItemIndexAt(dialogRect, y, s.listBaseRow, start, end-start)
	if !ok {
		return 0, false
	}
	if idx < 0 || idx >= s.list.Len() {
		return 0, false
	}
	return idx, true
}
