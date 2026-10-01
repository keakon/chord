package main

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/config"
)

// configShowResponsesRequest keeps derived request settings outside YAML config.
type configShowResponsesRequest struct {
	Provider          string            `json:"provider" yaml:"provider"`
	Model             string            `json:"model" yaml:"model"`
	Store             bool              `json:"store" yaml:"store"`
	ParallelToolCalls bool              `json:"parallel_tool_calls" yaml:"parallel_tool_calls"`
	SendFields        map[string]bool   `json:"send_fields" yaml:"send_fields"`
	Sources           map[string]string `json:"sources" yaml:"sources"`
}

func hasConfigErrors(diagnostics []config.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == config.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

// configShowResponses explains request values separately from field emission.
func configShowResponses(cfg *config.Config, pathFilter string) []configShowResponsesRequest {
	if cfg == nil {
		return nil
	}
	var out []configShowResponsesRequest
	for name, provider := range cfg.Providers {
		if provider.Type != config.ProviderTypeResponses {
			continue
		}
		for modelName, model := range provider.Models {
			path := "providers." + name + ".models." + modelName
			if pathFilter != "" && pathFilter != path && !strings.HasPrefix(path, pathFilter+".") && !strings.HasPrefix(pathFilter, path+".") {
				continue
			}
			compat, sources := config.ResolveResponsesCompat(provider.Preset, modelName, model, provider.Compat)
			if compat == nil {
				compat = &config.ResponsesCompatConfig{}
			}
			parallel := provider.ParallelToolCalls
			if model.ParallelToolCalls != nil {
				parallel = model.ParallelToolCalls
			}
			fields := map[string]bool{
				"send_store":               responseFieldEnabled(compat.SendStore, true),
				"send_parallel_tool_calls": responseFieldEnabled(compat.SendParallelToolCalls, true),
				"send_reasoning_include":   responseFieldEnabled(compat.SendReasoningInclude, true),
				"send_tool_choice":         responseFieldEnabled(compat.SendToolChoice, true),
				"send_prompt_cache_key":    responseFieldEnabled(compat.SendPromptCacheKey, true),
				"send_max_output_tokens":   responseFieldEnabled(compat.SendMaxOutputTokens, false),
			}
			if sources == nil {
				sources = make(map[string]string)
			}
			for key := range fields {
				if sources[key] == "" {
					sources[key] = "protocol default"
				}
			}
			sources["store"], sources["parallel_tool_calls"] = "protocol default", "protocol default"
			if provider.Store != nil {
				sources["store"] = "provider"
			}
			if model.Store != nil {
				sources["store"] = "model"
			}
			if provider.ParallelToolCalls != nil {
				sources["parallel_tool_calls"] = "provider"
			}
			if model.ParallelToolCalls != nil {
				sources["parallel_tool_calls"] = "model"
			}
			out = append(out, configShowResponsesRequest{Provider: name, Model: modelName, Store: config.EffectiveStore(provider.Store, model.Store), ParallelToolCalls: responseFieldEnabled(parallel, true), SendFields: fields, Sources: sources})
		}
	}
	slices.SortFunc(out, func(a, b configShowResponsesRequest) int {
		if order := strings.Compare(a.Provider, b.Provider); order != 0 {
			return order
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out
}

func responseFieldEnabled(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}
