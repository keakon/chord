package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func hasDeferredTools(registry *tools.Registry) bool {
	if registry == nil {
		return false
	}
	return slices.ContainsFunc(registry.ToolsSnapshot(), tools.IsDeferredTool)
}

func (a *MainAgent) HasDeferredTools() bool { return a != nil && hasDeferredTools(a.tools) }
func (s *SubAgent) HasDeferredTools() bool  { return s != nil && hasDeferredTools(s.tools) }

// Filter only deferred candidates so availability checks cannot recurse through
// tool_search. Use the same permission and availability rules as declarations.
func hasDiscoverableTools(registry *tools.Registry, rules permission.Ruleset) bool {
	if registry == nil {
		return false
	}
	var candidates []tools.Tool
	for _, tool := range registry.ToolsSnapshot() {
		if tools.IsDeferredTool(tool) {
			candidates = append(candidates, tool)
		}
	}
	return len(filterLLMToolVisibility(candidates, rules, func(string) bool { return false }, toolPermissionContext{})) > 0
}

func (a *MainAgent) HasDiscoverableTools() bool {
	return a != nil && hasDiscoverableTools(a.tools, a.effectiveRuleset())
}
func (s *SubAgent) HasDiscoverableTools() bool {
	return s != nil && hasDiscoverableTools(s.tools, s.currentRuleset())
}

func toolSurfaceTokens(visible []tools.Tool) int {
	return llm.EstimateRequestInputTokens("", nil, llmToolDefinitionsFromVisibleTools(visible))
}

// projectDiscoveredTools keeps eager definitions and all successfully loaded
// definitions still present in this agent's canonical history. Context pressure
// is handled by compaction, never by evicting schemas from the request prefix.
func projectDiscoveredTools(visible []tools.Tool, names []string) []tools.Tool {
	byName := make(map[string]tools.Tool)
	out := make([]tools.Tool, 0, len(visible))
	for _, tool := range visible {
		if tools.IsDeferredTool(tool) {
			byName[tool.Name()] = tool
		} else {
			out = append(out, tool)
		}
	}
	for _, name := range names {
		if tool, ok := byName[name]; ok {
			delete(byName, name)
			out = append(out, tool)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func (a *MainAgent) SearchTools(ctx context.Context, query string, names []string) (message.ToolDiscoveryResult, error) {
	return searchDeferredTools(ctx, a.tools, a.mainVisibleCatalogTools(), a.effectiveRuleset(), query, names)
}

func (s *SubAgent) SearchTools(ctx context.Context, query string, names []string) (message.ToolDiscoveryResult, error) {
	client, modelName := s.llmSnapshot()
	return searchDeferredTools(ctx, s.tools, s.visibleCatalogToolsForModel(modelName, client), s.currentRuleset(), query, names)
}

func searchDeferredTools(ctx context.Context, registry *tools.Registry, visible []tools.Tool, ruleset permission.Ruleset, query string, names []string) (message.ToolDiscoveryResult, error) {
	result := message.ToolDiscoveryResult{Tools: []message.ToolDiscoveryEntry{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if registry == nil {
		return result, fmt.Errorf("tool catalog is unavailable")
	}
	available := make(map[string]tools.Tool)
	for _, tool := range visible {
		if tools.IsDeferredTool(tool) {
			available[tool.Name()] = tool
		}
	}
	if len(names) == 0 {
		type match struct {
			name  string
			score int
		}
		var matches []match
		terms := strings.Fields(strings.ToLower(query))
		for name, tool := range available {
			score := 0
			for _, term := range terms {
				if strings.Contains(strings.ToLower(name), term) {
					score += 3
				}
				if strings.Contains(strings.ToLower(tool.Description()), term) {
					score++
				}
			}
			if score > 0 {
				matches = append(matches, match{name, score})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].score != matches[j].score {
				return matches[i].score > matches[j].score
			}
			return matches[i].name < matches[j].name
		})
		for _, m := range matches[:min(len(matches), tools.DiscoveryMaxResults)] {
			names = append(names, m.name)
		}
		if len(names) == 0 {
			result.Tools = append(result.Tools, message.ToolDiscoveryEntry{Status: tools.DiscoveryNotFound})
			return result, nil
		}
	}
	remaining := tools.DiscoveryMaxResultTokens
	remainingBytes := tools.DiscoveryMaxResultBytes
	seen := make(map[string]bool)
	for _, name := range names {
		name = tools.NormalizeName(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		entry := message.ToolDiscoveryEntry{Name: name, Status: tools.DiscoveryNotFound}
		tool, exists := registry.Get(name)
		switch {
		case ruleset.IsDisabled(name):
			entry.Status = tools.DiscoveryDenied
		case !exists || !tools.IsDeferredTool(tool):
		case available[name] == nil:
			entry.Status = tools.DiscoveryUnavailable
			if status, ok := tool.(interface{ DiscoveryStatus() string }); ok {
				entry.Status = status.DiscoveryStatus()
			}
			if entry.Status == message.ToolDiscoveryLoaded {
				entry.Status = tools.DiscoveryDenied
			}
		default:
			cost := toolSurfaceTokens([]tools.Tool{tool})
			def := llmToolDefinitionsFromVisibleTools([]tools.Tool{tool})[0]
			raw, err := json.Marshal(def)
			if err != nil {
				entry.Status = tools.DiscoveryUnavailable
				break
			}
			if cost > remaining || len(raw) > remainingBytes {
				entry.Status = tools.DiscoveryBudgetExceeded
			} else {
				remaining -= cost
				remainingBytes -= len(raw)
				entry.Status, entry.Definition = message.ToolDiscoveryLoaded, &def
			}
		}
		result.Tools = append(result.Tools, entry)
	}
	return result, ctx.Err()
}
