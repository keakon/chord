package agent

import (
	"encoding/json"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type speculativeExecutionDecision struct {
	Allowed bool
	Reason  string
}

func allowSpeculativeExecution() speculativeExecutionDecision {
	return speculativeExecutionDecision{Allowed: true, Reason: "safe_read_only"}
}

func rejectSpeculativeExecution(reason string) speculativeExecutionDecision {
	return speculativeExecutionDecision{Allowed: false, Reason: reason}
}

// evaluateSpeculativeExecutionPolicy is the streaming-execution safety gate.
// Side-effecting and interactive tools stay out of speculative execution
// until each tool has an audited rollback / interrupt protocol.
func evaluateSpeculativeExecutionPolicy(registry *tools.Registry, ruleset permission.Ruleset, toolName string, args json.RawMessage) speculativeExecutionDecision {
	return evaluateSpeculativeExecutionPolicyWithPrefix(registry, ruleset, toolName, args, nil)
}

func evaluateSpeculativeExecutionPolicyWithPrefix(registry *tools.Registry, ruleset permission.Ruleset, toolName string, args json.RawMessage, priorCalls []PendingToolCall) speculativeExecutionDecision {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return rejectSpeculativeExecution("missing_tool_name")
	}
	if llm.IsMalformedArgs(args) || llm.IsEmptyArgs(args) {
		return rejectSpeculativeExecution("invalid_args")
	}
	if registry != nil {
		if _, ok := registry.Get(toolName); !ok {
			return rejectSpeculativeExecution("unknown_tool")
		}
	}
	if len(ruleset) > 0 && !isInternalControlTool(toolName) {
		decision := evaluateToolPermission(ruleset, toolName, args)
		if decision.Action != permission.ActionAllow {
			return rejectSpeculativeExecution("permission_" + string(decision.Action))
		}
	}

	class := tools.ConcurrencyClassForTool(registry, toolName, args)
	if class != tools.ToolConcurrencyClassReadOnly {
		switch toolName {
		case tools.NameSpawn, tools.NameSpawnStop:
			return rejectSpeculativeExecution("process_side_effect")
		case tools.NameQuestion:
			return rejectSpeculativeExecution("interactive_tool")
		case tools.NameTodoWrite, tools.NameDelegate, tools.NameNotify, tools.NameHandoff, tools.NameEscalate, tools.NameCancel, tools.NameComplete, tools.NameSaveArtifact:
			return rejectSpeculativeExecution("stateful_or_control_tool")
		case tools.NameWrite, tools.NameEdit, tools.NameDelete:
			return rejectSpeculativeExecution("mutation_tool")
		case tools.NameShell:
			return rejectSpeculativeExecution("shell_not_static_read_only")
		default:
			return rejectSpeculativeExecution("not_in_speculative_allowlist")
		}
	}
	if blocking, ok := firstBlockingPriorSpeculativeCall(registry, ruleset, priorCalls); ok {
		return rejectSpeculativeExecution("prior_pending_non_read_only:" + blocking)
	}
	return allowSpeculativeExecution()
}

func firstBlockingPriorSpeculativeCall(registry *tools.Registry, ruleset permission.Ruleset, priorCalls []PendingToolCall) (string, bool) {
	for _, call := range priorCalls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		args := json.RawMessage(call.ArgsJSON)
		if len(ruleset) > 0 && !isInternalControlTool(name) {
			decision := evaluateToolPermission(ruleset, name, args)
			if decision.Action != permission.ActionAllow {
				return name, true
			}
		}
		if registry != nil {
			if _, ok := registry.Get(name); !ok {
				return name, true
			}
		}
		if tools.ConcurrencyClassForTool(registry, name, args) != tools.ToolConcurrencyClassReadOnly {
			return name, true
		}
	}
	return "", false
}
func logSpeculativeExecutionDecision(callID, toolName string, decision speculativeExecutionDecision) {
	if strings.TrimSpace(callID) == "" || decision.Allowed {
		return
	}
	log.Debugf("speculative execution skipped call_id=%s tool=%s reason=%s", callID, toolName, decision.Reason)
}
