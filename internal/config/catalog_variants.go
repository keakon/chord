package config

import (
	"strings"

	"github.com/keakon/chord/internal/modelcatalog"
)

// CatalogReasoningVariants returns choices for explicit model configuration.
// A route's verified variants take precedence over model-wide effort options.
// Options transfer only to a protocol the catalog documents for this model.
func CatalogReasoningVariants(provider ProviderConfig, wireModel string, model ModelConfig) (map[string]ModelVariant, error) {
	facts, variants, _, binding, found, err := catalogModelDefaults(provider, wireModel, model)
	if err != nil || !found {
		return nil, err
	}
	result := make(map[string]ModelVariant)
	if len(variants) > 0 {
		for name, variant := range variants {
			result[name] = catalogModelVariant(variant)
		}
		return result, nil
	}
	protocol := strings.ToLower(strings.TrimSpace(provider.Type))
	if protocol == "" {
		protocol = InferProviderTypeFromAPIURL(provider.APIURL)
	}
	if binding == nil {
		binding = customCatalogBinding(protocol, facts.ID)
	}
	if binding != nil {
		endpoint, ok := modelcatalog.EndpointContract(binding.Endpoint)
		if !ok || protocol != "" && protocol != endpoint.Protocol {
			return nil, nil
		}
		protocol = endpoint.Protocol
	} else if facts.Connection == nil || InferProviderTypeFromAPIURL(facts.Connection.RequestURL) != protocol {
		return nil, nil
	}
	for _, effort := range facts.ReasoningOptions {
		var variant ModelVariant
		switch protocol {
		case ProviderTypeChatCompletions, ProviderTypeResponses:
			variant.Reasoning = &ReasoningConfig{Effort: effort}
		case ProviderTypeMessages:
			variant.Thinking = &ThinkingConfig{Type: ThinkingTypeAdaptive, Effort: effort}
		case ProviderTypeGenerateContent:
			variant.Thinking = &ThinkingConfig{Level: effort}
		default:
			continue
		}
		result[effort] = variant
	}
	return result, nil
}
