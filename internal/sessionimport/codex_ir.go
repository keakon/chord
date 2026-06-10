package sessionimport

import (
	"encoding/json"

	"github.com/keakon/chord/internal/message"
)

type codexMessageSource string

const (
	codexMessageSourceResponseItem codexMessageSource = codexTopLevelTypeResponseItem
	codexMessageSourceEventMsg     codexMessageSource = codexTopLevelTypeEventMsg
)

type codexOrderedKind string

const (
	codexOrderedKindUser      codexOrderedKind = codexOrderedKind(message.RoleUser)
	codexOrderedKindAssistant codexOrderedKind = codexOrderedKind(message.RoleAssistant)
	codexOrderedKindToolCall  codexOrderedKind = "tool_call"
)

// ---------------------------------------------------------------------------
// Codex intermediate representation (IR)
//
// The IR sits between raw JSONL parsing and Chord message linearization.
// It preserves turn grouping, call-id lineage, and source-precedence
// decisions so the final conversion can emit structurally valid Chord
// messages without re-scanning the original rollout lines.
// ---------------------------------------------------------------------------

// codexTurn represents one logical turn in a Codex session.
// Content is grouped by turn identity (explicit turn_id or turn_context).
type codexTurn struct {
	TurnID string

	// Canonical content – chosen by precedence after parsing.
	UserMessages      []codexMessageItem
	AssistantMessages []codexMessageItem
	ReasoningEntries  []codexReasoningEntry

	// Structured tool activity keyed by call_id.
	ToolCalls   map[string]*codexToolCall
	ToolResults map[string]*codexToolResult

	// Supplemental metadata.
	UsageEvents        []codexTokenUsageEvent
	SourceOrder        []codexOrderRef
	HasExplicitTurnID  bool
	HasTurnContext     bool
	FallbackTurnNumber int
}

// codexMessageItem holds a single user/assistant text message before
// Chord linearization.
type codexMessageItem struct {
	Role        message.Role
	Content     string
	Source      codexMessageSource
	SourceOrder int
}

// codexReasoningEntry holds visible reasoning text that may be attached
// to an assistant message later.
type codexReasoningEntry struct {
	Text        string
	TurnID      string
	SourceOrder int
}

// codexToolCall holds a structured tool call from a function_call
// response_item.
type codexToolCall struct {
	CallID       string
	Name         string
	Arguments    json.RawMessage // raw arguments JSON string from rollout
	TurnID       string
	SourceOrder  int
	StatusEvents []codexEvent
}

// codexToolResult holds a structured tool result from a
// function_call_output response_item.
type codexToolResult struct {
	CallID      string
	Output      string // text output
	TurnID      string
	SourceOrder int
	Status      string // inferred terminal status: "success" or "error"
}

// codexTokenUsageEvent records a token_count event_msg for potential
// attachment to assistant messages.
type codexTokenUsageEvent struct {
	InputTokens  int
	OutputTokens int
	CacheTokens  int
	ReasonTokens int
	TurnID       string
	SourceOrder  int
}

// codexOrderRef records the source order of an item within a turn for
// stable output sequencing.
type codexOrderRef struct {
	SourceOrder int
	ItemType    codexOrderedKind
	CallID      string // for tool_call/tool_result
}

// codexEvent is a lightweight wrapper for supplemental event_msg entries
// that carry execution status, duration, or other metadata.
type codexEvent struct {
	EventType string
	Raw       json.RawMessage
}
