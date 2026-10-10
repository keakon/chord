package modelcatalog

import (
	"fmt"
	"maps"
	"slices"

	"github.com/keakon/chord/internal/toolname"
)

const (
	ServerToolUnknown                          = "unknown"
	ServerToolSupported                        = "supported"
	ServerToolUnsupported                      = "unsupported"
	ServerToolContractResponsesImageGeneration = "openai.responses.image_generation"
)

// ServerToolCapability belongs to one endpoint and wire model, never to a
// portable config profile. Documentation alone cannot mark a route supported.
type ServerToolCapability struct {
	State       string   `json:"state" yaml:"state"`
	Contract    string   `json:"contract,omitempty" yaml:"contract,omitempty"`
	Evidence    string   `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	Sources     []Source `json:"sources,omitempty" yaml:"sources,omitempty"`
	ClientTools bool     `json:"client_tools,omitempty" yaml:"client_tools,omitempty"`
}

func validateServerTools(binding Binding, endpoint Endpoint) error {
	for name, capability := range binding.ServerTools {
		if name != toolname.WebSearch && name != toolname.GenerateImage {
			return fmt.Errorf("binding %s: unknown server tool %q", binding.ModelID, name)
		}
		switch capability.State {
		case ServerToolUnknown, ServerToolSupported, ServerToolUnsupported:
		default:
			return fmt.Errorf("binding %s: invalid server tool state %q", binding.ModelID, capability.State)
		}
		if capability.Evidence != "" && capability.Evidence != "documentation" && capability.Evidence != "api" {
			return fmt.Errorf("binding %s: invalid server tool evidence", binding.ModelID)
		}
		if capability.State != ServerToolUnknown && (capability.Evidence != "api" || len(capability.Sources) == 0) {
			return fmt.Errorf("binding %s: server tool support requires API evidence", binding.ModelID)
		}
		if capability.State == ServerToolSupported && capability.Contract == "" {
			return fmt.Errorf("binding %s: supported server tool requires a contract", binding.ModelID)
		}
		if capability.Contract == "openai.responses.web_search" && endpoint.Protocol != "responses" || capability.Contract == "anthropic.messages.web_search_20250305" && endpoint.Protocol != "messages" || capability.Contract == ServerToolContractResponsesImageGeneration && endpoint.Protocol != "responses" {
			return fmt.Errorf("binding %s: server tool contract does not match endpoint protocol", binding.ModelID)
		}
		if name == toolname.GenerateImage && capability.Contract != "" && capability.Contract != ServerToolContractResponsesImageGeneration {
			return fmt.Errorf("binding %s: image generation contract is not implemented", binding.ModelID)
		}
		for _, source := range capability.Sources {
			if err := validateMetadataSource(source); err != nil {
				return fmt.Errorf("binding %s server tool evidence: %w", binding.ModelID, err)
			}
		}
	}
	return nil
}

func cloneServerTools(in map[string]ServerToolCapability) map[string]ServerToolCapability {
	out := maps.Clone(in)
	for name, c := range out {
		c.Sources = slices.Clone(c.Sources)
		out[name] = c
	}
	return out
}
