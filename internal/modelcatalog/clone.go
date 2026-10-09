package modelcatalog

import (
	"maps"
	"slices"
)

// The effective catalog is published through an atomic pointer and is shared
// by all callers. Public accessors therefore return deep copies so callers
// cannot mutate the snapshot that other goroutines resolve against.

func cloneCatalogSource(in *CatalogSource) *CatalogSource {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneConnection(in *Connection) *Connection {
	if in == nil {
		return nil
	}
	out := *in
	out.Sources = slices.Clone(in.Sources)
	return &out
}

func cloneCost(in *Cost) *Cost {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, nested := range value {
			out[key] = cloneJSONValue(nested)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, nested := range value {
			out[i] = cloneJSONValue(nested)
		}
		return out
	default:
		return value
	}
}

func cloneCompactionProfile(in *CompactionProfile) *CompactionProfile {
	if in == nil {
		return nil
	}
	out := *in
	if in.Threshold != nil {
		out.Threshold = new(*in.Threshold)
	}
	if in.Reminder != nil {
		out.Reminder = new(*in.Reminder)
	}
	return &out
}

func cloneConfigProfile(in *ConfigProfile) *ConfigProfile {
	if in == nil {
		return nil
	}
	out := *in
	out.Model = cloneJSONMap(in.Model)
	out.Compat = cloneJSONMap(in.Compat)
	out.Compaction = cloneCompactionProfile(in.Compaction)
	out.Sources = slices.Clone(in.Sources)
	return &out
}

func cloneModelFacts(in ModelFacts) ModelFacts {
	out := in
	out.CodingSources = slices.Clone(in.CodingSources)
	out.Connection = cloneConnection(in.Connection)
	out.InputModalities = slices.Clone(in.InputModalities)
	out.ReasoningOptions = slices.Clone(in.ReasoningOptions)
	out.Cost = cloneCost(in.Cost)
	out.Sources = slices.Clone(in.Sources)
	out.Profile = cloneConfigProfile(in.Profile)
	return out
}

func cloneEndpoint(in Endpoint) Endpoint {
	out := in
	out.Docs = slices.Clone(in.Docs)
	return out
}

func cloneLimitOverride(in *LimitOverride) *LimitOverride {
	if in == nil {
		return nil
	}
	out := *in
	if in.Context != nil {
		out.Context = new(*in.Context)
	}
	if in.Input != nil {
		out.Input = new(*in.Input)
	}
	if in.Output != nil {
		out.Output = new(*in.Output)
	}
	return &out
}

func cloneResponsesContract(in *ResponsesContract) *ResponsesContract {
	if in == nil {
		return nil
	}
	out := *in
	if in.SendStore != nil {
		out.SendStore = new(*in.SendStore)
	}
	if in.SendReasoningInclude != nil {
		out.SendReasoningInclude = new(*in.SendReasoningInclude)
	}
	if in.SendToolChoice != nil {
		out.SendToolChoice = new(*in.SendToolChoice)
	}
	if in.SendPromptCacheKey != nil {
		out.SendPromptCacheKey = new(*in.SendPromptCacheKey)
	}
	if in.SendMaxOutputTokens != nil {
		out.SendMaxOutputTokens = new(*in.SendMaxOutputTokens)
	}
	if in.SendParallelToolCalls != nil {
		out.SendParallelToolCalls = new(*in.SendParallelToolCalls)
	}
	return &out
}

func cloneBinding(in Binding) Binding {
	out := in
	out.ServerTools = cloneServerTools(in.ServerTools)
	out.Variants = maps.Clone(in.Variants)
	out.Responses = cloneResponsesContract(in.Responses)
	out.Limit = cloneLimitOverride(in.Limit)
	out.InputModalities = slices.Clone(in.InputModalities)
	out.Profile = cloneConfigProfile(in.Profile)
	return out
}

func cloneCandidate(in Candidate) Candidate {
	out := in
	out.CodingSources = slices.Clone(in.CodingSources)
	out.InputModalities = slices.Clone(in.InputModalities)
	out.Sources = slices.Clone(in.Sources)
	return out
}
