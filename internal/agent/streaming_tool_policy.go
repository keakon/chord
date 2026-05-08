package agent

import (
	"encoding/json"
	"strings"
	"unicode"

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

	switch toolName {
	case tools.NameRead, tools.NameGrep, tools.NameGlob:
		return allowSpeculativeExecution()
	case tools.NameBash:
		if bashReadOnlySpeculativeAllowed(args) {
			return allowSpeculativeExecution()
		}
		return rejectSpeculativeExecution("bash_not_static_read_only")
	case tools.NameWrite, tools.NameEdit, tools.NameDelete:
		return allowSpeculativeExecution()
	case tools.NameSpawn, tools.NameSpawnStop:
		return rejectSpeculativeExecution("process_side_effect")
	case tools.NameQuestion:
		return rejectSpeculativeExecution("interactive_tool")
	case tools.NameTodoWrite, tools.NameDelegate, tools.NameNotify, tools.NameHandoff, tools.NameEscalate, tools.NameCancel, tools.NameComplete, tools.NameSaveArtifact:
		return rejectSpeculativeExecution("stateful_or_control_tool")
	default:
		return rejectSpeculativeExecution("not_in_speculative_allowlist")
	}
}

func bashReadOnlySpeculativeAllowed(args json.RawMessage) bool {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(llm.UnwrapToolArgs(args), &parsed); err != nil {
		return false
	}
	command := strings.TrimSpace(parsed.Command)
	if command == "" || containsShellMetachar(command) {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	cmd := fields[0]
	switch cmd {
	case "pwd", "ls", "cat", "which":
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "status", "log", "diff", "show", "branch", "rev-parse":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func containsShellMetachar(command string) bool {
	for _, r := range command {
		switch r {
		case '|', '&', ';', '<', '>', '(', ')', '$', '`', '\\', '*', '?', '[', ']', '{', '}', '\n', '\r':
			return true
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func logSpeculativeExecutionDecision(callID, toolName string, decision speculativeExecutionDecision) {
	if strings.TrimSpace(callID) == "" || decision.Allowed {
		return
	}
	log.Debugf("speculative execution skipped call_id=%s tool=%s reason=%s", callID, toolName, decision.Reason)
}
