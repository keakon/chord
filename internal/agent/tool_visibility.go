package agent

import (
	"strings"

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

	filtered := make([]toolpkg.Tool, 0, len(allTools))
	spawnDisabled := ruleset.IsDisabled(toolpkg.NameSpawn)
	for _, tool := range allTools {
		name := toolpkg.NormalizeName(tool.Name())
		if (name == toolpkg.NameSpawnStop || name == toolpkg.NameSpawnStatus) && spawnDisabled {
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
	toolName = toolpkg.NormalizeName(toolName)
	switch toolName {
	case toolpkg.NameComplete:
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
	filtered := filterVisibleTools(visible, isMainAgentReservedTool)
	// Apply per-model edit tool selection
	return filterEditToolsByModel(filtered, a.modelName, a.effectiveRuleset())
}

// filterEditToolsByModel applies per-model edit tool selection to visible tools.
// It is the shared implementation used by both MainAgent and SubAgent to keep a
// consistent tool surface across agent types, filtering edit tools based on the
// active model and tool-wide availability.
// The logic is:
// 1. Check which edit tools are registered and not tool-wide disabled by permissions
// 2. If both are available, choose based on model preference
// 3. If only one is available, use that one regardless of model
// 4. If neither is available, remove both
func filterEditToolsByModel(tools []toolpkg.Tool, modelName string, ruleset permission.Ruleset) []toolpkg.Tool {
	// Check which edit tools are available. Path-scoped allow/ask rules still make
	// the tool usable, so visibility should only collapse when the whole tool is
	// disabled. Actual path authorization still happens at execution time.
	patchAvailable := false
	editAvailable := false
	for _, tool := range tools {
		switch toolpkg.NormalizeName(tool.Name()) {
		case toolpkg.NamePatch:
			patchAvailable = true
		case toolpkg.NameEdit:
			editAvailable = true
		}
	}
	patchAllowed := patchAvailable && !ruleset.IsDisabled(toolpkg.NamePatch)
	editAllowed := editAvailable && !ruleset.IsDisabled(toolpkg.NameEdit)

	// Determine which tool to keep
	var keepPatch bool
	if patchAllowed && editAllowed {
		// Both allowed: use model preference
		keepPatch = shouldUsePatchForModel(modelName)
	} else if patchAllowed {
		// Only patch allowed
		keepPatch = true
	} else if editAllowed {
		// Only edit allowed
		keepPatch = false
	} else {
		// Neither allowed: remove both
		filtered := make([]toolpkg.Tool, 0, len(tools))
		for _, tool := range tools {
			name := toolpkg.NormalizeName(tool.Name())
			if name != toolpkg.NamePatch && name != toolpkg.NameEdit {
				filtered = append(filtered, tool)
			}
		}
		return filtered
	}

	filtered := make([]toolpkg.Tool, 0, len(tools))
	for _, tool := range tools {
		name := toolpkg.NormalizeName(tool.Name())
		// Filter out the non-matching edit tool
		if name == toolpkg.NamePatch && !keepPatch {
			continue
		}
		if name == toolpkg.NameEdit && keepPatch {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

// shouldUsePatchForModel returns true if the model should use patch (@@-style),
// false if it should use edit (old_string/new_string).
func shouldUsePatchForModel(modelName string) bool {
	// Extract the model ID from provider/model format
	// e.g. "codex/gpt-5.5" → "gpt-5.5", "anthropic-main/claude-opus-4.7" → "claude-opus-4.7"
	modelID := modelName
	if idx := strings.LastIndex(modelName, "/"); idx >= 0 {
		modelID = modelName[idx+1:]
	}
	// Strip priority suffix if present (e.g. "@xhigh" → "")
	if idx := strings.Index(modelID, "@"); idx >= 0 {
		modelID = modelID[:idx]
	}

	modelID = strings.ToLower(modelID)
	return isOpenAIApplyPatchModel(modelID)
}

func isOpenAIApplyPatchModel(modelID string) bool {
	if strings.HasPrefix(modelID, "gpt-") || strings.Contains(modelID, "-codex") {
		return true
	}
	if len(modelID) < 2 || modelID[0] != 'o' || modelID[1] < '0' || modelID[1] > '9' {
		return false
	}
	return len(modelID) == 2 || modelID[2] == '-'
}

func toolNamesFromVisibleTools(visibleTools []toolpkg.Tool) map[string]struct{} {
	visible := make(map[string]struct{}, len(visibleTools))
	for _, tool := range visibleTools {
		visible[toolpkg.NormalizeName(tool.Name())] = struct{}{}
	}
	return visible
}

func visibleToolNamesIfNeeded(visibleTools []toolpkg.Tool) map[string]struct{} {
	for _, tool := range visibleTools {
		if _, ok := tool.(toolpkg.DescriptiveTool); ok {
			return toolNamesFromVisibleTools(visibleTools)
		}
	}
	return nil
}

func llmToolDefinitionsFromVisibleTools(visibleTools []toolpkg.Tool) []message.ToolDefinition {
	visibleNames := visibleToolNamesIfNeeded(visibleTools)
	defs := make([]message.ToolDefinition, len(visibleTools))
	for i, tool := range visibleTools {
		description := tool.Description()
		if descriptive, ok := tool.(toolpkg.DescriptiveTool); ok {
			description = descriptive.DescriptionForTools(visibleNames)
		}
		defs[i] = message.ToolDefinition{
			Name:        toolpkg.NormalizeName(tool.Name()),
			Description: description,
			InputSchema: tool.Parameters(),
		}
	}
	return defs
}
