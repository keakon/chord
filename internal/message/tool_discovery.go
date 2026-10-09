package message

import (
	"encoding/json"
	"slices"

	"github.com/keakon/chord/internal/toolname"
)

const ToolDiscoveryLoaded = "loaded"

// ToolDiscoveryEntry preserves the schema actually loaded by a discovery call.
// Requests use current top-level schemas; historical snapshots stay immutable.
type ToolDiscoveryEntry struct {
	Name       string          `json:"name,omitempty"`
	Status     string          `json:"status"`
	Definition *ToolDefinition `json:"definition,omitempty"`
}

type ToolDiscoveryResult struct {
	Tools []ToolDiscoveryEntry `json:"tools"`
}

// Only names are needed when selecting declarations or stripping historical
// schemas. Skip decoding descriptions and arbitrarily large input schemas.
type toolDiscoveryNames struct {
	Tools []struct {
		Name       string `json:"name,omitempty"`
		Status     string `json:"status"`
		Definition *struct {
			Name string `json:"name"`
		} `json:"definition,omitempty"`
	} `json:"tools"`
}

// ToolDiscoveryHistory returns loaded names in most-recent-first order. Only
// results paired with an actual tool_search call can activate declarations.
func ToolDiscoveryHistory(messages []Message) []string {
	var names []string
	visitToolDiscovery(messages, func(_ int, result toolDiscoveryNames) {
		for _, entry := range result.Tools {
			if entry.Status == ToolDiscoveryLoaded && entry.Definition != nil && entry.Name == entry.Definition.Name {
				names = append(names, entry.Name)
			}
		}
	})
	slices.Reverse(names)
	return names
}

// ProjectToolDiscoveryHistory removes duplicate schema bodies from the request
// projection, preserving names/statuses and every byte of canonical history.
func ProjectToolDiscoveryHistory(messages []Message) []Message {
	var out []Message
	visitToolDiscovery(messages, func(index int, result toolDiscoveryNames) {
		changed := false
		for i := range result.Tools {
			changed = changed || result.Tools[i].Definition != nil
			result.Tools[i].Definition = nil
		}
		if !changed {
			return
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return
		}
		if out == nil {
			out = slices.Clone(messages)
		}
		out[index].Content = string(raw)
		out[index].ToolPayload = ""
		out[index].Parts = nil
	})
	if out == nil {
		return messages
	}
	return out
}

func visitToolDiscovery(messages []Message, visit func(int, toolDiscoveryNames)) {
	var calls map[string]bool
	for i, msg := range messages {
		if msg.Role == RoleAssistant {
			for _, call := range msg.ToolCalls {
				if call.Name == toolname.ToolSearch {
					if calls == nil {
						calls = make(map[string]bool)
					}
					calls[call.ID] = true
				} else {
					delete(calls, call.ID)
				}
			}
		}
		if msg.Role != RoleTool || msg.ToolStatus == ToolStatusError || msg.ToolStatus == ToolStatusCancelled || !calls[msg.ToolCallID] || !ToolResultSucceeded(msg.Content) {
			continue
		}
		payload := msg.Content
		if msg.ToolPayload != "" {
			payload = msg.ToolPayload
		}
		var result toolDiscoveryNames
		if json.Unmarshal([]byte(payload), &result) == nil {
			visit(i, result)
		}
	}
}
