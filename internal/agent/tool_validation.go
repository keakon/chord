package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func validateToolArgsAgainstSchema(registry *tools.Registry, toolName string, args json.RawMessage) error {
	if registry == nil {
		return nil
	}
	tool, ok := registry.Get(toolName)
	if !ok {
		return nil
	}
	return tools.ValidateToolArgs(tool, args)
}

func applyConfirmedArgsEdits(registry *tools.Registry, ruleset permission.Ruleset, toolName string, original json.RawMessage, modifiedArgs string) (json.RawMessage, error) {
	if strings.TrimSpace(modifiedArgs) == "" {
		return original, nil
	}

	edited := json.RawMessage(modifiedArgs)
	if err := validateToolArgsAgainstSchema(registry, toolName, edited); err != nil {
		return nil, fmt.Errorf("edited arguments for tool %q are invalid: %w", toolName, err)
	}

	decision := evaluateToolPermission(ruleset, toolName, edited)
	if decision.Action == permission.ActionDeny {
		return nil, wrapEditedArgsPermissionDenied(toolName)
	}
	return edited, nil
}

func buildToolArgsAudit(original json.RawMessage, effective json.RawMessage, editSummary string) *message.ToolArgsAudit {
	originalJSON := strings.TrimSpace(string(original))
	effectiveJSON := strings.TrimSpace(string(effective))
	if originalJSON == "" && effectiveJSON == "" && strings.TrimSpace(editSummary) == "" {
		return nil
	}
	userModified := effectiveJSON != "" && effectiveJSON != originalJSON
	if effectiveJSON == "" {
		effectiveJSON = originalJSON
	}
	return &message.ToolArgsAudit{
		OriginalArgsJSON:  originalJSON,
		EffectiveArgsJSON: effectiveJSON,
		UserModified:      userModified,
		EditSummary:       strings.TrimSpace(editSummary),
	}
}

func auditEffectiveArgsJSON(args json.RawMessage, audit *message.ToolArgsAudit) string {
	if audit != nil && strings.TrimSpace(audit.EffectiveArgsJSON) != "" {
		return audit.EffectiveArgsJSON
	}
	return string(args)
}

func syncAuditEffectiveArgs(audit *message.ToolArgsAudit, effective json.RawMessage) *message.ToolArgsAudit {
	if audit == nil {
		return nil
	}
	cloned := audit.Clone()
	cloned.EffectiveArgsJSON = strings.TrimSpace(string(effective))
	if cloned.EffectiveArgsJSON == "" {
		cloned.EffectiveArgsJSON = cloned.OriginalArgsJSON
	}
	cloned.UserModified = strings.TrimSpace(cloned.EffectiveArgsJSON) != strings.TrimSpace(cloned.OriginalArgsJSON)
	return cloned
}
