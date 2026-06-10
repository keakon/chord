package message

import (
	"encoding/json"

	"github.com/keakon/chord/internal/ratelimit"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentPartType string

func (t ContentPartType) String() string {
	return string(t)
}

const (
	ContentPartText  ContentPartType = "text"
	ContentPartImage ContentPartType = "image"
	ContentPartPDF   ContentPartType = "pdf"
)

const StatusDeltaWaitingHeaders = "waiting_headers"

const (
	ToolStatusSuccess   = "success"
	ToolStatusError     = "error"
	ToolStatusCancelled = "cancelled"
)

// ContentPart is one part of a multi-part user message (text, image, or pdf).
type ContentPart struct {
	Type        ContentPartType `json:"type"`                   // "text", "image", or "pdf"
	Text        string          `json:"text,omitempty"`         // for type="text"
	DisplayText string          `json:"display_text,omitempty"` // optional TUI-only summary for large hidden text parts
	InlineToken string          `json:"inline_token,omitempty"` // optional TUI-only marker for atomic inline composer tokens
	MimeType    string          `json:"mime_type,omitempty"`    // for type="image"/"pdf", e.g. "image/png" or "application/pdf"
	Data        []byte          `json:"data,omitempty"`         // for type="image"/"pdf", raw bytes (not persisted; loaded from ImagePath)
	ImagePath   string          `json:"image_path,omitempty"`   // for type="image"/"pdf", path to persisted file on disk
	FileName    string          `json:"file_name,omitempty"`    // optional display name for image/pdf attachments
}

// IsBinary reports whether the part carries out-of-band binary bytes (an image
// or a PDF) rather than inline text. Binary parts have their Data persisted to
// a session file and reloaded on restore, so persistence/restore/clone paths
// treat image and pdf parts identically.
func (p ContentPart) IsBinary() bool {
	return p.Type == ContentPartImage || p.Type == ContentPartPDF
}

// ToolArgsAudit records how a tool call's effective execution arguments differ
// from the model's original request after user confirmation.
type ToolArgsAudit struct {
	OriginalArgsJSON  string `json:"original_args_json,omitempty"`
	EffectiveArgsJSON string `json:"effective_args_json,omitempty"`
	UserModified      bool   `json:"user_modified,omitempty"`
	EditSummary       string `json:"edit_summary,omitempty"`
}

// ToolFileState records durable file-state metadata emitted by file tools.
// It is used only to restore runtime safety sentinels across session restore;
// file contents are not persisted here or re-injected into model context.
type ToolFileState struct {
	Reads   []TrackedFileState `json:"reads,omitempty"`
	Writes  []TrackedFileState `json:"writes,omitempty"`
	Deletes []TrackedFileState `json:"deletes,omitempty"`
}

// TrackedFileState records the observed state of one file at tool completion.
type TrackedFileState struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Exists bool   `json:"exists"`
}

func (s *ToolFileState) Clone() *ToolFileState {
	if s == nil {
		return nil
	}
	cloned := &ToolFileState{}
	if len(s.Reads) > 0 {
		cloned.Reads = append([]TrackedFileState(nil), s.Reads...)
	}
	if len(s.Writes) > 0 {
		cloned.Writes = append([]TrackedFileState(nil), s.Writes...)
	}
	if len(s.Deletes) > 0 {
		cloned.Deletes = append([]TrackedFileState(nil), s.Deletes...)
	}
	return cloned
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
	Role                Role               `json:"role"` // "user", "assistant", "tool"
	Content             string             `json:"content"`
	Parts               []ContentPart      `json:"parts,omitempty"`                 // multi-part content (text + images); when set, supersedes Content
	ThinkingBlocks      []ThinkingBlock    `json:"thinking_blocks,omitempty"`       // assistant only; must be replayed verbatim
	ReasoningContent    string             `json:"reasoning_content,omitempty"`     // assistant only; OpenAI-compatible reasoning/thinking text for chain replay
	ToolCalls           []ToolCall         `json:"tool_calls,omitempty"`            // non-nil for assistant tool_use
	ToolCallID          string             `json:"tool_call_id,omitempty"`          // non-empty for tool results
	ToolDiff            string             `json:"tool_diff,omitempty"`             // unified diff for Write/Edit tool results
	ToolDiffAdded       int                `json:"tool_diff_added,omitempty"`       // total added lines for Write/Edit; computed before diff truncation
	ToolDiffRemoved     int                `json:"tool_diff_removed,omitempty"`     // total removed lines for Write/Edit; computed before diff truncation
	ToolDurationMs      int64              `json:"tool_duration_ms,omitempty"`      // final tool elapsed time in milliseconds for restored footer display
	ToolStatus          string             `json:"tool_status,omitempty"`           // terminal tool status: success|error|cancelled
	FileState           *ToolFileState     `json:"file_state,omitempty"`            // durable file-state metadata for restore-time safety sentinels
	LSPReviews          []LSPReview        `json:"lsp_reviews,omitempty"`           // per-server last-review snapshot for the directly edited file only
	Audit               *ToolArgsAudit     `json:"audit,omitempty"`                 // tool-call audit metadata when effective args differ after confirmation
	IsCompactionSummary bool               `json:"is_compaction_summary,omitempty"` // first user message after compaction (summary of archived history)
	StopReason          string             `json:"stop_reason,omitempty"`           // assistant only; e.g. "stop", "end_turn", "max_tokens", "tool_use"
	Provenance          *MessageProvenance `json:"provenance,omitempty"`            // optional producer/source metadata for model-compat replay decisions
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
	// "key_switched", "key_confirmed", "key_deactivated", "key_invalidated", "key_expired".
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
	AccountID string                          // for Type="key_deactivated"/"key_invalidated"/"key_expired": the OAuth account ID
	Email     string                          // for Type="key_deactivated"/"key_invalidated"/"key_expired": the OAuth account email, if available
	Progress  *StreamProgressDelta            // optional cumulative/request progress hint for status bar or transport diagnostics
}

const (
	StreamDeltaText        = "text"
	StreamDeltaThinking    = "thinking"
	StreamDeltaThinkingEnd = "thinking_end"
	StreamDeltaError       = "error"
	StreamDeltaStatus      = "status"
	StreamDeltaRateLimits  = "rate_limits"
	StreamDeltaRollback    = "rollback"

	StreamDeltaToolUseStart = "tool_use_start"
	StreamDeltaToolUseDelta = "tool_use_delta"
	StreamDeltaToolUseEnd   = "tool_use_end"

	StreamDeltaKeySwitched    = "key_switched"
	StreamDeltaKeyConfirmed   = "key_confirmed"
	StreamDeltaKeyDeactivated = "key_deactivated"
	StreamDeltaKeyInvalidated = "key_invalidated"
	StreamDeltaKeyExpired     = "key_expired"
)

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
// CacheReadTokens/CacheWriteTokens = cache read/write; CacheWrite1hTokens is the 1-hour TTL subset of CacheWriteTokens when reported separately; ReasoningTokens = thinking/reasoning output when reported separately.
type TokenUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	CacheReadTokens    int `json:"cache_read_input_tokens"`
	CacheWriteTokens   int `json:"cache_creation_input_tokens"`
	CacheWrite1hTokens int `json:"cache_creation_1h_input_tokens,omitempty"`
	ReasoningTokens    int `json:"reasoning_tokens"`
}

// ThinkingBlock holds extended-thinking content returned by Anthropic models.
// Both fields must be preserved and sent back verbatim in subsequent API calls
// so the API can validate the conversation chain.
type ThinkingBlock struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// MessageProvenance captures the producer/source metadata of a persisted
// message. It is optional and primarily used for request-time model-compat
// normalization, where provider-specific payloads may need to be replayed,
// downgraded, or dropped depending on the current target model.
type MessageProvenance struct {
	Source     string `json:"source,omitempty"`      // chord|import:claude|import:codex|import:opencode
	ProviderID string `json:"provider_id,omitempty"` // anthropic-main / openai / gemini ...
	ModelID    string `json:"model_id,omitempty"`
	Variant    string `json:"variant,omitempty"`
	ModelRef   string `json:"model_ref,omitempty"`   // provider/model[@variant]
	WireFamily string `json:"wire_family,omitempty"` // anthropic|openai-chat|openai-responses|gemini|unknown
	Imported   bool   `json:"imported,omitempty"`
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
	// OpenAI-compatible providers. It is populated whenever the transport emits
	// reasoning/thinking text, and marker hits only add observational metadata so
	// the agent layer can decide whether pseudo tool-call parsing is applicable.
	ReasoningContent string
	// ProviderResponseID is the server-assigned response ID from the Responses API
	// (response.completed / response.incomplete). Used by the Codex WebSocket
	// transport for connection-scoped previous_response_id incremental requests.
	ProviderResponseID string
}
