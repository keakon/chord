package externalclaude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"
)

// Stream event types emitted on stdout.
const (
	TypeSystem               = "system"
	TypeAssistant            = "assistant"
	TypeUser                 = "user"
	TypeResult               = "result"
	TypeStreamEvent          = "stream_event"
	TypeControlRequest       = "control_request"
	TypeControlResponse      = "control_response"
	TypeControlCancelRequest = "control_cancel_request"
)

// Event is one decoded stdout line. Claude Code uses a flat envelope: fields
// that do not apply to Type are zero.
type Event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	SessionID         string   `json:"session_id,omitempty"`
	UUID              string   `json:"uuid,omitempty"`
	ParentToolUseID   string   `json:"parent_tool_use_id,omitempty"`
	Model             string   `json:"model,omitempty"`
	PermissionMode    string   `json:"permissionMode,omitempty"`
	ClaudeCodeVersion string   `json:"claude_code_version,omitempty"`
	Tools             []string `json:"tools,omitempty"`

	Message     *Message     `json:"message,omitempty"`
	StreamEvent *StreamEvent `json:"-"`

	// result fields.
	Result            string             `json:"result,omitempty"`
	IsError           bool               `json:"is_error,omitempty"`
	DurationMS        int64              `json:"duration_ms,omitempty"`
	DurationAPIMS     int64              `json:"duration_api_ms,omitempty"`
	NumTurns          int                `json:"num_turns,omitempty"`
	TotalCostUSD      float64            `json:"total_cost_usd,omitempty"`
	TerminalReason    string             `json:"terminal_reason,omitempty"`
	Usage             *Usage             `json:"usage,omitempty"`
	PermissionDenials []PermissionDenial `json:"permission_denials,omitempty"`

	// control_request / control_response.
	RequestID string               `json:"request_id,omitempty"`
	Request   *ControlRequest      `json:"request,omitempty"`
	Response  *ControlResponseBody `json:"response,omitempty"`
}

type PermissionDenial struct {
	ToolName  string          `json:"tool_name,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
}

type Message struct {
	ID         string         `json:"id,omitempty"`
	Role       string         `json:"role,omitempty"`
	Model      string         `json:"model,omitempty"`
	Content    []ContentBlock `json:"content,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
}

// StreamEvent is the nested Anthropic message event carried by a
// stream_event envelope. Complete assistant events remain the authoritative
// persisted message.
type StreamEvent struct {
	Type         string        `json:"type"`
	Delta        *StreamDelta  `json:"delta,omitempty"`
	ContentBlock *ContentBlock `json:"content_block,omitempty"`
}

type StreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// UnmarshalJSON accepts "event" only when it carries the stream_event object.
// The CLI reuses the same key for string-valued UI system events such as
// system/ui_invalidate ("event":"ui.render"); rejecting those would abort the
// session before its initialize acknowledgment.
func (e *Event) UnmarshalJSON(data []byte) error {
	type eventFields Event
	fields := struct {
		*eventFields
		Event json.RawMessage `json:"event,omitempty"`
	}{eventFields: (*eventFields)(e)}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields.Event) == 0 || fields.Event[0] != '{' {
		return nil
	}
	var stream StreamEvent
	if err := json.Unmarshal(fields.Event, &stream); err != nil {
		return err
	}
	e.StreamEvent = &stream
	return nil
}

// Decode parses one stdout line. A frame must decode completely when its type
// drives authorization or the task outcome; system and unknown frames that gain
// an incompatible field shape fall back to their envelope so an additive CLI
// change cannot abort a healthy session.
func Decode(line []byte) (Event, error) {
	event, err := decodeEvent(line)
	if err == nil {
		if event.Type == "" {
			return Event{}, fmt.Errorf("missing event type")
		}
		return event, nil
	}
	var envelope struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype,omitempty"`
	}
	if json.Unmarshal(line, &envelope) != nil || envelope.Type == "" || actionableEventType(envelope.Type) {
		return Event{}, err
	}
	if envelope.Type == TypeSystem && envelope.Subtype == "init" {
		// The init frame carries the tool catalogue the worker validates against
		// the managed set. Other init fields may drift, but a catalogue that is
		// present and no longer a string array must fail closed instead of
		// silently skipping that check.
		tools, err := decodeInitToolCatalogue(line)
		if err != nil {
			return Event{}, err
		}
		return Event{Type: envelope.Type, Subtype: envelope.Subtype, Tools: tools}, nil
	}
	log.Debugf("external protocol frame tolerated type=%v subtype=%v error=%v", envelope.Type, envelope.Subtype, err)
	return Event{Type: envelope.Type, Subtype: envelope.Subtype}, nil
}

func decodeInitToolCatalogue(line []byte) ([]string, error) {
	var init struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(line, &init); err != nil {
		return nil, err
	}
	if len(init.Tools) == 0 || string(init.Tools) == "null" {
		return nil, nil
	}
	var tools []string
	if err := json.Unmarshal(init.Tools, &tools); err != nil {
		return nil, fmt.Errorf("system/init tools: %w", err)
	}
	return tools, nil
}

func decodeEvent(line []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(line, &event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func actionableEventType(eventType string) bool {
	switch eventType {
	case TypeAssistant, TypeUser, TypeResult, TypeStreamEvent,
		TypeControlRequest, TypeControlResponse, TypeControlCancelRequest:
		return true
	default:
		return false
	}
}

// AssistantText concatenates the text blocks of an assistant event.
func (e Event) AssistantText() string {
	if e.Message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range e.Message.Content {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

// ToolUses returns the tool_use blocks carried by an assistant event.
func (e Event) ToolUses() []ContentBlock {
	if e.Message == nil {
		return nil
	}
	var uses []ContentBlock
	for _, block := range e.Message.Content {
		if block.Type == "tool_use" {
			uses = append(uses, block)
		}
	}
	return uses
}

// Succeeded reports a terminal result that did not carry an error. The
// result subtype is not a reliable signal: an authentication failure was
// observed with subtype "success" and is_error true.
func (e Event) Succeeded() bool {
	return e.Type == TypeResult && !e.IsError
}

// Failed reports a terminal result that carried an error.
func (e Event) Failed() bool {
	return e.Type == TypeResult && e.IsError
}
