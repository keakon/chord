package ctxmgr

import "github.com/keakon/chord/internal/message"

// HistoryGeneration changes only when durable history is rewritten.
func (m *Manager) HistoryGeneration() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.historyGeneration
}

// SnapshotWithGeneration reads the history and its generation atomically.
func (m *Manager) SnapshotWithGeneration() ([]message.Message, uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotLocked(), m.historyGeneration
}
