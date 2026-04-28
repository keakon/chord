package tui

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var exportInfoRE = regexp.MustCompile(`^Session exported \(([^)]+)\) to (.+)$`)

func (m *Model) hasStreamingViewportBlock() bool {
	if m == nil {
		return false
	}
	return (m.currentThinkingBlock != nil && m.thinkingBlockAppended) || (m.currentAssistantBlock != nil && m.assistantBlockAppended)
}

func (m *Model) appendLocalStatusCard(title, content string) {
	if m == nil || m.viewport == nil {
		return
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	card := localStatusCard{title: strings.TrimSpace(title), content: content}
	if m.hasStreamingViewportBlock() {
		m.pendingLocalStatusCards = append(m.pendingLocalStatusCards, card)
		return
	}
	m.appendLocalStatusCardNow(card)
}

func (m *Model) appendLocalStatusCardNow(card localStatusCard) {
	if m == nil || m.viewport == nil {
		return
	}
	if strings.TrimSpace(card.content) == "" {
		return
	}
	wasNearBottom := m.viewport.sticky || m.viewport.TotalLines()-m.viewport.height-m.viewport.offset <= 1
	block := &Block{ID: m.nextBlockID, Type: BlockStatus, StatusTitle: strings.TrimSpace(card.title), Content: card.content}
	m.nextBlockID++
	m.appendViewportBlock(block)
	m.markBlockSettled(block)
	if wasNearBottom {
		m.viewport.ScrollToBottom()
	}
}

func (m *Model) flushPendingLocalStatusCards() {
	if m == nil || m.viewport == nil || len(m.pendingLocalStatusCards) == 0 || m.hasStreamingViewportBlock() {
		return
	}
	cards := m.pendingLocalStatusCards
	m.pendingLocalStatusCards = nil
	for _, card := range cards {
		m.appendLocalStatusCardNow(card)
	}
}

func formatDiagnosticsStatusCard(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || clean == "" {
		clean = strings.TrimSpace(path)
	}
	return fmt.Sprintf("Diagnostics bundle exported to %s\n\nBefore sharing it, please inspect the bundle and remove any sensitive content if needed.", clean)
}

func formatExportStatusCard(message string) (title, content string, ok bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", "", false
	}
	matches := exportInfoRE.FindStringSubmatch(message)
	if len(matches) != 3 {
		return "", "", false
	}
	format := strings.TrimSpace(matches[1])
	path := filepath.Clean(strings.TrimSpace(matches[2]))
	if path == "." {
		path = strings.TrimSpace(matches[2])
	}
	if format == "" {
		format = "Session"
	}
	return "EXPORT", fmt.Sprintf("Session export (%s) written to %s", format, path), true
}
