package message

import (
	"encoding/json"

	"github.com/keakon/chord/internal/ratelimit"
)

// ContentPart is one part of a multi-part user message (text or image).
type ContentPart struct {
	Type        string `json:"type"`                   // "text" or "image"
	Text        string `json:"text,omitempty"`         // for type="text"
	DisplayText string `json:"display_text,omitempty"` // optional TUI-only summary for large hidden text parts
	MimeType    string `json:"mime_type,omitempty"`    // for type="image", e.g. "image/png"
	Data        []byte `json:"data,omitempty"`         // for type="image", raw bytes (not persisted; loaded from ImagePath)
	ImagePath   string `json:"image_path,omitempty"`   // for type="image", path to persisted image file on disk
	FileName    string `json:"file_name,omitempty"`    // optional display name for image attachments
}

// ToolArgsAudit records how a tool call's effective execution arguments differ
// from the model's original request after user confirmation.
type ToolArgsAudit struct {
	OriginalArgsJSON  string `json:"original_args_json,omitempty"`
	EffectiveArgsJSON string `json:"effective_args_json,omitempty"`
	UserModified      bool   `json:"user_modified,omitempty"`
	EditSummary       string `json:"edit_summary,omitempty"`
}

type LSPReview struct {
	ServerID string `json:"server_id,omitempty"`
	Errors   int    `json:"errors,omitempty"`
	Warnings int    `json:"warnings,omitempty"`
}

// Clone returns a deep copy of the audit payload.
func (a *ToolArgsAudit) Clone() *ToolArgsAudit {
	if a == nil {
		return nil
	}
	cloned := *a
	return &cloned
}

// Message represents a conversation message (user, assistant, or tool result).
type Message struct {
	Role                string          `json:"role"` // "user", "assistant", "tool"
	Content             string          `json:"content"`
	Parts               []ContentPart   `json:"parts,omitempty"`                 // multi-part content (text + images); when set, supersedes Content
	ThinkingBlocks      []ThinkingBlock `json:"thinking_blocks,omitempty"`       // assistant only; must be replayed verbatim
	ToolCalls           []ToolCall      `json:"tool_calls,omitempty"`            // non-nil for assistant tool_use
	ToolCallID          string          `json:"tool_call_id,omitempty"`          // non-empty for tool results
	ToolDiff            string          `json:"tool_diff,omitempty"`             // unified diff for Write/Edit tool results
	ToolDiffAdded       int             `json:"tool_diff_added,omitempty"`       // total added lines for Write/Edit; computed before diff truncation
	ToolDiffRemoved     int             `json:"tool_diff_removed,omitempty"`     // total removed lines for Write/Edit; computed before diff truncation
	ToolDurationMs      int64           `json:"tool_duration_ms,omitempty"`      // final tool elapsed time in milliseconds for restored footer display
	LSPReviews          []LSPReview     `json:"lsp_reviews,omitempty"`           // per-server last-review snapshot for the directly edited file only
	Audit               *ToolArgsAudit  `json:"audit,omitempty"`                 // tool-call audit metadata when effective args differ after confirmation
	IsCompactionSummary bool            `json:"is_compaction_summary,omitempty"` // first user message after compaction (summary of archived history)
	StopReason          string          `json:"stop_reason,omitempty"`           // assistant only; e.g. "stop", "end_turn", "max_tokens", "tool_use"
	// Usage is the token usage for this message when it ends an LLM round (assistant only).
	// Persisted in JSONL so session resume can sum per-message usage to restore session totals.
	Usage        *TokenUsage `json:"usage,omitempty"`
	Kind         string      `json:"kind,omitempty"` // control/display subtype, e.g. "loop_notice"
	MailboxAckID string      `json:"-"`              // transient runtime-only mailbox ack marker; never persisted
}

// ToolCall represents a single tool invocation by the LLM.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolDefinition describes a tool for the LLM API's tools parameter.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// StreamDelta represents an incremental piece of a streaming LLM response.
type StreamDelta struct {
	// Type is one of: "text", "tool_use_start", "tool_use_delta", "tool_use_end",
	// "thinking", "thinking_end", "error", "status", "rate_limits", "rollback",
	// "key_switched", "key_confirmed", "key_deactivated".
	//
	// key_confirmed may carry Status.ModelRef/Status.Reason to identify the
	// effective model that produced the first visible token for the current
	// streaming attempt (used for confirming fallback/key-switch UI toasts).
	Type      string                          // delta category
	Text      string                          // for Type="text" or "thinking"
	ToolCall  *ToolCallDelta                  // for Type="tool_use_*"
	Status    *StatusDelta                    // for Type="status"
	RateLimit *ratelimit.KeyRateLimitSnapshot // for Type="rate_limits"
	Rollback  *RollbackDelta                  // for Type="rollback"
	AccountID string                          // for Type="key_deactivated": the deactivated OAuth account ID
	Email     string                          // for Type="key_deactivated": the deactivated OAuth account email, if available
	Progress  *StreamProgressDelta            // optional cumulative/request progress hint for status bar or transport diagnostics
}

// StatusDelta represents a technical state change during an LLM request.
type StatusDelta struct {
	Type     string // e.g. "connecting", "waiting_headers", etc.
	Detail   string // e.g. "retry 2/5"
	ModelRef string // non-empty when a model switch is in progress (e.g. fallback: "provider/model")
	Reason   string // optional machine-readable reason for model routing changes (e.g. fallback cause)
}

// StreamProgressDelta reports cumulative response transport progress observed by
// the provider/parser for the current request.
type StreamProgressDelta struct {
	Bytes  int64 // cumulative response bytes received so far; provider may include response headers
	Events int64 // cumulative stream event/data count received so far
}

// ToolCallDelta represents incremental tool call data during streaming.
type ToolCallDelta struct {
	ID    string
	Name  string
	Input string // partial JSON
}

// RollbackDelta indicates that the current streamed assistant output should be
// discarded (e.g. incremental chain validation failed and provider will retry
// with a full-input request).
type RollbackDelta struct {
	Reason string
}

// TokenUsage records token usage from a single API response.
// InputTokens = prompt size (total input); OutputTokens = total generated (content + reasoning if reported together).
// CacheReadTokens/CacheWriteTokens = cache read/write; ReasoningTokens = thinking/reasoning output when reported separately.
type TokenUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
	CacheWriteTokens int `json:"cache_creation_input_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// ThinkingBlock holds extended-thinking content returned by Anthropic models.
// Both fields must be preserved and sent back verbatim in subsequent API calls
// so the API can validate the conversation chain.
type ThinkingBlock struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// Response represents a complete LLM response.
type Response struct {
	Content        string
	ThinkingBlocks []ThinkingBlock // non-nil when extended thinking was enabled
	ToolCalls      []ToolCall
	Usage          *TokenUsage
	StopReason     string
	// ThinkingToolcallMarkerHit is true when provider-side reasoning content
	// contained pseudo tool-call template markers (e.g. "<|tool_call_begin|>").
	// This is observational metadata only; tool execution must still come from
	// structured ToolCalls.
	ThinkingToolcallMarkerHit bool
	// ReasoningContent holds the full accumulated reasoning/thinking text from
	// OpenAI-compatible providers. Populated only when ThinkingToolcallMarkerHit
	// is true, so the agent layer can parse pseudo tool calls from it.
	ReasoningContent string
	// ProviderResponseID is the server-assigned response ID from the Responses API
	// (response.completed / response.incomplete). Used by the Codex WebSocket
	// transport for connection-scoped previous_response_id incremental requests.
	ProviderResponseID string
}
