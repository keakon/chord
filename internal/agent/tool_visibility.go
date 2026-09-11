package agent

import (
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	toolpkg "github.com/keakon/chord/internal/tools"
)

func visibleLLMTools(registry *toolpkg.Registry, ruleset permission.Ruleset, keepInternal func(string) bool, pctx toolPermissionContext) []toolpkg.Tool {
	if registry == nil {
		return nil
	}
	allTools := registry.ListTools()
	if len(allTools) == 0 {
		return nil
	}

	filtered := make([]toolpkg.Tool, 0, len(allTools))
	for _, tool := range allTools {
		name := toolpkg.NormalizeName(tool.Name())
		if controlled, ok := tool.(toolpkg.RulesetAwareVisibilityTool); ok && !controlled.VisibleWithRuleset(ruleset) {
			continue
		}
		// done exists to signal loop exit and nothing else, so it is mounted
		// only while loop mode is active. Keeping it off the surface otherwise
		// saves its definition on every request and spares the model the
		// "reply directly or call done?" decision that its own description
		// spends half its length arguing about. Loop entry mounts it — as a
		// late-mounted additional tool where the provider supports one, and
		// through a tool-surface rebuild elsewhere (mountDoneForLoopEntry).
		if name == toolpkg.NameDone && !pctx.LoopExitAuthorized {
			continue
		}
		disabled := ruleset.IsDisabled(name)
		// compact_context is registered only while the model-driven compaction
		// feature is enabled, so registration is the user's authorization:
		// wildcard-only rules (an allowlist's `"*": deny`) must not silently
		// hide the tool. Non-global rules whose tool pattern matches
		// compact_context still apply, so an explicit deny keeps IsDisabled
		// true.
		if name == toolpkg.NameCompactContext && compactContextPermissionAction(ruleset) != permission.ActionDeny {
			disabled = false
		}
		// Loop mode is the user's authorization for the loop's own exit
		// signal, so a wildcard-only deny must not strip it — an allowlist
		// role would otherwise be unable to run a loop at all. A rule naming
		// done still wins. See donePermissionAction.
		if name == toolpkg.NameDone && donePermissionAction(ruleset) != permission.ActionDeny {
			disabled = false
		}
		if !keepInternal(name) && disabled {
			continue
		}
		// The job management tools only read and stop jobs that shell starts,
		// so a ruleset that disables shell hides them too. A non-wildcard rule
		// naming the tool is the ruleset author asking for it directly and
		// overrides the coupling: a worker with shell denied may still be
		// granted job_output to read a job its owner started.
		if jobManagementTool(name) && ruleset.IsDisabled(toolpkg.NameShell) && !jobManagementToolRequested(ruleset, name) {
			continue
		}
		if available, ok := tool.(toolpkg.AvailableTool); ok && !available.IsAvailable() {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

// jobManagementTool reports whether name is one of the background-job
// management tools, which are only useful alongside shell.
func jobManagementTool(name string) bool {
	switch name {
	case toolpkg.NameJobOutput, toolpkg.NameJobList, toolpkg.NameJobKill:
		return true
	default:
		return false
	}
}

// jobManagementToolRequested reports whether a non-wildcard rule targets the
// tool, i.e. the ruleset author asked for it by name. The last such rule wins,
// so a later `job_output: deny` turns an earlier allow back off (and IsDisabled
// hides the tool first anyway). An allow or ask rule both count as the grant: a
// `job_output: ask` rule still means the user wants the tool on the surface,
// just confirmed per call.
func jobManagementToolRequested(ruleset permission.Ruleset, toolName string) bool {
	return specificToolRuleAction(ruleset, toolName, permission.ActionDeny) != permission.ActionDeny
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
	visible := visibleLLMTools(a.tools, a.effectiveRuleset(), isInternalControlTool, a.toolPermissionContext())
	filtered := filterVisibleTools(visible, isMainAgentReservedTool)
	// Apply per-model edit tool selection
	return filterEditToolsByModel(filtered, a.modelName, a.effectiveRuleset(), a.applyPatchSurfacePolicy())
}

// MainToolVisible reports whether the named tool is part of the MainAgent's
// live, model-appropriate tool surface (the same source the capability prompt
// and tool declarations are built from). Descriptions and prompt blocks that
// reference another tool by name must gate on this so they never push a tool
// the current model is not allowed to call. The result mirrors a frozen
// decision (the tool surface is stable within a session), so it is safe to
// bake into tool descriptions at registration time.
func (a *MainAgent) MainToolVisible(name string) bool {
	if a == nil {
		return false
	}
	_, ok := a.mainVisibleLLMToolNames()[toolpkg.NormalizeName(name)]
	return ok
}

// applyPatchSurfacePolicy resolves the apply_patch tool-surface decision from
// the bound LLM client's stable primary model-pool entry. The agent layer
// consumes the resolved policy instead of reading global configuration or
// re-running model-name inference against the current fallback cursor.
func (a *MainAgent) applyPatchSurfacePolicy() *bool {
	if a == nil {
		return nil
	}
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	if client == nil {
		return nil
	}
	return new(client.UsesApplyPatchSurface())
}

// filterEditToolsByModel applies per-model file tool selection to visible tools.
// It is the shared implementation used by both MainAgent and SubAgent to keep a
// consistent tool surface across agent types, filtering edit tools based on the
// active model and tool-wide availability.
// The logic is:
// 1. Check which edit tools are registered and not tool-wide disabled by permissions
// 2. If both are available, choose based on model preference
// 3. If only one is available, use that one regardless of model
// 4. If neither is available, remove both
// 5. When a patch-native model keeps apply_patch, also hide write and delete:
// the Codex envelope subsumes them (`*** Add File:` creates, `*** Delete File:`
// removes), matching the native Codex CLI surface those models are trained on.
// Fallback pairings keep write/delete: a non-patch-native model that only got
// apply_patch because edit is disabled is not trained to route everything
// through the envelope, and a patch-native model downgraded to edit needs write
// to create files at all (edit cannot).
//
// patchSurfaceDecision carries the client-resolved apply_patch tool-surface
// decision (compat override or primary-model inference); nil falls back to
// name-based inference via shouldUsePatchForModel.
func filterEditToolsByModel(tools []toolpkg.Tool, modelName string, ruleset permission.Ruleset, patchSurfaceDecision *bool) []toolpkg.Tool {
	// Check which edit tools are available. Path-scoped allow/ask rules still make
	// the tool usable, so visibility should only collapse when the whole tool is
	// disabled. Actual path authorization still happens at execution time.
	patchAvailable := false
	editAvailable := false
	for _, tool := range tools {
		switch toolpkg.NormalizeName(tool.Name()) {
		case toolpkg.NameApplyPatch:
			patchAvailable = true
		case toolpkg.NameEdit:
			editAvailable = true
		}
	}
	patchAllowed := patchAvailable && !ruleset.IsDisabled(toolpkg.NameApplyPatch)
	editAllowed := editAvailable && !ruleset.IsDisabled(toolpkg.NameEdit)

	// Determine which tool to keep
	var keepPatch bool
	patchNativeModel := shouldUsePatchForModel(modelName)
	if patchSurfaceDecision != nil {
		patchNativeModel = *patchSurfaceDecision
	}
	if patchAllowed && editAllowed {
		// Both allowed: use model preference
		keepPatch = patchNativeModel
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
			if name != toolpkg.NameApplyPatch && name != toolpkg.NameEdit {
				filtered = append(filtered, tool)
			}
		}
		return filtered
	}

	hideWriteDelete := keepPatch && patchNativeModel
	filtered := make([]toolpkg.Tool, 0, len(tools))
	for _, tool := range tools {
		name := toolpkg.NormalizeName(tool.Name())
		// Filter out the non-matching edit tool
		if name == toolpkg.NameApplyPatch && !keepPatch {
			continue
		}
		if name == toolpkg.NameEdit && keepPatch {
			continue
		}
		if hideWriteDelete && (name == toolpkg.NameWrite || name == toolpkg.NameDelete) {
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
	// The patch tool surface is a separate dimension from the freeform wire
	// shape: a gpt-5 family model keeps the patch tool surface even when
	// compat.apply_patch.freeform forces the JSON function wire shape (e.g. on
	// a non-Responses endpoint or a host that rejects custom tools).
	return llm.IsApplyPatchModel(modelID)
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
