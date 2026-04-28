package tui

import (
	"encoding/json"
	"strings"
)

const maxTaskCollapsedPreviewLogicalLines = 2

type taskToolArgs struct {
	Description string `json:"description"`
	AgentType   string `json:"agent_type"`
}

type taskToolHandle struct {
	Status          string `json:"status"`
	TaskID          string `json:"task_id"`
	AgentID         string `json:"agent_id"`
	PreviousAgentID string `json:"previous_agent_id,omitempty"`
	Rehydrated      bool   `json:"rehydrated,omitempty"`
	Message         string `json:"message"`
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
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	parsed.Description = strings.TrimSpace(parsed.Description)
	parsed.AgentType = strings.TrimSpace(parsed.AgentType)
	return parsed
}

func parseTaskToolHandle(result string) (taskToolHandle, bool) {
	if strings.TrimSpace(result) == "" {
		return taskToolHandle{}, false
	}
	var handle taskToolHandle
	if err := json.Unmarshal([]byte(result), &handle); err != nil {
		return taskToolHandle{}, false
	}
	handle.Status = strings.TrimSpace(handle.Status)
	handle.TaskID = strings.TrimSpace(handle.TaskID)
	handle.AgentID = strings.TrimSpace(handle.AgentID)
	handle.PreviousAgentID = strings.TrimSpace(handle.PreviousAgentID)
	handle.Message = strings.TrimSpace(handle.Message)
	if handle.Status == "" && handle.TaskID == "" && handle.AgentID == "" && handle.PreviousAgentID == "" && handle.Message == "" && !handle.Rehydrated {
		return taskToolHandle{}, false
	}
	return handle, true
}

func taskToolHeaderTitle(argsJSON string) string {
	return ""
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

func taskToolCollapsedDescriptionLines(argsJSON string, width int) ([]string, int) {
	desc := taskToolDescriptionContent(argsJSON)
	if desc == "" {
		return nil, 0
	}
	rawLines := strings.Split(desc, "\n")
	previewLines := make([]string, 0, maxTaskCollapsedPreviewLogicalLines)
	hidden := 0
	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(previewLines) < maxTaskCollapsedPreviewLogicalLines {
			previewLines = append(previewLines, trimmed)
			continue
		}
		hidden++
	}
	if len(previewLines) == 0 {
		return nil, 0
	}
	return wrapText(strings.Join(previewLines, "\n"), width), hidden
}

func taskToolExpandedDescriptionLines(argsJSON string, width int) []string {
	desc := taskToolDescriptionContent(argsJSON)
	if desc == "" {
		return nil
	}
	return toolExpandedTextLines(desc, width)
}

func taskToolCollapsedHandleSummary(result string) string {
	handle, ok := parseTaskToolHandle(result)
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

func taskToolExpandedHandleLines(result string) []string {
	handle, ok := parseTaskToolHandle(result)
	if !ok {
		trimmed := strings.TrimSpace(result)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	}
	var lines []string
	if handle.AgentID != "" {
		lines = append(lines, "agent_id: "+handle.AgentID)
	}
	if handle.PreviousAgentID != "" {
		lines = append(lines, "previous_agent_id: "+handle.PreviousAgentID)
	}
	if handle.TaskID != "" {
		lines = append(lines, "task_id: "+handle.TaskID)
	}
	if handle.Status != "" {
		lines = append(lines, "status: "+handle.Status)
	}
	if handle.Rehydrated {
		lines = append(lines, "rehydrated: true")
	}
	if handle.Message != "" {
		lines = append(lines, "message: "+handle.Message)
	}
	return lines
}

func parseCancelToolArgs(argsJSON string) cancelToolArgs {
	if strings.TrimSpace(argsJSON) == "" {
		return cancelToolArgs{}
	}
	var parsed cancelToolArgs
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	parsed.TargetTaskID = strings.TrimSpace(parsed.TargetTaskID)
	parsed.Reason = strings.TrimSpace(parsed.Reason)
	return parsed
}

func parseNotifyToolArgs(argsJSON string) notifyToolArgs {
	if strings.TrimSpace(argsJSON) == "" {
		return notifyToolArgs{}
	}
	var parsed notifyToolArgs
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	parsed.TargetTaskID = strings.TrimSpace(parsed.TargetTaskID)
	parsed.Message = strings.TrimSpace(parsed.Message)
	parsed.Kind = strings.TrimSpace(parsed.Kind)
	return parsed
}

func extractReadableTarget(taskID string) string {
	if taskID == "" {
		return ""
	}
	if strings.HasPrefix(taskID, "adhoc-") {
		return strings.TrimPrefix(taskID, "adhoc-")
	}
	return taskID
}
