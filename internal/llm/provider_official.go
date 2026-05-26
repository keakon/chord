package llm

import (
	"strings"

	"github.com/keakon/chord/internal/config"
)

func providerUsesOfficialAPI(p *ProviderConfig) bool {
	if p == nil {
		return false
	}
	if p.officialAPI != nil {
		return *p.officialAPI
	}
	return strings.EqualFold(strings.TrimSpace(p.preset), config.ProviderPresetCodex)
}
