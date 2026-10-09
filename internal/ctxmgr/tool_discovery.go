package ctxmgr

import "github.com/keakon/chord/internal/message"

// ToolDiscoveryNames reads the canonical discovery facts under the history
// lock. The returned strings are immutable; attachments and raw receipts do not
// need to be cloned merely to select the next request's tool declarations.
func (m *Manager) ToolDiscoveryNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return message.ToolDiscoveryHistory(m.messages)
}
