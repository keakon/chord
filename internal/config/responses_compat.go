package config

// ResolveResponsesCompat merges the verified binding, provider and model
// settings in increasing priority and records each explicit field's source.
// Nullable model blocks do not erase provider contracts.
func ResolveResponsesCompat(preset, wireModel string, model ModelConfig, provider *ProviderCompatConfig) (*ResponsesCompatConfig, map[string]string) {
	catalog := CatalogResponsesCompat(preset, wireModel, model)
	var providerCompat, modelCompat *ResponsesCompatConfig
	if provider != nil {
		providerCompat = provider.Responses
	}
	if model.Compat != nil {
		modelCompat = model.Compat.Responses
	}
	if catalog == nil && providerCompat == nil && modelCompat == nil {
		return nil, nil
	}
	merged := &ResponsesCompatConfig{}
	sources := make(map[string]string)
	mergeResponsesCompat(merged, catalog, sources, "catalog")
	mergeResponsesCompat(merged, providerCompat, sources, "provider")
	mergeResponsesCompat(merged, modelCompat, sources, "model")
	return merged, sources
}

func mergeResponsesCompat(dst, src *ResponsesCompatConfig, sources map[string]string, source string) {
	if src == nil {
		return
	}
	for _, field := range []struct {
		name  string
		dst   **bool
		value *bool
	}{
		{"send_store", &dst.SendStore, src.SendStore},
		{"send_reasoning_include", &dst.SendReasoningInclude, src.SendReasoningInclude},
		{"send_tool_choice", &dst.SendToolChoice, src.SendToolChoice},
		{"send_parallel_tool_calls", &dst.SendParallelToolCalls, src.SendParallelToolCalls},
		{"send_prompt_cache_key", &dst.SendPromptCacheKey, src.SendPromptCacheKey},
		{"send_max_output_tokens", &dst.SendMaxOutputTokens, src.SendMaxOutputTokens},
		{"mcp_additional_tools", &dst.MCPAdditionalTools, src.MCPAdditionalTools},
	} {
		if field.value != nil {
			*field.dst = new(*field.value)
			sources[field.name] = source
		}
	}
}
