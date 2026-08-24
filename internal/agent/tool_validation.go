package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type toolArgsAbnormality struct {
	Malformed      bool
	EmptyRequired  bool
	RequiredFields []string
}

func classifyToolArgsAbnormality(registry *tools.Registry, toolName string, args json.RawMessage) toolArgsAbnormality {
	if llm.IsMalformedArgs(args) {
		return toolArgsAbnormality{Malformed: true}
	}
	if !llm.IsEmptyArgs(args) || registry == nil {
		return toolArgsAbnormality{}
	}
	tool, ok := registry.Get(toolName)
	if !ok {
		return toolArgsAbnormality{}
	}
	required := llm.RequiredFields(tool.Parameters())
	if len(required) == 0 {
		return toolArgsAbnormality{}
	}
	return toolArgsAbnormality{EmptyRequired: true, RequiredFields: required}
}

func isAbnormalToolArgs(registry *tools.Registry, toolName string, args json.RawMessage) bool {
	abnormality := classifyToolArgsAbnormality(registry, toolName, args)
	return abnormality.Malformed || abnormality.EmptyRequired
}

// validateEditedToolArgs checks user-edited arguments against the tool schema.
// Unknown fields follow the execution-time contract: they are tolerated here
// and stripped by validateToolCallArgs, which runs after this edit is applied,
// so only invalid JSON, missing required fields, and wrongly typed values
// reject an edit.
func validateEditedToolArgs(registry *tools.Registry, toolName string, args json.RawMessage) error {
	if registry == nil {
		return nil
	}
	tool, ok := registry.Get(toolName)
	if !ok {
		return nil
	}
	return tools.ValidateToolArgs(tool, llm.UnwrapToolArgs(args))
}

func applyConfirmedArgsEdits(registry *tools.Registry, ruleset permission.Ruleset, toolName string, original json.RawMessage, modifiedArgs string) (json.RawMessage, error) {
	if strings.TrimSpace(modifiedArgs) == "" {
		return original, nil
	}

	edited := json.RawMessage(modifiedArgs)
	if err := validateEditedToolArgs(registry, toolName, edited); err != nil {
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

func syncAuditEffectiveArgs(audit *message.ToolArgsAudit, original json.RawMessage, effective json.RawMessage) *message.ToolArgsAudit {
	if audit == nil {
		return buildToolArgsAudit(original, effective, "")
	}
	cloned := audit.Clone()
	cloned.EffectiveArgsJSON = strings.TrimSpace(string(effective))
	if cloned.EffectiveArgsJSON == "" {
		cloned.EffectiveArgsJSON = cloned.OriginalArgsJSON
	}
	cloned.UserModified = strings.TrimSpace(cloned.EffectiveArgsJSON) != strings.TrimSpace(cloned.OriginalArgsJSON)
	return cloned
}

// promotedToolAudit layers the on_tool_call hook's audit over the audit the
// promoted speculative execution produced. The hook branch records provenance
// (what the model originally asked for, that an upstream edit happened), but it
// never ran the tool; the speculative execution did, after stripping fields the
// schema does not declare. Promote only succeeds when the hook's arguments are
// canonically identical to the speculative ones, so exactly the same fields
// were stripped and executedArgsJSON is what actually ran. Taking the hook's
// audit wholesale would advertise its unsanitized arguments as effective and
// drop the argument diagnostics rendered by the tool card.
func promotedToolAudit(hookAudit, executedAudit *message.ToolArgsAudit, executedArgsJSON string) *message.ToolArgsAudit {
	if hookAudit == nil {
		return executedAudit
	}
	merged := hookAudit.Clone()
	merged.EffectiveArgsJSON = executedArgsJSON
	merged.IgnoredArgs = nil
	merged.InvalidArgs = nil
	if executedAudit != nil {
		merged.IgnoredArgs = slices.Clone(executedAudit.IgnoredArgs)
		merged.InvalidArgs = slices.Clone(executedAudit.InvalidArgs)
	}
	return merged
}
