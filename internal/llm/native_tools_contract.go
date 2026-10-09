package llm

import (
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
	applyDeclaration func([]byte) ([]byte, error)
	formatContent    func(*message.NativeToolHistory, string) string
}

func resolveNativeToolRequest(provider *ProviderConfig, model string, tuning RequestTuning, policy *NativeToolPolicy) (*nativeToolRequest, error) {
	if policy == nil || policy.Permitted == nil || !nativeToolChoiceAllowed(provider.Type(), tuning) {
		return nil, nil
	}
	modelCfg, _ := provider.GetModel(model)
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
