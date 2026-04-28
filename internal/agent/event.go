// Package agent implements the core agent loop that drives the
// LLM ↔ Tool execution cycle and exposes a stream of events for the TUI.
package agent

import (
	"context"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/tools"
)

// Internal event types used by the MainAgent event loop.
const (
	EventUserMessage        = "user_message"
	EventAppendContext      = "append_context" // append user message to ctx without calling LLM (e.g. !shell output)
	EventLLMResponse        = "llm_response"
	EventToolResult         = "tool_result"
	EventTurnCancelled      = "turn_cancelled"
	EventAgentError         = "agent_error"
	EventExecutePlan        = "execute_plan" // Internal: execute a plan file after user selects target agent (payload: *executePlanPayload)
	EventSessionControl     = "session_control"
	EventPendingDraftUpsert = "pending_draft_upsert"
	EventPendingDraftRemove = "pending_draft_remove"

	// Phase 2a event types for multi-agent orchestration.
	EventAgentDone               = "agent_done"       // SubAgent completed its task
	EventAgentIdle               = "agent_idle"       // SubAgent idle timeout (no tool calls, no Complete)
	EventAgentNotify             = "agent_notify"     // SubAgent non-blocking notify update
	EventEscalate                = "escalate"         // SubAgent requests owner/MainAgent intervention
	EventSubAgentMailbox         = "subagent_mailbox" // structured mailbox message from or about a SubAgent
	EventSubAgentStateChanged    = "subagent_state_changed"
	EventSubAgentCloseRequested  = "subagent_close_requested"
	EventSubAgentProgressUpdated = "subagent_progress_updated"
	EventSubAgentSendMessage     = "subagent_send_message"
	EventSubAgentStop            = "subagent_stop"
	EventAgentLog                = "agent_log"                  // Informational log from SubAgent (e.g. buffer overflow warning)
	EventResetNudge              = "reset_nudge"                // SubAgent activity detected; reset idle nudge counter
	EventSpawnFinished           = "background_object_finished" // Spawned background process finished; runtime-only notification
	EventContinue                = "continue"                   // re-run LLM with existing context (no new user message)
	EventLoopAssessment          = "loop_assessment"            // internal loop-controller decision point after a completed assistant round

	// Durable compaction (async worker); payloads are *compactionDraft / error.
	EventCompactionReady           = "compaction_ready"
	EventCompactionFailed          = "compaction_failed"
	EventCompactionOversizeSuspend = "compaction_oversize_suspend" // LLM call suspended due to oversize while compaction running
)

// Event is an internal event in the MainAgent event loop.
type Event struct {
	Type     string
	TurnID   uint64
	Payload  any
	Seq      uint64
	SourceID string // identifies which agent sent the event (e.g. "main", "agent-1")
}

// LLMResponsePayload wraps an LLM response for the internal event bus.
type LLMResponsePayload struct {
	Content                   string
	ThinkingBlocks            []message.ThinkingBlock
	ToolCalls                 []message.ToolCall
	StopReason                string
	ThinkingToolcallMarkerHit bool
	ReasoningContent          string              // full reasoning text when marker hit
	Usage                     *message.TokenUsage // token usage for this round; nil when the provider did not return usage; persisted with the assistant message for session resume
}

// ToolResultPayload wraps a tool execution result for the internal event bus.
type ToolResultPayload struct {
	CallID      string
	Name        string
	ArgsJSON    string
	Audit       *message.ToolArgsAudit
	Result      string
	Error       error
	TurnID      uint64
	Duration    time.Duration
	Diff        string              // unified diff for Write/Edit tools; not sent to LLM
	DiffAdded   int                 // full added-line count before any diff truncation
	DiffRemoved int                 // full removed-line count before any diff truncation
	FileCreated bool                // true when Write created a file that did not previously exist
	LSPReviews  []message.LSPReview // last-review snapshot for the directly edited file only
}

// TurnCancelledPayload carries the pending tool calls that must be explicitly
// closed in the UI when a turn is cancelled. The main agent also persists
// synthetic terminal tool-result messages for these calls so session restore can
// show them as cancelled instead of pending forever.
type TurnCancelledPayload struct {
	TurnID uint64
	Calls  []PendingToolCall
	// MarkToolCallsFailed turns synthetic terminal tool results for already
	// declared calls into error results instead of cancelled results.
	MarkToolCallsFailed bool
	// KeepPendingUserMessagesQueued suppresses the usual idle-time pending-input
	// drain so cancellation does not immediately auto-run any remaining queued
	// work on the IdleEvent it just produced.
	KeepPendingUserMessagesQueued bool
	// CommitPendingUserMessagesWithoutTurn appends queued user messages to the
	// durable context/transcript but does not start a follow-up LLM turn.
	CommitPendingUserMessagesWithoutTurn bool
}

// HandoffResult wraps the data from a Handoff tool invocation.
type HandoffResult struct {
	PlanPath string
}

type SubAgentStateChangedPayload struct {
	State   SubAgentState
	Summary string
}

type SubAgentCloseRequestedPayload struct {
	Reason       string
	ClosedReason string
	FinalState   SubAgentState
}

type SubAgentProgressUpdatedPayload struct {
	Summary string
}

type subAgentControlResult struct {
	Handle tools.TaskHandle
	Err    error
}

type SubAgentSendMessagePayload struct {
	Ctx           context.Context
	CallerAgentID string
	CallerTaskID  string
	TaskID        string
	Message       string
	Kind          string
	Reply         chan subAgentControlResult
}

type SubAgentStopPayload struct {
	Ctx           context.Context
	CallerAgentID string
	CallerTaskID  string
	TaskID        string
	Reason        string
	Reply         chan subAgentControlResult
}

// ---------------------------------------------------------------------------
// AgentEvent — events sent to the TUI (or any external consumer).
// ---------------------------------------------------------------------------

// AgentEvent is the sealed interface for events emitted to the TUI layer.
type AgentEvent interface{ agentEvent() }

// StreamTextEvent carries an incremental text chunk from the LLM.
type StreamTextEvent struct {
	Text    string
	AgentID string // originating agent ("" = main agent)
}

func (StreamTextEvent) agentEvent() {}

// ThinkingStartedEvent is emitted when the first thinking delta is received
// in a block, so the TUI can start the "thought duration" timer.
type ThinkingStartedEvent struct{}

func (ThinkingStartedEvent) agentEvent() {}

// StreamThinkingEvent carries a complete thinking block from the LLM.
// It is emitted once per thinking block (after the block is fully assembled),
// not incrementally — this avoids flooding the output channel during extended
// thinking sessions that may produce thousands of small deltas.
type StreamThinkingEvent struct {
	Text    string // full thinking content for this block
	AgentID string // originating agent ("" = main agent)
}

func (StreamThinkingEvent) agentEvent() {}

// StreamThinkingDeltaEvent carries an incremental thinking chunk for streaming display.
// Emitted every ~150ms while thinking is in progress, so users see thinking as it evolves.
// The final complete thinking block is still sent via StreamThinkingEvent on thinking_end.
type StreamThinkingDeltaEvent struct {
	Text    string // incremental thinking content since last delta
	AgentID string // originating agent ("" = main agent)
}

func (StreamThinkingDeltaEvent) agentEvent() {}

// StreamRollbackEvent asks the UI to discard the currently streaming assistant
// output for an agent (used when a provider-side incremental attempt must be
// rolled back and retried with full input).
type StreamRollbackEvent struct {
	Reason  string
	AgentID string
}

func (StreamRollbackEvent) agentEvent() {}

// ToolResultStatus is the terminal state of a tool call for UI/protocol purposes.
type ToolResultStatus string

const (
	ToolResultStatusSuccess   ToolResultStatus = "success"
	ToolResultStatusError     ToolResultStatus = "error"
	ToolResultStatusCancelled ToolResultStatus = "cancelled"
)

// ToolCallStartEvent is emitted when the LLM begins a tool invocation.
type ToolCallStartEvent struct {
	ID       string
	Name     string
	ArgsJSON string
	AgentID  string // originating agent ("" = main agent)
}

func (ToolCallStartEvent) agentEvent() {}

// LoopNoticeEvent displays the loop continuation/control note that is also
// sent to the model for the next continued request.
type LoopNoticeEvent struct {
	Title    string
	Text     string
	DedupKey string
}

func (LoopNoticeEvent) agentEvent() {}

// LoopStateChangedEvent notifies the TUI that loop-controller state changed and
// any loop-dependent pills/activity text should refresh immediately.
type LoopStateChangedEvent struct{}

func (LoopStateChangedEvent) agentEvent() {}

// ToolCallUpdateEvent refreshes the visible arguments for an already-started tool call.
// Used for streaming providers that deliver tool arguments incrementally.
// When ArgsStreamingDone is true, ArgsJSON is the final accumulated argument JSON
// for this speculative card and the temporary "chars received" indicator should
// be cleared immediately even before execution-state/result events arrive.
type ToolCallUpdateEvent struct {
	ID                string
	Name              string
	ArgsJSON          string
	ArgsStreamingDone bool
	AgentID           string // originating agent ("" = main agent)
}

func (ToolCallUpdateEvent) agentEvent() {}

// ToolCallExecutionState is the live execution phase of a visible tool card.
type ToolCallExecutionState string

const (
	ToolCallExecutionStateQueued  ToolCallExecutionState = "queued"
	ToolCallExecutionStateRunning ToolCallExecutionState = "running"
)

// ToolCallExecutionEvent updates the live execution phase for an already-visible
// tool card after finalize. It distinguishes truly queued work from actively
// running work so the TUI does not animate tools that are only waiting on a
// later batch.
type ToolCallExecutionEvent struct {
	ID       string
	Name     string
	ArgsJSON string
	State    ToolCallExecutionState
	AgentID  string // originating agent ("" = main agent)
}

func (ToolCallExecutionEvent) agentEvent() {}

// ToolProgressSnapshot is a best-effort structured progress snapshot for a
// visible running tool card. Zero values mean "no known progress".
type ToolProgressSnapshot struct {
	Label   string
	Current int64
	Total   int64
	Text    string
}

// ToolProgressEvent updates the visible progress for an already-started tool
// call. It never replaces start/end lifecycle events.
type ToolProgressEvent struct {
	CallID   string
	Name     string
	AgentID  string // originating agent ("" = main agent)
	Progress ToolProgressSnapshot
}

func (ToolProgressEvent) agentEvent() {}

// ToolResultEvent is emitted after a tool execution completes.
type ToolResultEvent struct {
	CallID      string
	Name        string
	ArgsJSON    string // full tool arguments (available after streaming completes)
	Audit       *message.ToolArgsAudit
	Result      string
	Status      ToolResultStatus
	AgentID     string // originating agent ("" = main agent)
	Diff        string // unified diff for Write/Edit tools (not sent to LLM)
	DiffAdded   int    // full added-line count before any diff truncation
	DiffRemoved int    // full removed-line count before any diff truncation
	FileCreated bool   // true when Write created a file that did not previously exist
}

func (ToolResultEvent) agentEvent() {}

// ErrorEvent carries an error that occurred during the agent loop.
type ErrorEvent struct {
	Err     error
	AgentID string // originating agent ("" = main agent)
}

func (ErrorEvent) agentEvent() {}

// AssistantMessageEvent is emitted when a finalized assistant message has been
// appended to the conversation context. This is the stable, post-streaming
// representation — no rollback or retry will change it.
//
// Consumers that need "what the assistant just said" should use this event
// rather than watching StreamTextEvent deltas or waiting for IdleEvent.
type AssistantMessageEvent struct {
	AgentID   string // originating agent ("" = main agent)
	Text      string // assistant text content (may be empty if only tool calls)
	ToolCalls int    // number of tool calls in this response
}

func (AssistantMessageEvent) agentEvent() {}

// IdleEvent signals that the agent has finished processing and is waiting
// for new user input.
type IdleEvent struct{}

func (IdleEvent) agentEvent() {}

// PendingDraftConsumedEvent signals that a queued draft was actually appended
// to the conversation context and is now part of the transcript.
type PendingDraftConsumedEvent struct {
	DraftID string
	Parts   []message.ContentPart
	AgentID string // "" = main agent
}

func (PendingDraftConsumedEvent) agentEvent() {}

// RequestCycleStartedEvent signals that the runtime has started a new main
// request cycle. TUI should reset request-scoped display progress immediately;
// subsequent transport progress belongs to the new cycle.
type RequestCycleStartedEvent struct {
	AgentID string // "" = main agent
	TurnID  uint64
}

func (RequestCycleStartedEvent) agentEvent() {}

// UsageUpdatedEvent signals that session usage (input/output tokens, cost)
// was just updated after an LLM round. In C/S mode the server uses this to
// push context_usage to clients so the sidebar updates during tool-call loops,
// not only after the turn ends (IdleEvent).
type UsageUpdatedEvent struct{}

func (UsageUpdatedEvent) agentEvent() {}

// HandoffEvent signals that plan generation has finished. The TUI prompts
// the user to select a target agent for execution (default: builder).
type HandoffEvent struct {
	PlanPath string
}

func (HandoffEvent) agentEvent() {}

// InfoEvent carries an informational message for display in the TUI.
// Used for non-error status messages (e.g. export/resume success).
type InfoEvent struct {
	Message string
	AgentID string // originating agent ("" = main agent)
}

func (InfoEvent) agentEvent() {}

// ToastEvent carries a transient notification message.
// Level is one of: "info", "warn", "error".
type ToastEvent struct {
	Message string
	Level   string
	AgentID string // originating agent ("" = main agent)
}

func (ToastEvent) agentEvent() {}

// CompactionStatusEvent drives the TUI background compaction slot precisely.
// Status is one of: "started", "succeeded", "failed", "cancelled".
// Bytes/Events are optional and currently reserved for future dedicated
// compaction-progress wiring.
type CompactionStatusEvent struct {
	Status string
	Bytes  int64
	Events int64
}

func (CompactionStatusEvent) agentEvent() {}

// AgentDoneEvent signals that a SubAgent has completed its task.
// Emitted to TUI so it can update the sidebar and optionally switch focus.
type AgentDoneEvent struct {
	AgentID string // instance ID (e.g. "agent-1")
	TaskID  string // plan task ID (e.g. "3") or ad-hoc ID (e.g. "adhoc-1")
	Summary string // completion summary from Complete tool
}

func (AgentDoneEvent) agentEvent() {}

// AgentStatusEvent carries a SubAgent status update for the TUI sidebar.
// Used to reflect agent lifecycle changes (running, idle, error, etc.).
type AgentStatusEvent struct {
	AgentID string // instance ID (e.g. "agent-1")
	Status  string // e.g. "running", "idle", "error", "done"
	Message string // human-readable detail
}

func (AgentStatusEvent) agentEvent() {}

// ModelSelectEvent signals the TUI to open the model selector overlay.
// Emitted in response to the /model slash command (without arguments).
type ModelSelectEvent struct{}

func (ModelSelectEvent) agentEvent() {}

// RunningModelChangedEvent signals that the active running model has changed.
// Emitted after a manual model switch or fallback switch so the TUI can
// update the sidebar without waiting for the next idle snapshot.
type RunningModelChangedEvent struct {
	AgentID          string // "" = main agent, non-empty = sub-agent instance ID
	ProviderModelRef string // selected model (user's choice)
	RunningModelRef  string // actually running model (may differ during fallback)
}

func (RunningModelChangedEvent) agentEvent() {}

// RoleChangedEvent signals that the MainAgent's active role has changed.
// The TUI uses this to update the role pill in the status bar.
type RoleChangedEvent struct {
	Role string // new active role name (e.g. "planner", "builder")
}

func (RoleChangedEvent) agentEvent() {}

// SessionSelectEvent signals the TUI to open the session picker overlay.
// Emitted when the user runs /resume with no arguments; the user then
// chooses a session from the list to restore.
// Sessions, when non-nil, is the list from the server (C/S mode); the TUI uses
// it instead of calling ListSessionSummaries() so remote clients get the list.
type SessionSelectEvent struct {
	Sessions []SessionSummary // optional: pre-fetched list from server
}

func (SessionSelectEvent) agentEvent() {}

// SessionSwitchStartedEvent signals that a local session-control operation has
// started and the TUI should show transient loading feedback until the switch
// either completes (SessionRestoredEvent) or fails (ErrorEvent/IdleEvent).
// Kind is one of: "resume", "new", "fork".
type SessionSwitchStartedEvent struct {
	Kind      string
	SessionID string // optional target session ID (for /resume)
}

func (SessionSwitchStartedEvent) agentEvent() {}

// SessionRestoredEvent signals that the conversation was restored from
// a persisted session (e.g. after /resume <id>). The TUI should rebuild
// the viewport from the current messages so the restored history is visible.
type SessionRestoredEvent struct{}

func (SessionRestoredEvent) agentEvent() {}

// ForkSessionEvent is emitted after a fork (ee chord) operation completes.
// Parts holds the content of the forked message so the TUI can load it
// into the composer for editing.
type ForkSessionEvent struct {
	Parts []message.ContentPart
}

func (ForkSessionEvent) agentEvent() {}

// ContextUsageUpdateEvent is emitted by the remote client when it receives
// a context_usage envelope from the server. It does not carry data; the TUI
// should re-read GetContextStats/GetUsageStats to refresh the sidebar.
// Used only in C/S mode so the sidebar updates after Idle.
type ContextUsageUpdateEvent struct{}

func (ContextUsageUpdateEvent) agentEvent() {}

// EnvStatusUpdateEvent signals that background environment state changed
// (currently MCP connectivity), so the TUI should re-read state providers.
type EnvStatusUpdateEvent struct{}

func (EnvStatusUpdateEvent) agentEvent() {}

// SpawnFinishedEvent is emitted when a background process started by Spawn completes.
// It is a lightweight runtime notification; stdout/stderr remain in the returned log_file.
type SpawnFinishedEvent struct {
	BackgroundID  string
	AgentID       string // originating agent ("" = main agent)
	Kind          string
	Status        string
	Command       string
	Description   string
	MaxRuntimeSec int
	Message       string
}

func (e SpawnFinishedEvent) EffectiveID() string {
	return strings.TrimSpace(e.BackgroundID)
}

func (SpawnFinishedEvent) agentEvent() {}

// RateLimitUpdatedEvent is emitted when a new rate-limit snapshot is available
// for the current API key. The TUI should re-read CurrentRateLimitSnapshot.
type RateLimitUpdatedEvent struct {
	Snapshot *ratelimit.KeyRateLimitSnapshot
}

func (RateLimitUpdatedEvent) agentEvent() {}

// KeyPoolChangedEvent signals that API key availability (cooldown / selection)
// changed; the TUI should re-read KeyStats and schedule key-pool ticks.
type KeyPoolChangedEvent struct{}

func (KeyPoolChangedEvent) agentEvent() {}

// ConfirmRequestEvent is sent to the TUI when a tool invocation requires user
// confirmation. The TUI shows the dialog and then calls ResolveConfirm on the
// agent with the user's choice.
type ConfirmRequestEvent struct {
	ToolName       string
	ArgsJSON       string
	RequestID      string
	Timeout        time.Duration
	NeedsApproval  []string
	AlreadyAllowed []string
}

func (ConfirmRequestEvent) agentEvent() {}

// QuestionRequestEvent is sent to the TUI when the agent asks a structured
// question. The TUI shows the dialog and then calls ResolveQuestion.
type QuestionRequestEvent struct {
	ToolName      string
	Header        string
	Question      string
	Options       []string
	OptionDetails []string
	DefaultAnswer string
	Multiple      bool
	RequestID     string
	Timeout       time.Duration
}

func (QuestionRequestEvent) agentEvent() {}

// ---------------------------------------------------------------------------
// Granular activity tracking (Phase 2c)
// ---------------------------------------------------------------------------

// ActivityType represents the specific technical state of an agent's LLM or tool loop.
type ActivityType string

const (
	ActivityIdle           ActivityType = "idle"
	ActivityConnecting     ActivityType = "connecting"
	ActivityWaitingHeaders ActivityType = "waiting_headers"
	ActivityWaitingToken   ActivityType = "waiting_token"
	ActivityStreaming      ActivityType = "streaming"
	ActivityExecuting      ActivityType = "executing"
	ActivityCompacting     ActivityType = "compacting"
	ActivityRetrying       ActivityType = "retrying"
	ActivityRetryingKey    ActivityType = "retrying_key"
	ActivityCooling        ActivityType = "cooling"
	ActivityVerifying      ActivityType = "verifying"
)

// AgentActivityEvent is emitted to the TUI to show real-time progress.
type AgentActivityEvent struct {
	AgentID string
	Type    ActivityType
	Detail  string // e.g. "3 tools", "retry 2/6", "cooldown 5s"
}

func (AgentActivityEvent) agentEvent() {}

// RequestProgressEvent reports cumulative visible response progress for the
// currently active request of an agent. Bytes are cumulative received bytes for
// visible response payload/status metadata tracked by the agent; Events counts
// visible stream/status events received so far. Zero values mean "no known
// visible progress yet".
type RequestProgressEvent struct {
	AgentID string
	Bytes   int64
	Events  int64
	Done    bool
}

func (RequestProgressEvent) agentEvent() {}

// TodosUpdatedEvent is emitted when the todo list changes via TodoWrite.
type TodosUpdatedEvent struct {
	Todos []tools.TodoItem
}

func (TodosUpdatedEvent) agentEvent() {}
