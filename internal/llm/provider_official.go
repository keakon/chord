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
	preset := strings.TrimSpace(p.preset)
	return strings.EqualFold(preset, config.ProviderPresetCodex) || strings.EqualFold(preset, config.ProviderPresetAzure)
}
