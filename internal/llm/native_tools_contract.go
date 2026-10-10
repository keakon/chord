package llm

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcatalog"
	"github.com/keakon/chord/internal/toolname"
)

// nativeToolRequest can only be constructed by a supported contract adapter.
// An arbitrary hosted declaration is not evidence of native replay support.
type nativeToolRequest struct {
	authorization    message.NativeToolAuthorization
	additionalTools  []string
	applyDeclaration func([]byte) ([]byte, error)
	formatContent    func(*message.NativeToolHistory, string) string
}

func resolveNativeToolRequest(provider *ProviderConfig, model string, tuning RequestTuning, policy *NativeToolPolicy) (*nativeToolRequest, error) {
	if policy == nil || policy.Permitted == nil || !nativeToolChoiceAllowed(provider.Type(), tuning) {
		return nil, nil
	}
	modelCfg, _ := provider.GetModel(model)
	if cfg := modelCfg.NativeImageGeneration; cfg != nil && cfg.Preauthorized && !policy.DisableImage && policy.Permitted(toolname.GenerateImage) {
		image, err := nativeImageRequest(cfg, provider)
		if err != nil {
			return nil, err
		}
		if searchCfg := modelCfg.NativeWebSearch; searchCfg != nil && searchCfg.Preauthorized && policy.Permitted(toolname.WebSearch) {
			search, err := nativeWebSearchRequest(searchCfg, provider)
			if err != nil {
				return nil, err
			}
			if err := validateNativeToolBinding(provider, model, search.authorization, nil); err != nil {
				return nil, err
			}
			declaration := image.applyDeclaration
			image.formatContent = search.formatContent
			image.applyDeclaration = func(raw []byte) ([]byte, error) {
				withSearch, err := search.applyDeclaration(raw)
				if err != nil {
					return nil, err
				}
				combined, err := declaration(withSearch)
				if err != nil {
					return nil, err
				}
				// Responses exposes one total limit for all server tools. The
				// smaller authorization bounds each tool even when combined.
				var body map[string]json.RawMessage
				if err := json.Unmarshal(combined, &body); err != nil {
					return nil, err
				}
				imageMax := cfg.MaxUses
				if imageMax == 0 {
					imageMax = 1
				}
				searchMax := searchCfg.MaxUses
				if searchMax == 0 {
					searchMax = 8
				}
				body["max_tool_calls"], _ = json.Marshal(min(imageMax, searchMax))
				return json.Marshal(body)
			}
			image.additionalTools = []string{toolname.WebSearch}
			image.authorization.Constraints, err = json.Marshal(struct{ Image, Search json.RawMessage }{image.authorization.Constraints, search.authorization.Constraints})
			if err != nil {
				return nil, err
			}
		}
		return image, nil
	}
	if cfg := modelCfg.NativeWebSearch; cfg != nil && cfg.Preauthorized && policy.Permitted(toolname.WebSearch) {
		return nativeWebSearchRequest(cfg, provider)
	}
	return nil, nil
}

func addNativeToolDeclaration(raw []byte, request *nativeToolRequest) ([]byte, error) {
	if request == nil {
		return raw, nil
	}
	return request.applyDeclaration(raw)
}

// Capability evidence constrains an authorized request; it never grants access.
func validateNativeToolBinding(provider *ProviderConfig, model string, authorization message.NativeToolAuthorization, defs []message.ToolDefinition) error {
	endpoint, ok := modelcatalog.EndpointContract(provider.Preset())
	if !ok || endpoint.RequestURL != provider.APIURL() || endpoint.Protocol != provider.Type() {
		return nil
	}
	binding, ok := modelcatalog.LookupBinding(provider.Preset(), model)
	if !ok {
		return nil
	}
	capability, exists := binding.ServerTools[authorization.Tool]
	if !exists {
		return nil
	}
	clientTools := slices.ContainsFunc(defs, func(def message.ToolDefinition) bool { return def.Name != authorization.Tool })
	if capability.State == modelcatalog.ServerToolUnsupported || (capability.State == modelcatalog.ServerToolSupported && (capability.Contract != authorization.Contract || (clientTools && !capability.ClientTools))) {
		return fmt.Errorf("native tool %s conflicts with the verified endpoint binding", authorization.Tool)
	}
	return nil
}
