package externalclaude

import (
	"encoding/json"
)

// Control subtypes exchanged over stdout/stdin in stream-json mode.
const (
	ControlSubtypeCanUseTool = "can_use_tool"
	ControlSubtypeInterrupt  = "interrupt"
	ControlSubtypeInitialize = "initialize"

	ControlResponseSuccess = "success"

	PermissionBehaviorAllow = "allow"
	PermissionBehaviorDeny  = "deny"
)

// ControlRequest is the body of a control_request frame. The CLI sends
// can_use_tool requests to the host; the host sends initialization and
// interrupt requests to the CLI.
type ControlRequest struct {
	Subtype string `json:"subtype"`

	ToolName              string          `json:"tool_name,omitempty"`
	Input                 json.RawMessage `json:"input,omitempty"`
	ToolUseID             string          `json:"tool_use_id,omitempty"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions,omitempty"`
	BlockedPath           string          `json:"blocked_path,omitempty"`
	AgentID               string          `json:"agent_id,omitempty"`

	AppendSystemPrompt string `json:"appendSystemPrompt,omitempty"`
}

// ControlResponseBody is the body of a control_response frame.
type ControlResponseBody struct {
	Subtype   string          `json:"subtype,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// Frame is one JSON object written to the CLI's stdin.
type Frame struct {
	Type      string               `json:"type"`
	RequestID string               `json:"request_id,omitempty"`
	Request   *ControlRequest      `json:"request,omitempty"`
	Response  *ControlResponseBody `json:"response,omitempty"`
}

// PermissionDecision is the allow/deny payload Chord returns for can_use_tool.
type PermissionDecision struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

func AllowPermission(requestID string, updatedInput json.RawMessage) Frame {
	decision, _ := json.Marshal(PermissionDecision{
		Behavior:     PermissionBehaviorAllow,
		UpdatedInput: updatedInput,
	})
	return Frame{
		Type: TypeControlResponse,
		Response: &ControlResponseBody{
			Subtype:   ControlResponseSuccess,
			RequestID: requestID,
			Response:  decision,
		},
	}
}

func DenyPermission(requestID, message string) Frame {
	decision, _ := json.Marshal(PermissionDecision{
		Behavior: PermissionBehaviorDeny,
		Message:  message,
	})
	return Frame{
		Type: TypeControlResponse,
		Response: &ControlResponseBody{
			Subtype:   ControlResponseSuccess,
			RequestID: requestID,
			Response:  decision,
		},
	}
}

func InterruptRequest(requestID string) Frame {
	return Frame{
		Type:      TypeControlRequest,
		RequestID: requestID,
		Request:   &ControlRequest{Subtype: ControlSubtypeInterrupt},
	}
}

func InitializeRequest(requestID, appendSystemPrompt string) Frame {
	return Frame{
		Type: TypeControlRequest, RequestID: requestID,
		Request: &ControlRequest{Subtype: ControlSubtypeInitialize, AppendSystemPrompt: appendSystemPrompt},
	}
}
