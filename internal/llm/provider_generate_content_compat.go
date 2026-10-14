package llm

import "github.com/keakon/chord/internal/config"

// GenerateContentCompat returns an independent snapshot for the configured target.
func (p *ProviderConfig) GenerateContentCompat(modelID string) *config.GenerateContentCompatConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	value, ok := p.generateContentCompat[modelID]
	if !ok {
		value = p.defaultGenerateContentCompat
	}
	return config.ResolveGenerateContentCompat(&config.ProviderCompatConfig{GenerateContent: value}, config.ModelConfig{})
}

func automaticGeminiContinuation(compat *config.GenerateContentCompatConfig) bool {
	return compat != nil && compat.AutomaticContinuation != nil && *compat.AutomaticContinuation
}

func constrainGenerateContentOutputBudget(maxTokens int, compat *config.GenerateContentCompatConfig) int {
	if !automaticGeminiContinuation(compat) && compat != nil && compat.SingleRequestOutputLimit != nil && *compat.SingleRequestOutputLimit > 0 {
		if maxTokens <= 0 {
			return *compat.SingleRequestOutputLimit
		}
		return min(maxTokens, *compat.SingleRequestOutputLimit)
	}
	return maxTokens
}
