package tui

import "github.com/keakon/chord/internal/message"

const progressNoticeCardTitle = "PROGRESS REMINDER"

func newProgressNoticeBlock(id, messageIndex int, text string) *Block {
	return &Block{
		ID: id, Type: BlockStatus, StatusTitle: progressNoticeCardTitle,
		Content: message.StripSystemReminderOverlay(text), MsgIndex: messageIndex, Collapsed: true,
	}
}

func (m *Model) appendProgressNotice(messageIndex int, text string) {
	// Persistence recovery may replay a durable notice already restored into
	// the viewport. Its transcript index owns the card, not its repeated text.
	for _, block := range m.viewport.blocks {
		if block.Type == BlockStatus && block.StatusTitle == progressNoticeCardTitle && block.MsgIndex == messageIndex {
			return
		}
	}
	block := newProgressNoticeBlock(m.nextBlockID, messageIndex, text)
	m.nextBlockID++
	m.appendViewportBlock(block)
	m.markBlockSettled(block)
}
