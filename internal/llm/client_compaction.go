package llm

import (
	"strings"

	"github.com/keakon/chord/internal/config"
)

// CompactionBudgetForModelRef returns the model's fixed compaction baseline.
// Request output caps do not change this budget.
func (c *Client) CompactionBudgetForModelRef(ref string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ref = strings.TrimSpace(ref)
	if ref != "" {
		ref, _ = config.ParseModelRef(ref)
	}
	if c.provider != nil && (ref == "" || ref == providerModelRef(c.provider, c.modelID)) {
		if model, ok := c.provider.GetModel(c.modelID); ok {
			return model.Limit.CompactionBudget()
		}
	}
	for _, fallback := range c.fallbackModels {
		if fallback.ProviderConfig != nil && ref == providerModelRef(fallback.ProviderConfig, fallback.ModelID) {
			return fallback.CompactionBudget()
		}
	}
	return 0
}

// CompactionBudget resolves facts for an in-flight fallback without borrowing
// the primary model's output capacity or its derived request-input budget.
func (m FallbackModel) CompactionBudget() int {
	if m.ProviderConfig != nil {
		if model, ok := m.ProviderConfig.GetModel(m.ModelID); ok {
			if budget := model.Limit.CompactionBudget(); budget > 0 {
				return budget
			}
		}
	}
	if m.InputLimit > 0 && !m.DeriveInputLimit {
		return m.InputLimit
	}
	return m.ContextLimit
}
