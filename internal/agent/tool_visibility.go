package agent

import (
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	toolpkg "github.com/keakon/chord/internal/tools"
)

func visibleLLMTools(registry *toolpkg.Registry, ruleset permission.Ruleset, keepInternal func(string) bool) []toolpkg.Tool {
	if registry == nil {
		return nil
	}
	allTools := registry.ListTools()
	if len(allTools) == 0 {
		return nil
	}
	if len(ruleset) == 0 {
		return allTools
	}

	filtered := make([]toolpkg.Tool, 0, len(allTools))
	spawnDisabled := ruleset.IsDisabled("Spawn")
	for _, tool := range allTools {
		name := tool.Name()
		if (name == "SpawnStop" || name == "SpawnStatus") && spawnDisabled {
			continue
		}
		if controlled, ok := tool.(toolpkg.RulesetAwareVisibilityTool); ok && !controlled.VisibleWithRuleset(ruleset) {
			continue
		}
		if !keepInternal(name) && ruleset.IsDisabled(name) {
			continue
		}
		if available, ok := tool.(toolpkg.AvailableTool); ok && !available.IsAvailable() {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func filterVisibleTools(tools []toolpkg.Tool, deny func(string) bool) []toolpkg.Tool {
	if len(tools) == 0 || deny == nil {
		return tools
	}
	filtered := make([]toolpkg.Tool, 0, len(tools))
	for _, tool := range tools {
		if deny(tool.Name()) {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func isMainAgentReservedTool(toolName string) bool {
	switch toolName {
	case "Complete":
		return true
	default:
		return false
	}
}

func (a *MainAgent) mainVisibleLLMTools() []toolpkg.Tool {
	if a == nil {
		return nil
	}
	visible := visibleLLMTools(a.tools, a.effectiveRuleset(), isInternalControlTool)
	return filterVisibleTools(visible, isMainAgentReservedTool)
}

func toolNamesFromVisibleTools(visibleTools []toolpkg.Tool) map[string]struct{} {
	visible := make(map[string]struct{}, len(visibleTools))
	for _, tool := range visibleTools {
		visible[tool.Name()] = struct{}{}
	}
	return visible
}

func llmToolDefinitionsFromVisibleTools(visibleTools []toolpkg.Tool) []message.ToolDefinition {
	visibleNames := toolNamesFromVisibleTools(visibleTools)
	defs := make([]message.ToolDefinition, len(visibleTools))
	for i, tool := range visibleTools {
		description := tool.Description()
		if descriptive, ok := tool.(toolpkg.DescriptiveTool); ok {
			description = descriptive.DescriptionForTools(visibleNames)
		}
		defs[i] = message.ToolDefinition{
			Name:        tool.Name(),
			Description: description,
			InputSchema: tool.Parameters(),
		}
	}
	return defs
}
