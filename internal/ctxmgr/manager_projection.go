package ctxmgr

import "github.com/keakon/chord/internal/message"

type usageProjection struct {
	bytes      int
	images     int
	generation uint64
}

// AppendResponse appends the settled response and excludes its payload from
// growth only when provider usage already charged its output. A frozen reading
// was taken before settlement, so that response remains unobserved growth.
func (m *Manager) AppendResponse(msg message.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	beforeBytes, beforeImages := m.estimatorContextBytesLocked(), m.imageEstimateTokens
	m.appendLocked(msg)
	if m.observedValid && !m.observationStale && m.projection.generation == m.historyGeneration &&
		msg.Usage != nil {
		if _, known := message.ContextPromptTokens(*msg.Usage); known {
			// Advance by this response alone: an intervening append was not
			// part of the request measured by the provider.
			m.projection.bytes += m.estimatorContextBytesLocked() - beforeBytes
			m.projection.images += m.imageEstimateTokens - beforeImages
		}
	}
}

func (m *Manager) projectedContextTokensLocked() int {
	if m.usageStateLocked() == ContextUsageUnknown || m.projection.generation != m.historyGeneration {
		return 0
	}
	growthBytes := max(m.estimatorContextBytesLocked()-m.projection.bytes, 0)
	growthTokens := growthBytes / 3
	if m.calibratedRatioCache > 0 {
		growthTokens = int(float64(growthBytes) * m.calibratedRatioCache)
	}
	return m.observedEffectiveLocked() + growthTokens + max(m.imageEstimateTokens-m.projection.images, 0)
}
