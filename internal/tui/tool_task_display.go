package tui

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

type taskToolArgs struct {
	Description string `json:"description"`
	AgentType   string `json:"agent_type"`
}

type cancelToolArgs struct {
	TargetTaskID string `json:"target_task_id"`
	Reason       string `json:"reason,omitempty"`
}

type notifyToolArgs struct {
	TargetTaskID string `json:"target_task_id,omitempty"`
	Message      string `json:"message"`
	Kind         string `json:"kind,omitempty"`
}

func parseTaskToolArgs(argsJSON string) taskToolArgs {
	if strings.TrimSpace(argsJSON) == "" {
		return taskToolArgs{}
	}
	var parsed taskToolArgs
	if json.Unmarshal([]byte(argsJSON), &parsed) != nil {
		_, vals := parseToolArgs(argsJSON)
		parsed.Description = vals["description"]
		parsed.AgentType = vals["agent_type"]
	}
	parsed.Description = sanitizeToolDisplayText(strings.TrimSpace(parsed.Description))
	parsed.AgentType = sanitizeToolDisplayText(strings.TrimSpace(parsed.AgentType))
	return parsed
}

// parseTaskToolHandle parses the delegate/task result payload into the
// canonical tools.TaskHandle and surfaces any trailing prose the runtime
// appends after the JSON (e.g. "Note: ignored unrecognized parameter(s): …").
// json.Unmarshal rejects trailing text, so the previous parser silently fell
// through to dumping the whole result back into the card body — a stale raw
// JSON blob the user had to read past to find anything structured. This
// tolerant form decodes the first JSON value, returns the rest for the
// caller to render as a note, and only reports ok when at least one
// displayable field is non-empty.
func parseTaskToolHandle(result string) (tools.TaskHandle, string, bool) {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return tools.TaskHandle{}, "", false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	var handle tools.TaskHandle
	if err := dec.Decode(&handle); err != nil {
		return tools.TaskHandle{}, trimmed, false
	}
	rest := strings.TrimSpace(trimmed[dec.InputOffset():])
	if !taskHandleHasContent(handle) {
		return tools.TaskHandle{}, rest, false
	}
	return handle, rest, true
}

func taskHandleHasContent(h tools.TaskHandle) bool {
	return h.Status != "" ||
		h.TaskID != "" ||
		h.AgentID != "" ||
		h.PreviousAgentID != "" ||
		h.Rehydrated ||
		h.Message != "" ||
		h.PlanTaskRef != "" ||
		h.SemanticTaskKey != "" ||
		h.ScopeConflict ||
		h.DuplicateDetected ||
		h.SuggestedTaskID != "" ||
		h.SuggestedAgentID != "" ||
		h.SuggestedAction != "" ||
		!isWriteScopeEmpty(h.ExpectedWriteScope)
}

// isWriteScopeEmpty reports whether a WriteScope has no declared content.
// WriteScope contains slice fields, so it cannot be compared with == and
// each field has to be checked individually.
func isWriteScopeEmpty(s tools.WriteScope) bool {
	return !s.ReadOnly && len(s.Files) == 0 && len(s.PathPrefix) == 0 && len(s.Modules) == 0
}

func taskToolDescriptionContent(argsJSON string) string {
	args := parseTaskToolArgs(argsJSON)
	if args.Description == "" {
		return ""
	}
	desc := strings.ReplaceAll(args.Description, "\r\n", "\n")
	desc = strings.ReplaceAll(desc, "\r", "\n")
	return strings.TrimSpace(desc)
}

func taskToolExpandedDescriptionLines(argsJSON string, width int) []string {
	desc := taskToolDescriptionContent(argsJSON)
	if desc == "" {
		return nil
	}
	return toolExpandedTextLines(desc, width)
}

func taskToolCollapsedHandleSummary(result string) string {
	handle, _, ok := parseTaskToolHandle(result)
	if !ok {
		return strings.TrimSpace(result)
	}
	var parts []string
	switch handle.Status {
	case "resumed":
		parts = append(parts, "Resumed")
	case "rehydrated":
		parts = append(parts, "Rehydrated")
	default:
		parts = append(parts, "Spawned")
	}
	if handle.AgentID != "" {
		parts = append(parts, handle.AgentID)
	}
	return strings.Join(parts, " · ")
}

// taskToolExpandedHandleLines produces the plain "key: value" lines used by
// the non-TUI display path (skill/result text extraction in
// tool_skill_display.go). The TUI card itself renders the same handle via
// appendTaskHandleFieldRows and the field-row primitives; this helper
// exists so the skill surface keeps a single-line-per-field format. Any
// runtime note appended after the JSON is intentionally dropped here — the
// caller's contract is the handle fields, not the surrounding commentary.
func taskToolExpandedHandleLines(result string) []string {
	handle, _, ok := parseTaskToolHandle(result)
	if !ok {
		trimmed := strings.TrimSpace(result)
		if trimmed == "" {
			return nil
		}
		return []string{sanitizeToolDisplayText(trimmed)}
	}
	var lines []string
	if handle.AgentID != "" {
		lines = append(lines, "agent_id: "+sanitizeToolDisplayText(handle.AgentID))
	}
	if handle.PreviousAgentID != "" {
		lines = append(lines, "previous_agent_id: "+sanitizeToolDisplayText(handle.PreviousAgentID))
	}
	if handle.TaskID != "" {
		// The readable form drops the internal "adhoc-" prefix and marks the
		// number with "#" so it still reads as a task handle.
		lines = append(lines, "task_id: "+sanitizeToolDisplayText(extractReadableTarget(handle.TaskID)))
	}
	if handle.Status != "" {
		lines = append(lines, "status: "+sanitizeToolDisplayText(handle.Status))
	}
	if handle.Rehydrated {
		lines = append(lines, "rehydrated: true")
	}
	if handle.Message != "" {
		lines = append(lines, "message: "+sanitizeToolDisplayText(handle.Message))
	}
	return lines
}

func parseCancelToolArgs(argsJSON string) cancelToolArgs {
	if strings.TrimSpace(argsJSON) == "" {
		return cancelToolArgs{}
	}
	var parsed cancelToolArgs
	if json.Unmarshal([]byte(argsJSON), &parsed) != nil {
		_, vals := parseToolArgs(argsJSON)
		parsed.TargetTaskID = vals["target_task_id"]
		parsed.Reason = vals["reason"]
	}
	parsed.TargetTaskID = sanitizeToolDisplayText(strings.TrimSpace(parsed.TargetTaskID))
	parsed.Reason = sanitizeToolDisplayText(strings.TrimSpace(parsed.Reason))
	return parsed
}

func parseNotifyToolArgs(argsJSON string) notifyToolArgs {
	if strings.TrimSpace(argsJSON) == "" {
		return notifyToolArgs{}
	}
	var parsed notifyToolArgs
	if json.Unmarshal([]byte(argsJSON), &parsed) != nil {
		_, vals := parseToolArgs(argsJSON)
		parsed.TargetTaskID = vals["target_task_id"]
		parsed.Message = vals["message"]
		parsed.Kind = vals["kind"]
	}
	parsed.TargetTaskID = sanitizeToolDisplayText(strings.TrimSpace(parsed.TargetTaskID))
	parsed.Message = sanitizeToolDisplayText(strings.TrimSpace(parsed.Message))
	parsed.Kind = sanitizeToolDisplayText(strings.TrimSpace(parsed.Kind))
	return parsed
}

// extractReadableTarget renders a task handle for display. An ad-hoc handle
// ("adhoc-8") drops the internal "adhoc-" prefix but keeps a "#" marker, so
// the bare number still reads as a task handle instead of being taken for an
// unrelated number such as a plan task's own "8". Any other form — a plan
// task reference, say — is shown unchanged.
func extractReadableTarget(taskID string) string {
	if taskID == "" {
		return ""
	}
	after, ok := strings.CutPrefix(taskID, "adhoc-")
	if !ok {
		return taskID
	}
	if after == "" {
		return ""
	}
	return "#" + after
}
