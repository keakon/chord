// Package protocol defines the server-to-client event wire format used by
// remote HTTP/SSE mode.
//
// Every outbound server event is encoded as an [Envelope] with a typed JSON
// payload so transports can remain simple and transport-agnostic.
package protocol

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// Envelope — unified wire format
// ---------------------------------------------------------------------------

// Envelope is the top-level JSON frame for all C/S messages.
// Both client-to-server commands and server-to-client events share this shape.
type Envelope struct {
	// ID is a unique message identifier (UUID v4).  Client-originated messages
	// set this to correlate with optional server acknowledgements.  Server
	// events use a fresh ID per envelope.
	ID string `json:"id"`

	// Type discriminates the payload kind.  See the Type* constants below.
	Type string `json:"type"`

	// Payload carries the type-specific data as raw JSON so that the
	// receiver can unmarshal lazily into the correct struct.
	Payload json.RawMessage `json:"payload"`

	// Seq is a monotonically increasing sequence number assigned by the
	// server to every outbound event.  Clients use it for gap detection
	// after reconnection (last_seq handshake).  Zero means unset.
	Seq uint64 `json:"seq,omitempty"`

	// SessionID identifies which session this event belongs to.
	// Set by the server on all session-scoped outbound events so
	// clients can filter by subscription.  Empty for global events.
	SessionID string `json:"session_id,omitempty"`
}

const (
	// TypeStreamText carries incremental LLM text output.
	TypeStreamText = "stream_text"
	// TypeStreamThinking carries a complete thinking block.
	TypeStreamThinking = "stream_thinking"
	// TypeStreamThinkingDelta carries an incremental thinking chunk for streaming display.
	TypeStreamThinkingDelta = "stream_thinking_delta"
	// TypeStreamRollback asks client to discard in-flight streamed assistant output.
	TypeStreamRollback = "stream_rollback"
	// TypeToolCallStart signals the beginning of a tool invocation.
	TypeToolCallStart = "tool_call_start"
	// TypeToolCallUpdate refreshes arguments for an already-started tool invocation.
	TypeToolCallUpdate = "tool_call_update"
	// TypeToolCallExecution updates the live execution phase of a tool card.
	TypeToolCallExecution = "tool_call_execution"
	// TypeToolResult carries a completed tool execution result.
	TypeToolResult = "tool_result"
	// TypeError reports an error from the agent.
	TypeError = "error"
	// TypeIdle signals the agent is waiting for user input.
	TypeIdle = "idle"
	// TypePlanComplete signals plan generation is done.
	TypePlanComplete = "plan_complete"
	// TypeStats carries formatted usage statistics.
	TypeStats = "stats"
	// TypeInfo carries informational messages.
	TypeInfo = "info"
	// TypeNotification carries a user-facing notification that gateway clients should surface.
	TypeNotification = "notification"
	// TypeAgentDone signals a SubAgent completed its task.
	TypeAgentDone = "agent_done"
	// TypeAgentStatus carries agent lifecycle updates.
	TypeAgentStatus = "agent_status"
	// TypeActivity carries granular activity status (connecting, streaming, etc.).
	TypeActivity = "activity"
	// TypeModelSelectRequest asks the client to show a model picker.
	TypeModelSelectRequest = "model_select_request"
	// TypeRunningModelChanged notifies the client that the active running model has changed
	// (manual switch or fallback). Allows immediate sidebar update without waiting for a snapshot.
	TypeRunningModelChanged = "running_model_changed"
	// TypeSessionSelectRequest asks the client to show the session picker (/resume).
	// When present, payload is SessionSelectRequestPayload with the session list from the server.
	TypeSessionSelectRequest = "session_select_request"
	// TypeSessionSwitchStarted signals that a local session-control operation has started
	// and the client should show transient loading feedback.
	TypeSessionSwitchStarted = "session_switch_started"
	// TypeSessionRestored signals that the conversation was restored; client should refresh view.
	TypeSessionRestored = "session_restored"
	// TypeConfirmRequest asks the client to confirm a tool invocation.
	TypeConfirmRequest = "confirm_request"
	// TypeQuestionRequest asks the client to answer a question.
	TypeQuestionRequest = "question_request"
	// TypeToast carries a transient notification for the client.
	TypeToast = "toast"
	// TypeBackgroundObjectFinished carries a lightweight runtime notification that a background object completed.
	TypeBackgroundObjectFinished = "background_object_finished"
	// TypeSnapshot carries a full state snapshot for reconnecting clients
	// whose last_seq is too old for incremental sync.
	TypeSnapshot = "snapshot"
	// TypeRoleChanged signals that the MainAgent's active role has changed.
	TypeRoleChanged = "role_changed"
	// TypeTodosUpdated carries the full replacement todo list when it changes.
	TypeTodosUpdated = "todos_updated"
	// TypeLSPDiagnostics carries LSP diagnostics for a file (workspace-level, no session_id).
	TypeLSPDiagnostics = "lsp.diagnostics"
	// TypeLSPServerState carries LSP server connection state for TUI (e.g. connected, error).
	TypeLSPServerState = "lsp.server_state"
	// TypeLSPSidebarStatus broadcasts LSP sidebar rows when connection state changes.
	TypeLSPSidebarStatus = "lsp.sidebar_status"
)

// RunningModelChangedPayload is the payload for TypeRunningModelChanged.
type RunningModelChangedPayload struct {
	AgentID          string `json:"agent_id,omitempty"`
	ProviderModelRef string `json:"provider_model_ref"`
	RunningModelRef  string `json:"running_model_ref"`
}

// ---------------------------------------------------------------------------
// Server → Client payload structs
// ---------------------------------------------------------------------------

// StreamTextPayload is the payload for TypeStreamText.
type StreamTextPayload struct {
	Text    string `json:"text"`
	AgentID string `json:"agent_id"`
}

// StreamThinkingPayload is the payload for TypeStreamThinking.
type StreamThinkingPayload struct {
	Text    string `json:"text"`
	AgentID string `json:"agent_id"`
}

// StreamThinkingDeltaPayload is the payload for TypeStreamThinkingDelta.
type StreamThinkingDeltaPayload struct {
	Text    string `json:"text"`
	AgentID string `json:"agent_id"`
}

// StreamRollbackPayload is the payload for TypeStreamRollback.
type StreamRollbackPayload struct {
	Reason  string `json:"reason,omitempty"`
	AgentID string `json:"agent_id"`
}

// ToolCallStartPayload is the payload for TypeToolCallStart.
type ToolCallStartPayload struct {
	CallID   string `json:"call_id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json"`
	AgentID  string `json:"agent_id"`
}

// ToolCallUpdatePayload is the payload for TypeToolCallUpdate.
type ToolCallUpdatePayload struct {
	CallID            string `json:"call_id"`
	Name              string `json:"name"`
	ArgsJSON          string `json:"args_json"`
	ArgsStreamingDone bool   `json:"args_streaming_done,omitempty"`
	AgentID           string `json:"agent_id"`
}

// ToolCallExecutionPayload is the payload for TypeToolCallExecution.
type ToolCallExecutionPayload struct {
	CallID   string `json:"call_id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json"`
	State    string `json:"state"` // queued or running
	AgentID  string `json:"agent_id"`
}

// ToolResultPayload is the payload for TypeToolResult.
type ToolResultPayload struct {
	CallID      string                 `json:"call_id"`
	Name        string                 `json:"name"`
	ArgsJSON    string                 `json:"args_json"`
	Audit       *message.ToolArgsAudit `json:"audit,omitempty"`
	Result      string                 `json:"result"`
	Status      string                 `json:"status,omitempty"` // success, error, cancelled
	AgentID     string                 `json:"agent_id"`
	Diff        string                 `json:"diff,omitempty"`         // unified diff for Write/Edit tools
	DiffAdded   int                    `json:"diff_added,omitempty"`   // full added-line count before any diff truncation
	DiffRemoved int                    `json:"diff_removed,omitempty"` // full removed-line count before any diff truncation
	FileCreated bool                   `json:"file_created,omitempty"` // true when Write created a file that did not previously exist
}

// ErrorPayload is the payload for TypeError.
type ErrorPayload struct {
	Message string `json:"message"`
	AgentID string `json:"agent_id"`
}

// PlanCompletePayload is the payload for TypePlanComplete.
type PlanCompletePayload struct {
	Summary  string `json:"summary"`
	PlanPath string `json:"plan_path"`
}

// RoleChangedPayload is the payload for TypeRoleChanged.
type RoleChangedPayload struct {
	Role string `json:"role"` // new active role name
}

// LSPDiagnosticsPayload is the payload for TypeLSPDiagnostics (Hub.Broadcast).
// No session_id; workspace-level.
type LSPDiagnosticsPayload struct {
	URI         string       `json:"uri"`
	ServerID    string       `json:"server_id,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Diagnostic is a single LSP diagnostic (1=Error, 2=Warning, 3=Info, 4=Hint).
type Diagnostic struct {
	Severity int    `json:"severity"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

// LSPServerStatePayload is the payload for TypeLSPServerState (optional, for TUI).
type LSPServerStatePayload struct {
	ServerID string `json:"server_id"`
	Status   string `json:"status"` // e.g. "connected", "error", "starting"
	Message  string `json:"message,omitempty"`
}

// LSPSidebarRow is one configured language server row in the TUI ENVIRONMENT block.
type LSPSidebarRow struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Pending  bool   `json:"pending,omitempty"`
	Error    string `json:"error,omitempty"`
	Errors   int    `json:"errors,omitempty"`
	Warnings int    `json:"warnings,omitempty"`
}

// LSPSidebarStatusPayload is the payload for TypeLSPSidebarStatus (Hub.Broadcast).
type LSPSidebarStatusPayload struct {
	Servers []LSPSidebarRow `json:"servers"`
}

// StatsPayload is the payload for TypeStats.
type StatsPayload struct {
	FormattedStats string `json:"formatted_stats"`
}

// InfoPayload is the payload for TypeInfo.
type InfoPayload struct {
	Message string `json:"message"`
	AgentID string `json:"agent_id,omitempty"`
}

// NotificationPayload is the payload for TypeNotification.
type NotificationPayload struct {
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

// AgentDonePayload is the payload for TypeAgentDone.
type AgentDonePayload struct {
	AgentID string `json:"agent_id"`
	TaskID  string `json:"task_id"`
	Summary string `json:"summary"`
}

// AgentStatusPayload is the payload for TypeAgentStatus.
type AgentStatusPayload struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ActivityPayload is the payload for TypeActivity.
type ActivityPayload struct {
	AgentID string `json:"agent_id"`
	Type    string `json:"type"`   // e.g. "connecting", "waiting_headers", "streaming"
	Detail  string `json:"detail"` // optional detail text
}

// ConfirmRequestPayload is the payload for TypeConfirmRequest.
type ConfirmRequestPayload struct {
	ToolName       string   `json:"tool_name"`
	ArgsJSON       string   `json:"args_json"`
	RequestID      string   `json:"request_id,omitempty"`
	TimeoutMS      int64    `json:"timeout_ms,omitempty"`
	NeedsApproval  []string `json:"needs_approval,omitempty"`
	AlreadyAllowed []string `json:"already_allowed,omitempty"`
}

// QuestionRequestPayload is the payload for TypeQuestionRequest.
type QuestionRequestPayload struct {
	ToolName      string   `json:"tool_name"`
	Header        string   `json:"header,omitempty"`
	Question      string   `json:"question"`
	Options       []string `json:"options"`
	OptionDetails []string `json:"option_details,omitempty"`
	DefaultAnswer string   `json:"default_answer"`
	Multiple      bool     `json:"multiple,omitempty"`
	RequestID     string   `json:"request_id,omitempty"`
	TimeoutMS     int64    `json:"timeout_ms,omitempty"`
}

// SessionSummaryPayload is one session entry for the session picker (TypeSessionSelectRequest).
type SessionSummaryPayload struct {
	ID                       string    `json:"id"`
	LastModTime              time.Time `json:"last_mod_time"`
	FirstUserMessage         string    `json:"first_user_message"`
	OriginalFirstUserMessage string    `json:"original_first_user_message,omitempty"`
	ForkedFrom               string    `json:"forked_from,omitempty"`
}

// SessionSelectRequestPayload is the payload for TypeSessionSelectRequest when the server sends the list.
type SessionSelectRequestPayload struct {
	Sessions []SessionSummaryPayload `json:"sessions,omitempty"`
}

// SessionSwitchStartedPayload is the payload for TypeSessionSwitchStarted.
type SessionSwitchStartedPayload struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
}

// MCPServerStatusEntry describes one configured MCP server for the TUI sidebar.
type MCPServerStatusEntry struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ToastPayload is the payload for TypeToast.
type ToastPayload struct {
	Message string `json:"message"`
	// Level is one of "info", "warn", "error".
	Level   string `json:"level"`
	AgentID string `json:"agent_id,omitempty"`
}

// BackgroundObjectFinishedPayload is the payload for TypeBackgroundObjectFinished.
type BackgroundObjectFinishedPayload struct {
	BackgroundID  string `json:"background_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Status        string `json:"status"`
	Command       string `json:"command,omitempty"`
	Description   string `json:"description,omitempty"`
	MaxRuntimeSec int    `json:"max_runtime_sec,omitempty"`
	Message       string `json:"message"`
}

func (p BackgroundObjectFinishedPayload) EffectiveID() string {
	return strings.TrimSpace(p.BackgroundID)
}

// SnapshotAgentInfo describes an active agent within a snapshot, mirroring
// the server-internal AgentInfo so the protocol package stays independent.
type SnapshotAgentInfo struct {
	ID                  string `json:"id"`
	Status              string `json:"status"`
	TaskID              string `json:"task_id,omitempty"`
	LastSummary         string `json:"last_summary,omitempty"`
	UrgentInboxCount    int    `json:"urgent_inbox_count,omitempty"`
	LastArtifactRelPath string `json:"last_artifact_rel_path,omitempty"`
	LastArtifactType    string `json:"last_artifact_type,omitempty"`
}

// AgentUsageEntry holds context (and optionally usage) for one agent so the client
// can show the focused agent's stats in the sidebar instead of a single global value.
type AgentUsageEntry struct {
	AgentID             string `json:"agent_id"`
	ContextCurrent      int    `json:"context_current,omitempty"`
	ContextLimit        int    `json:"context_limit,omitempty"`
	ContextMessageCount int    `json:"context_message_count,omitempty"`
}

// ModelOptionPayload describes a model available for runtime switching.
// It mirrors agent.ModelOption but lives in the protocol package so the wire
// format does not depend on agent internals.
type ModelOptionPayload struct {
	ProviderModel string `json:"provider_model"` // e.g. "anthropic-main/claude-opus-4.7"
	ProviderName  string `json:"provider_name"`  // e.g. "anthropic-main"
	ModelID       string `json:"model_id"`       // e.g. "claude-opus-4.7"
	ContextLimit  int    `json:"context_limit"`
	OutputLimit   int    `json:"output_limit"`
}

// UsageModelStatsPayload mirrors analytics.ModelStats for remote snapshots.
type UsageModelStatsPayload struct {
	Calls            int64   `json:"calls"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// UsageAgentStatsPayload mirrors analytics.AgentStats for remote snapshots.
type UsageAgentStatsPayload struct {
	InputTokens      int64                             `json:"input_tokens"`
	OutputTokens     int64                             `json:"output_tokens"`
	CacheReadTokens  int64                             `json:"cache_read_tokens"`
	CacheWriteTokens int64                             `json:"cache_write_tokens"`
	ReasoningTokens  int64                             `json:"reasoning_tokens"`
	LLMCalls         int64                             `json:"llm_calls"`
	EstimatedCost    float64                           `json:"estimated_cost"`
	ByModel          map[string]UsageModelStatsPayload `json:"by_model,omitempty"`
}

// SnapshotPayload is the payload for TypeSnapshot. It carries the full
// server-side state so a reconnecting client can reconstruct its view
// without incremental event replay.
type SnapshotPayload struct {
	SessionID        string               `json:"session_id"`
	ModelName        string               `json:"model_name"`
	ProviderModelRef string               `json:"provider_model_ref"`
	RunningModelRef  string               `json:"running_model_ref,omitempty"`
	Messages         []message.Message    `json:"messages"`
	Agents           []SnapshotAgentInfo  `json:"agents"`
	AvailableModels  []ModelOptionPayload `json:"available_models,omitempty"`
	LastSeq          uint64               `json:"last_seq"`
	// Context and usage for sidebar display (C/S mode).
	ContextCurrent     int                               `json:"context_current,omitempty"`
	ContextLimit       int                               `json:"context_limit,omitempty"`
	UsageInputTokens   int64                             `json:"usage_input_tokens,omitempty"`
	UsageOutputTokens  int64                             `json:"usage_output_tokens,omitempty"`
	UsageLLMCalls      int64                             `json:"usage_llm_calls,omitempty"`
	UsageEstimatedCost float64                           `json:"usage_estimated_cost,omitempty"`
	UsageByModel       map[string]UsageModelStatsPayload `json:"usage_by_model,omitempty"`
	UsageByAgent       map[string]UsageAgentStatsPayload `json:"usage_by_agent,omitempty"`
	// Per-agent context so client can show the focused agent's stats (main + sub-agents).
	AgentsUsage []AgentUsageEntry `json:"agents_usage,omitempty"`
	// Role state for the TUI status bar.
	CurrentRole     string   `json:"current_role,omitempty"`
	AvailableRoles  []string `json:"available_roles,omitempty"`
	AvailableAgents []string `json:"available_agents,omitempty"` // agent names for Handoff selection (plan mode)
	// KeysAvailable/KeysTotal for sidebar "Keys: 7/10" (only when total > 1).
	KeysAvailable int `json:"keys_available,omitempty"`
	KeysTotal     int `json:"keys_total,omitempty"`
	// Todos carries the current todo list so the client can populate its info panel on connect.
	Todos []tools.TodoItem `json:"todos,omitempty"`
	// MCPServers lists connected MCP server config names (sidebar).
	MCPServers []string `json:"mcp_servers,omitempty"`
	// MCPStatus lists every configured MCP with ok / error (sidebar red/green).
	MCPStatus []MCPServerStatusEntry `json:"mcp_status,omitempty"`
	// LSPStatus lists configured language servers (sidebar).
	LSPStatus []LSPSidebarRow `json:"lsp_status,omitempty"`
	// PendingConfirm / PendingQuestion carry unresolved interactive requests so
	// a reconnecting remote client can continue the flow.
	PendingConfirm  *ConfirmRequestPayload  `json:"pending_confirm,omitempty"`
	PendingQuestion *QuestionRequestPayload `json:"pending_question,omitempty"`
}

// TodoItemPayload mirrors tools.TodoItem for wire encoding.
type TodoItemPayload struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form,omitempty"`
}

// TodosUpdatedPayload is the payload for TypeTodosUpdated.
type TodosUpdatedPayload struct {
	Todos []tools.TodoItem `json:"todos"`
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// newUUID generates a version-4 UUID using crypto/rand.
func newUUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("protocol: crypto/rand.Read failed: %w", err)
	}
	// Set version 4 and variant bits per RFC 4122 §4.4.
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// NewEnvelope creates an Envelope with a fresh UUID and marshals the payload.
// Pass nil for event types that carry no payload.
func NewEnvelope(typ string, payload any) (*Envelope, error) {
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	env := &Envelope{
		ID:   id,
		Type: typ,
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("protocol: marshal payload for %q: %w", typ, err)
		}
		env.Payload = raw
	}
	return env, nil
}

// ParsePayload unmarshals the envelope's raw payload into the concrete type T.
// It returns an error if the payload is nil/empty or cannot be decoded.
func ParsePayload[T any](env *Envelope) (*T, error) {
	if len(env.Payload) == 0 {
		return nil, fmt.Errorf("protocol: envelope %q (%s) has no payload", env.Type, env.ID)
	}
	var v T
	if err := json.Unmarshal(env.Payload, &v); err != nil {
		return nil, fmt.Errorf("protocol: unmarshal payload for %q (%s): %w", env.Type, env.ID, err)
	}
	return &v, nil
}

// MarshalEnvelope serialises an Envelope to JSON bytes.
func MarshalEnvelope(env *Envelope) ([]byte, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("protocol: marshal envelope %q (%s): %w", env.Type, env.ID, err)
	}
	return data, nil
}

// UnmarshalEnvelope deserialises JSON bytes into an Envelope.
func UnmarshalEnvelope(data []byte) (*Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("protocol: unmarshal envelope: %w", err)
	}
	return &env, nil
}
