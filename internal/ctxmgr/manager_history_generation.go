package ctxmgr

import "github.com/keakon/chord/internal/message"

// HistoryGeneration changes only when durable history is rewritten.
func (m *Manager) HistoryGeneration() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.historyGeneration
}

// CompactionWindowNumber reads the durable window carried by the current
// checkpoint, not the generation of an in-flight rewrite or a restored view.
func (m *Manager) CompactionWindowNumber() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, msg := range m.messages {
		if msg.IsCompactionSummary {
			return msg.CompactionWindowNumber
		}
	}
	return 0
}

// SnapshotWithGeneration reads the history and its generation atomically.
func (m *Manager) SnapshotWithGeneration() ([]message.Message, uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotLocked(), m.historyGeneration
}
